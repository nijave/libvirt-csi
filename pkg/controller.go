package pkg

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/alessio/shellescape"
	"github.com/container-storage-interface/spec/lib/go/csi"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/wrapperspb"
	"k8s.io/klog/v2"
	"strings"
)

type remoteSshRunner interface {
	RunCommand(string) (string, string, error)
}

type LibvirtCsiController struct {
	csi.IdentityServer
	csi.ControllerServer
	CommandRunner remoteSshRunner
}

const driverName = "libvirt-csi.nijave.github.com"
const driverVersion = "1.0.0"
const defaultCapacity = 20 // GB

type ExecResult struct {
	ExitCode int
	Output   string
	Error    error
}

type VolumeInfo struct {
	Id       string
	Capacity int64
	Owners   []string
}

// IdentityServer

func (s *LibvirtCsiController) Probe(ctx context.Context, request *csi.ProbeRequest) (*csi.ProbeResponse, error) {
	logRequest("identity probe", request)
	return &csi.ProbeResponse{Ready: &wrapperspb.BoolValue{Value: true}}, nil
}

func (s *LibvirtCsiController) GetPluginInfo(ctx context.Context, request *csi.GetPluginInfoRequest) (*csi.GetPluginInfoResponse, error) {
	logRequest("identity plugin info", request)
	return &csi.GetPluginInfoResponse{
		Name:          driverName,
		VendorVersion: driverVersion,
	}, nil
}

func (s *LibvirtCsiController) GetPluginCapabilities(ctx context.Context, request *csi.GetPluginCapabilitiesRequest) (*csi.GetPluginCapabilitiesResponse, error) {
	logRequest("identity plugin capabilities", request)

	return &csi.GetPluginCapabilitiesResponse{
		Capabilities: []*csi.PluginCapability{
			{
				Type: &csi.PluginCapability_Service_{
					Service: &csi.PluginCapability_Service{
						Type: csi.PluginCapability_Service_CONTROLLER_SERVICE,
					},
				},
			},
		},
	}, nil
}

// ControllerServer

func (s *LibvirtCsiController) ListVolumes(ctx context.Context, request *csi.ListVolumesRequest) (*csi.ListVolumesResponse, error) {
	logRequest("listing volumes", request)

	var volumeInfo []VolumeInfo
	stdout, stderr, err := s.CommandRunner.RunCommand(fmt.Sprintf(
		"sudo libvirt-storage-attach -operation=list",
	))

	if err != nil {
		klog.InfoS("error running libvirt-storage-attach", "operation", "list", "stdout", stdout, "stderr", stderr, "err", err.Error())
		return nil, err
	}

	err = json.Unmarshal([]byte(stdout), &volumeInfo)
	if err != nil {
		return nil, err
	}

	var volumeList []*csi.ListVolumesResponse_Entry
	for _, volume := range volumeInfo {
		volumeList = append(volumeList, &csi.ListVolumesResponse_Entry{
			Volume: &csi.Volume{
				VolumeId:           volume.Id,
				CapacityBytes:      volume.Capacity,
				VolumeContext:      nil,
				ContentSource:      nil,
				AccessibleTopology: nil,
			},
			Status: &csi.ListVolumesResponse_VolumeStatus{
				PublishedNodeIds: volume.Owners, // OPTIONAL
				VolumeCondition:  nil,           // OPTIONAL
			},
		})
	}

	return &csi.ListVolumesResponse{
		Entries:   volumeList,
		NextToken: "", // TODO pagination
	}, nil
}

