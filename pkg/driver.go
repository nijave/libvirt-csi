package pkg

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/container-storage-interface/spec/lib/go/csi"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"k8s.io/klog/v2"
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
)

// TODO figure out max disks that can be attached to a libvirt domain. Looks like this used to be 26 but maybe
// this has been increased since then
const scsiControllerAvailable = 20
const defaultFilesystem = "ext4"

type BlockDevice struct {
	Name   string `json:"name"`
	Serial string `json:"serial"`
}

type BlockDeviceList struct {
	BlockDevices []BlockDevice `json:"blockdevices"`
}

type LibvirtCsiDriver struct {
	csi.NodeServer
}

func (s *LibvirtCsiDriver) NodeGetInfo(ctx context.Context, req *csi.NodeGetInfoRequest) (*csi.NodeGetInfoResponse, error) {
	logRequest("NodeGetInfo", req)
	return &csi.NodeGetInfoResponse{
		NodeId:             os.Getenv("KUBE_NODE_NAME"),
		MaxVolumesPerNode:  scsiControllerAvailable,
		AccessibleTopology: nil,
	}, nil
}

func (s *LibvirtCsiDriver) NodeGetCapabilities(ctx context.Context, req *csi.NodeGetCapabilitiesRequest) (*csi.NodeGetCapabilitiesResponse, error) {
	//logRequest("NodeGetCapabilities", req)
	// https://kubernetes-csi.github.io/docs/developing.html#capabilities
	return &csi.NodeGetCapabilitiesResponse{
		Capabilities: []*csi.NodeServiceCapability{
			{
				Type: &csi.NodeServiceCapability_Rpc{
					Rpc: &csi.NodeServiceCapability_RPC{
						Type: csi.NodeServiceCapability_RPC_GET_VOLUME_STATS,
					},
				},
			},
		},
	}, nil
}

// NodePublishVolume Mount a volume to the target path
func (s *LibvirtCsiDriver) NodePublishVolume(ctx context.Context, req *csi.NodePublishVolumeRequest) (*csi.NodePublishVolumeResponse, error) {
	logRequest("NodePublishVolume", req)

	response := &csi.NodePublishVolumeResponse{}

	// Determine filesystem type
	fsType := defaultFilesystem
	if req.GetVolumeCapability() != nil && req.GetVolumeCapability().GetMount() != nil && req.GetVolumeCapability().GetMount().GetFsType() != "" {
		fsType = req.GetVolumeCapability().GetMount().GetFsType()
	}
	klog.V(8).Infof("using fstype %s", fsType)

	// Find block device from pvc ID (vhd id)
	blockDeviceCmd := exec.CommandContext(ctx, "lsblk", "--nodeps", "-o", "serial,name", "-J", "--include", "8,254")
	blockDeviceJson, err := blockDeviceCmd.Output()
	if err != nil {
		return response, err
	}
	var blockDevices BlockDeviceList
	err = json.Unmarshal(blockDeviceJson, &blockDevices)
	if err != nil {
		return response, err
	}

	var targetDevice string
	volumeSerial := strings.Replace(strings.TrimPrefix(req.VolumeId, "pv-"), "-", "", -1)
	for _, blockDevice := range blockDevices.BlockDevices {
		klog.InfoS("searching for device", "volumeId", req.VolumeId, "volumeSerial", volumeSerial, "blockSerial", blockDevice.Serial)
		// Some serial numbers are truncated
		if strings.HasPrefix(volumeSerial, blockDevice.Serial) {
			targetDevice = blockDevice.Name
			break
		}
	}

	if targetDevice == "" {
		klog.ErrorS(err, "couldn't find device for volume", "volumeSerial", volumeSerial, "blockDevices", blockDevices.BlockDevices, "cmdOutput", blockDeviceJson)
		return response, errors.New("device not found")
	}

	// Partition block device, if needed
	devicePath := fmt.Sprintf("/dev/%s", targetDevice)
	partitionPath := fmt.Sprintf("%s%d", devicePath, 1)
	if _, err = os.Stat(partitionPath); err != nil {
		klog.InfoS("partitioning pv", "pv", req.VolumeId)
		shellCommand := []string{devicePath, "--script", "-a", "optimal", "mklabel", "gpt", "mkpart", "primary", fsType, "0%", "100%"}
		if out, partErr := exec.CommandContext(ctx, "parted", shellCommand...).Output(); partErr != nil {
			klog.ErrorS(partErr, "failed to partition disk", "command", shellCommand, "output", string(out))
			return response, partErr
		}
	}

	// Format block device, if needed
	out, err := exec.CommandContext(ctx, "blkid", "-o", "value", "-s", "TYPE", partitionPath).Output()
	if err != nil {
		klog.ErrorS(err, "couldn't determine partition fstype", "partition", partitionPath, "output", out)
		return response, err
	}
	if len(out) == 0 {
		klog.InfoS("formatting pv", "pv", req.VolumeId, "fsType", fsType)
		out, err := exec.CommandContext(ctx, "mkfs", "-t", fsType, partitionPath).Output()
		if err != nil {
			klog.ErrorS(err, "couldn't format partition", "fsType", fsType, "partition", partitionPath, "output", out)
			return response, err
		}
	}

	klog.InfoS("creating mount point directory", "directory", req.TargetPath)
	err = os.MkdirAll(req.TargetPath, 0700)

	// Construct mount command
	mountCommand := make([]string, 0)
	var mountFlags []string
	if req.GetVolumeCapability() != nil && req.GetVolumeCapability().GetMount() != nil {
		mountFlags = req.GetVolumeCapability().GetMount().GetMountFlags()
	}
	if len(mountFlags) > 0 {
		// TODO, I think this works right... (need to verify what's actually in mount flags array)
		mountCommand = append(mountCommand, "-o")
		mountCommand = append(mountCommand, strings.Join(mountFlags, ","))
	}
	mountCommand = append(mountCommand, partitionPath)
	mountCommand = append(mountCommand, req.TargetPath)

	// Mount partition
	klog.InfoS("running command", "command", mountCommand)
	out, err = exec.CommandContext(ctx, "mount", mountCommand...).Output()
	if err != nil {
		var exitErr *exec.ExitError
		stderrMsg := "null"
		if errors.As(err, &exitErr) {
			stderrMsg = string(exitErr.Stderr)
		}
		klog.ErrorS(err, "failed to mount volume", "stdout", string(out), "stderr", stderrMsg)

		if err.Error() == "exit status 32" {
			if strings.Contains(stderrMsg, " already mounted on ") {
				return response, nil
			} else if strings.HasSuffix(stderrMsg, " does not exist.\n") {
				return response, status.Error(codes.NotFound, "volume not found")
			}
		}

		klog.ErrorS(err, "volume mount error", "output", out)
	}

	return response, err
}

