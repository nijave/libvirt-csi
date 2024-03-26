package pkg

import (
	"context"
	"errors"
	"github.com/container-storage-interface/spec/lib/go/csi"
	"github.com/stretchr/testify/assert"
	"io"
	"testing"
)

type mockWinRmClient struct {
	ReturnCode int
	Error      error
	Stderr     string
	Stdout     string
}

func (m mockWinRmClient) RunWithContext(ctx context.Context, command string, stdout, stderr io.Writer) (int, error) {
	if len(m.Stdout) > 0 {
		stdout.Write([]byte(m.Stdout))
	}
	if len(m.Stderr) > 0 {
		stderr.Write([]byte(m.Stderr))
	}
	return m.ReturnCode, m.Error
}

func newController() (*mockWinRmClient, *LibvirtCsiController) {
	mockWinRm := &mockWinRmClient{
		ReturnCode: 0,
		Error:      nil,
	}
	return mockWinRm, &LibvirtCsiController{
		IdentityServer:   nil,
		ControllerServer: nil,
	}
}

func Test_ListVolumesPowershellGenericError(t *testing.T) {
	mockWinRm, controller := newController()
	mockWinRm.ReturnCode = 1

	_, err := controller.ListVolumes(context.Background(), &csi.ListVolumesRequest{
		MaxEntries:    0,
		StartingToken: "",
	})

	assert.Equal(t, "powershell error", err.Error())
}

func Test_ListVolumesPowershellErrorMessage(t *testing.T) {
	mockWinRm, controller := newController()
	errorMsg := "a thing failed"
	mockWinRm.Error = errors.New(errorMsg)

	_, err := controller.ListVolumes(context.Background(), &csi.ListVolumesRequest{
		MaxEntries:    0,
		StartingToken: "",
	})

	assert.Equal(t, errorMsg, err.Error())
}

func Test_ListVolumesValidOutput(t *testing.T) {
	mockWinRm, controller := newController()
	volumeIds := []string{"eab72431-5d15-4152-a8d1-5cf4ea41627e", "eae2dc8f-a05f-4798-a2e7-2f4fc94353cf"}
	for _, vol := range volumeIds {
		mockWinRm.Stdout = mockWinRm.Stdout + "pv-" + vol + ".vhdx\r"
	}

	response, err := controller.ListVolumes(context.Background(), &csi.ListVolumesRequest{
		MaxEntries:    0,
		StartingToken: "",
	})

	assert.Nil(t, err)
	for i, vol := range volumeIds {
		assert.Equal(t, vol, response.Entries[i].Volume.VolumeId)
	}
}