func (s *LibvirtCsiController) CreateVolume(ctx context.Context, request *csi.CreateVolumeRequest) (*csi.CreateVolumeResponse, error) {
	logRequest("creating volume", request)

	volumeGroup := ""
	if vg, ok := request.Parameters["volumeGroup"]; ok {
		volumeGroup = vg
	}

	response := &csi.CreateVolumeResponse{
		Volume: &csi.Volume{
			VolumeId:      "",
			CapacityBytes: 0,
			VolumeContext: map[string]string{
				"volumeGroup": volumeGroup,
			},
			ContentSource:      nil,
			AccessibleTopology: nil,
		},
	}

	if len(request.VolumeCapabilities) > 0 {
		if request.VolumeCapabilities[0].AccessMode.GetMode() != csi.VolumeCapability_AccessMode_SINGLE_NODE_WRITER {
			capabilities := make([]string, len(request.VolumeCapabilities))
			for _, capability := range request.VolumeCapabilities {
				capabilities = append(capabilities, capability.String())
			}
			klog.InfoS("unsupported capabilities", "capabilities", strings.Join(capabilities, ","))
			return response, status.Error(codes.InvalidArgument, "")
		}
	}

	var capacity int64
	capacity = defaultCapacity * 1024 * 1024 * 1024
	if request.CapacityRange != nil {
		if request.CapacityRange.LimitBytes > 0 {
			capacity = request.CapacityRange.LimitBytes
		}
		if request.CapacityRange.RequiredBytes > 0 {
			capacity = request.CapacityRange.RequiredBytes
		}
	}

	response.Volume.CapacityBytes = capacity

	createPvCommand := fmt.Sprintf(
		"sudo libvirt-storage-attach -operation=create -volume-group=%s -size=%d",
		volumeGroup,
		request.CapacityRange.RequiredBytes,
	)
	klog.InfoS("creating volume", "command", createPvCommand)
	stdout, stderr, err := s.CommandRunner.RunCommand(createPvCommand)

	hopefullyVolumeId := strings.TrimSpace(stdout)

	if !strings.HasPrefix(hopefullyVolumeId, "pv-") || err != nil {
		errMsg := ""
		if err != nil {
			errMsg = err.Error()
		}
		klog.InfoS("error running libvirt-storage-attach", "operation", "create", "stdout", stdout, "stderr", stderr, "err", errMsg, "parsedVolumeId", hopefullyVolumeId)
		err = errors.New("unknown error creating volume")
	} else {
		response.Volume.VolumeId = hopefullyVolumeId
	}

	return response, err
}

func (s *LibvirtCsiController) DeleteVolume(ctx context.Context, request *csi.DeleteVolumeRequest) (*csi.DeleteVolumeResponse, error) {
	logRequest("deleting volume", request)
	response := &csi.DeleteVolumeResponse{}

	stdout, stderr, err := s.CommandRunner.RunCommand(fmt.Sprintf(
		"sudo libvirt-storage-attach -operation=delete -pv-id=%s",
		shellescape.Quote(request.VolumeId),
	))

	if err != nil {
		klog.InfoS("error running libvirt-storage-attach", "operation", "delete", "stdout", stdout, "stderr", stderr, "err", err.Error(), "pv-id", request.VolumeId)
	}

	// I0325 21:29:29.098228  774510 commands.go:213] "command output" stdout="" stderr="  Failed to find logical volume \"fedora_localhost-live/pv-a61a74d2-ab75-458b-bf1b-0216923ca686\"" err="exit status 5"
	if strings.HasPrefix(strings.TrimSpace(stderr), "Failed to find logical volume") {
		klog.Errorf("volume %s not found", request.VolumeId)
		return response, errors.New("volume not found")
	}

	// TODO are there any other special errors like "volume still attached" ?

	return response, err
}

func (s *LibvirtCsiController) ValidateVolumeCapabilities(ctx context.Context, request *csi.ValidateVolumeCapabilitiesRequest) (*csi.ValidateVolumeCapabilitiesResponse, error) {
	response := &csi.ValidateVolumeCapabilitiesResponse{
		Confirmed: nil,
		Message:   "",
	}

	responseCapabilities := make([]*csi.VolumeCapability, 0)
	for _, capability := range request.VolumeCapabilities {
		// Not sure if this is the right way to switch on capability...
		switch capability.GetAccessMode().GetMode() {
		case csi.VolumeCapability_AccessMode_SINGLE_NODE_WRITER:
			responseCapabilities = append(responseCapabilities, &csi.VolumeCapability{
				AccessMode: &csi.VolumeCapability_AccessMode{
					Mode: csi.VolumeCapability_AccessMode_SINGLE_NODE_WRITER,
				},
				AccessType: &csi.VolumeCapability_Mount{
					// All fields are optional
					Mount: &csi.VolumeCapability_MountVolume{},
				},
			})
		default:
		}
	}

	response.Confirmed = &csi.ValidateVolumeCapabilitiesResponse_Confirmed{
		VolumeCapabilities: responseCapabilities,
		VolumeContext:      request.VolumeContext,
		Parameters:         nil,
	}

	return response, nil
}