// NodeUnpublishVolume Unmount a volume from the target path
func (s *LibvirtCsiDriver) NodeUnpublishVolume(ctx context.Context, req *csi.NodeUnpublishVolumeRequest) (*csi.NodeUnpublishVolumeResponse, error) {
	logRequest("NodeUnpublishVolume", req)

	response := &csi.NodeUnpublishVolumeResponse{}
	var err error
	out, err := exec.CommandContext(ctx, "umount", req.TargetPath).Output()
	exec.CommandContext(ctx, "/usr/bin/rmdir", req.TargetPath)
	if err != nil {
		if err.Error() == "exit status 32" {
			klog.Warningf("failed to unmount %s '%s'", req.VolumeId, string(out))
			// TODO this seemed to get stuck unless I return a normal request
			// despite the docs suggesting this error should be returned
			//return response, status.Error(codes.NotFound, "volume not found")
			return response, nil
		} else {
			klog.ErrorS(err, "volume unmount error", "output", out)
		}
	}

	return response, err
}

// NodeStageVolume Not supported capability
func (s *LibvirtCsiDriver) NodeStageVolume(ctx context.Context, req *csi.NodeStageVolumeRequest) (*csi.NodeStageVolumeResponse, error) {
	logRequest("NodeStageVolume", req)
	return nil, status.Error(codes.Unimplemented, "method NodeStageVolume not implemented")
}

// NodeUnstageVolume Not supported capability
func (s *LibvirtCsiDriver) NodeUnstageVolume(ctx context.Context, req *csi.NodeUnstageVolumeRequest) (*csi.NodeUnstageVolumeResponse, error) {
	logRequest("NodeUnstageVolume", req)
	return nil, status.Error(codes.Unimplemented, "method NodeUnstageVolume not implemented")
}

// NodeGetVolumeStats Not supported capability
func (s *LibvirtCsiDriver) NodeGetVolumeStats(ctx context.Context, req *csi.NodeGetVolumeStatsRequest) (*csi.NodeGetVolumeStatsResponse, error) {
	// These requests are pretty frequent
	//logRequest("NodeGetVolumeStats", req)

	out, err := exec.CommandContext(ctx, "df", "-B", "1", "--output=iavail,itotal,iused,avail,size,used", req.GetStagingTargetPath()).Output()
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) && strings.HasSuffix(string(exitErr.Stderr), "No such file or directory\n") {
			return &csi.NodeGetVolumeStatsResponse{}, status.Error(codes.NotFound, "volume not found")
		}
		klog.Error(err)
	}

	statsLine := strings.Split(string(out), "\n")[1]
	re := regexp.MustCompile("\\s+")
	stats := re.Split(statsLine, -1)
	klog.InfoS("stats line", "volume", req.GetStagingTargetPath(), "line", stats)

	inodesAvail, _ := strconv.ParseInt(stats[0], 10, 64)
	inodesTotal, _ := strconv.ParseInt(stats[1], 10, 64)
	inodesUsed, _ := strconv.ParseInt(stats[2], 10, 64)

	bytesAvail, _ := strconv.ParseInt(stats[3], 10, 64)
	bytesTotal, _ := strconv.ParseInt(stats[4], 10, 64)
	bytesUsed, _ := strconv.ParseInt(stats[5], 10, 64)

	response := &csi.NodeGetVolumeStatsResponse{
		Usage: []*csi.VolumeUsage{{
			Unit:      csi.VolumeUsage_INODES,
			Available: inodesAvail,
			Total:     inodesTotal,
			Used:      inodesUsed,
		}, {
			Unit:      csi.VolumeUsage_BYTES,
			Available: bytesAvail,
			Total:     bytesTotal,
			Used:      bytesUsed,
		}},
		VolumeCondition: &csi.VolumeCondition{Abnormal: false, Message: ""},
	}

	return response, nil
}

// NodeExpandVolume Not supported capability
func (s *LibvirtCsiDriver) NodeExpandVolume(ctx context.Context, req *csi.NodeExpandVolumeRequest) (*csi.NodeExpandVolumeResponse, error) {
	logRequest("NodeExpandVolume", req)
	return nil, status.Error(codes.Unimplemented, "method NodeExpandVolume not implemented")
}
