package main

import (
	"flag"
	"github.com/container-storage-interface/spec/lib/go/csi"
	"github.com/nijave/libvirt-csi/pkg"
	"google.golang.org/grpc"
	"k8s.io/klog/v2"
	"net"
	"os"
)

func initController(grpcServer *grpc.Server) {

	csiController := &pkg.LibvirtCsiController{
		SshClient: nil,
	}

	csi.RegisterControllerServer(grpcServer, csiController)
	csi.RegisterIdentityServer(grpcServer, csiController)
}

func initDriver(grpcServer *grpc.Server) {
	csiController := &pkg.LibvirtCsiController{}
	csi.RegisterIdentityServer(grpcServer, csiController)
	csiDriver := &pkg.LibvirtCsiDriver{}
	csi.RegisterNodeServer(grpcServer, csiDriver)
}

func main() {
	var grpcService string
	klog.InitFlags(nil)
	flag.StringVar(&grpcService, "grpc-service", "controller", "Which gRPC services should run")
	flag.Parse()

	socket := "/run/csi/socket"
	if envSocket := os.Getenv("CSI_ADDRESS"); len(envSocket) > 0 {
		socket = envSocket
		klog.InfoS("using non-default socket", "socket", socket)
	}

	err := os.Remove(socket)
	if err != nil {
		klog.Infof("error removing existing socket %v", err)
	}

	listen, err := net.Listen("unix", socket)
	if err != nil {
		klog.Fatalf("failed to listen: %v", err)
	}
	defer listen.Close()
	grpcServer := grpc.NewServer()

	switch grpcService {
	case "controller":
		initController(grpcServer)
	case "driver":
		initDriver(grpcServer)
	default:
		listen.Close()
		klog.Fatal("invalid grpc-service specified")
	}

	klog.Infof("server %s listening at %v", grpcService, listen.Addr())
	if err := grpcServer.Serve(listen); err != nil {
		klog.Fatalf("failed to serve: %v", err)
	}
}