func (s *LibvirtCsiController) ControllerGetCapabilities(ctx context.Context, request *csi.ControllerGetCapabilitiesRequest) (*csi.ControllerGetCapabilitiesResponse, error) {
	response := &csi.ControllerGetCapabilitiesResponse{
		Capabilities: []*csi.ControllerServiceCapability{
			{
				Type: &csi.ControllerServiceCapability_Rpc{
					Rpc: &csi.ControllerServiceCapability_RPC{
						Type: csi.ControllerServiceCapability_RPC_LIST_VOLUMES,
					},
				},
			},
			{
				Type: &csi.ControllerServiceCapability_Rpc{
					Rpc: &csi.ControllerServiceCapability_RPC{
						Type: csi.ControllerServiceCapability_RPC_LIST_VOLUMES_PUBLISHED_NODES,
					},
				},
			},
			{
				Type: &csi.ControllerServiceCapability_Rpc{
					Rpc: &csi.ControllerServiceCapability_RPC{
						Type: csi.ControllerServiceCapability_RPC_CREATE_DELETE_VOLUME,
					},
				},
			},
			{
				Type: &csi.ControllerServiceCapability_Rpc{
					Rpc: &csi.ControllerServiceCapability_RPC{
						Type: csi.ControllerServiceCapability_RPC_PUBLISH_UNPUBLISH_VOLUME,
					},
				},
			},
		},
	}
	return response, nil
}

func (s *LibvirtCsiController) ControllerPublishVolume(ctx context.Context, request *csi.ControllerPublishVolumeRequest) (*csi.ControllerPublishVolumeResponse, error) {
	logRequest("publish volume", request)

	stdout, stderr, err := s.CommandRunner.RunCommand(fmt.Sprintf(
		"sudo libvirt-storage-attach -operation=attach -pv-id=%s -vm-name=%s",
		shellescape.Quote(request.VolumeId),
		shellescape.Quote(request.NodeId),
	))

	if err != nil {
		klog.InfoS("error running libvirt-storage-attach", "operation", "attach", "stdout", stdout, "stderr", stderr, "err", err.Error(), "pv-id", request.VolumeId)
	}

	return &csi.ControllerPublishVolumeResponse{
		PublishContext: map[string]string{},
	}, err
}

func (s *LibvirtCsiController) ControllerUnpublishVolume(ctx context.Context, request *csi.ControllerUnpublishVolumeRequest) (*csi.ControllerUnpublishVolumeResponse, error) {
	logRequest("unpublish volume", request)

	stdout, stderr, err := s.CommandRunner.RunCommand(fmt.Sprintf(
		"sudo libvirt-storage-attach -operation=detach -pv-id=%s -vm-name=%s",
		shellescape.Quote(request.VolumeId),
		shellescape.Quote(request.NodeId),
	))

	if err != nil {
		klog.InfoS("error running libvirt-storage-attach", "operation", "detach", "stdout", stdout, "stderr", stderr, "err", err.Error(), "pv-id", request.VolumeId)
	}

	return &csi.ControllerUnpublishVolumeResponse{}, err
}

func (s *LibvirtCsiController) GetCapacity(ctx context.Context, request *csi.GetCapacityRequest) (*csi.GetCapacityResponse, error) {
	// TODO v2
	return nil, status.Error(codes.Unimplemented, "")
}

func (s *LibvirtCsiController) ControllerExpandVolume(ctx context.Context, request *csi.ControllerExpandVolumeRequest) (*csi.ControllerExpandVolumeResponse, error) {
	// TODO v2
	return nil, status.Error(codes.Unimplemented, "")
}

func (s *LibvirtCsiController) ControllerGetVolume(ctx context.Context, request *csi.ControllerGetVolumeRequest) (*csi.ControllerGetVolumeResponse, error) {
	// TODO v2
	return nil, status.Error(codes.Unimplemented, "")
}

func (s *LibvirtCsiController) ListSnapshots(ctx context.Context, request *csi.ListSnapshotsRequest) (*csi.ListSnapshotsResponse, error) {
	// TODO v3
	return nil, status.Error(codes.Unimplemented, "")
}

func (s *LibvirtCsiController) CreateSnapshot(ctx context.Context, request *csi.CreateSnapshotRequest) (*csi.CreateSnapshotResponse, error) {
	// TODO v3
	return nil, status.Error(codes.Unimplemented, "")
}

func (s *LibvirtCsiController) DeleteSnapshot(ctx context.Context, request *csi.DeleteSnapshotRequest) (*csi.DeleteSnapshotResponse, error) {
	// TODO v3
	return nil, status.Error(codes.Unimplemented, "")
}
