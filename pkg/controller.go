package pkg

import (
	"context"
	"errors"
	"github.com/container-storage-interface/spec/lib/go/csi"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/wrapperspb"
	"io"
	"k8s.io/klog/v2"
	"strings"
)

type remoteSshRunner interface {
	RunWithContext(context.Context, string, io.Writer, io.Writer) (int, error)
}

type LibvirtCsiController struct {
	csi.IdentityServer
	csi.ControllerServer
	SshClient remoteSshRunner
}

const driverName = "libvirt-csi.nijave.github.com"
const driverVersion = "1.0.0"
const defaultCapacity = 20 // GB

type ExecResult struct {
	ExitCode int
	Output   string
	Error    error
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

	// TODO libvirt-attach-storage -operation=list ?
	// listCommand := fmt.Sprintf("Get-Item %s | Select Name", s.makeVolumePath(volumeFilePrefix+"*", true))
	result := "a\rb\r"

	volumeFiles := strings.Split(result, "\r")
	volumeList := make([]*csi.ListVolumesResponse_Entry, len(volumeFiles))
	for i, volumeFile := range volumeFiles {
		volumeList[i] = &csi.ListVolumesResponse_Entry{
			Volume: &csi.Volume{
				VolumeId:           volumeFile, // TODO
				CapacityBytes:      0,
				VolumeContext:      nil,
				ContentSource:      nil,
				AccessibleTopology: nil,
			},
			Status: &csi.ListVolumesResponse_VolumeStatus{
				PublishedNodeIds: nil, // OPTIONAL
				VolumeCondition:  nil, // OPTIONAL
			},
		}
	}

	return &csi.ListVolumesResponse{
		Entries:   volumeList,
		NextToken: "", // TODO pagination
	}, nil
}

func (s *LibvirtCsiController) CreateVolume(ctx context.Context, request *csi.CreateVolumeRequest) (*csi.CreateVolumeResponse, error) {
	logRequest("creating volume", request)

	response := &csi.CreateVolumeResponse{
		Volume: &csi.Volume{
			VolumeId:           "",
			CapacityBytes:      0,
			VolumeContext:      nil,
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

	// TODO ssh host sudo libvirt-attach-storage -operation=create -size=capacity
	//result := ""

	response.Volume.VolumeId = "some uuid"
	return response, nil
}

func (s *LibvirtCsiController) DeleteVolume(ctx context.Context, request *csi.DeleteVolumeRequest) (*csi.DeleteVolumeResponse, error) {
	logRequest("deleting volume", request)
	response := &csi.DeleteVolumeResponse{}

	// TODO create libvirt-storage-attach -operation=delete
	result := ""

	if strings.Contains(result, "failed to delete attached volume") {
		klog.Errorf("volume %s not found in volume list", request.VolumeId)
		return response, errors.New("powershell error")
	}

	return response, nil
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
	// TODO ssh host sudo libvirt-storage-attach -operation=attach -vm-name=... -pv-id=...

	return &csi.ControllerPublishVolumeResponse{
		PublishContext: map[string]string{},
	}, nil
}

func (s *LibvirtCsiController) ControllerUnpublishVolume(ctx context.Context, request *csi.ControllerUnpublishVolumeRequest) (*csi.ControllerUnpublishVolumeResponse, error) {
	// TODO ssh host sudo libvirt-storage-attach -operation=detach -vm-name=... -pv-id=...

	return &csi.ControllerUnpublishVolumeResponse{}, nil
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
