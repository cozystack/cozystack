package main

import (
	"context"
	"fmt"
	"os"

	csi "github.com/container-storage-interface/spec/lib/go/csi"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"k8s.io/klog/v2"
	mount "k8s.io/mount-utils"

	"kubevirt.io/csi-driver/pkg/service"
)

var _ csi.NodeServer = &WrappedNodeService{}

// WrappedNodeService embeds the upstream NodeService and adds NFS mount support.
type WrappedNodeService struct {
	*service.NodeService
	mounter mount.Interface
}

// NodeStageVolume for NFS volumes is a no-op (NFS doesn't need staging).
// For RWO volumes, delegates to upstream (lsblk + mkfs).
func (w *WrappedNodeService) NodeStageVolume(ctx context.Context, req *csi.NodeStageVolumeRequest) (*csi.NodeStageVolumeResponse, error) {
	if req.GetPublishContext()[nfsExportKey] != "" {
		klog.V(3).Infof("NFS volume %s: skipping stage", req.GetVolumeId())
		return &csi.NodeStageVolumeResponse{}, nil
	}
	return w.NodeService.NodeStageVolume(ctx, req)
}

// NodePublishVolume for NFS volumes: mounts NFS at the target path.
// For RWO volumes, delegates to upstream (mount block device).
func (w *WrappedNodeService) NodePublishVolume(ctx context.Context, req *csi.NodePublishVolumeRequest) (*csi.NodePublishVolumeResponse, error) {
	nfsExport := req.GetPublishContext()[nfsExportKey]
	if nfsExport == "" {
		return w.NodeService.NodePublishVolume(ctx, req)
	}

	klog.V(3).Infof("Publishing NFS volume %s at %s", req.GetVolumeId(), req.GetTargetPath())

	host, port, path, err := parseNFSExport(nfsExport)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "failed to parse NFS export: %v", err)
	}

	targetPath := req.GetTargetPath()

	// Check if already mounted
	notMnt, err := w.mounter.IsLikelyNotMountPoint(targetPath)
	if err != nil {
		if !os.IsNotExist(err) {
			return nil, status.Errorf(codes.Internal, "failed to check mount point %s: %v", targetPath, err)
		}
		if err := os.MkdirAll(targetPath, 0750); err != nil {
			return nil, status.Errorf(codes.Internal, "failed to create target path %s: %v", targetPath, err)
		}
		notMnt = true
	}

	if !notMnt {
		klog.V(3).Infof("NFS volume %s already mounted at %s", req.GetVolumeId(), targetPath)
		return &csi.NodePublishVolumeResponse{}, nil
	}

	source := fmt.Sprintf("%s:%s", host, path)
	mountOptions := []string{
		"nfsvers=4.2",
		fmt.Sprintf("port=%s", port),
	}

	klog.V(3).Infof("Mounting NFS %s at %s with options %v", source, targetPath, mountOptions)
	if err := w.mounter.Mount(source, targetPath, "nfs", mountOptions); err != nil {
		return nil, status.Errorf(codes.Internal, "NFS mount of %s at %s failed: %v", source, targetPath, err)
	}

	return &csi.NodePublishVolumeResponse{}, nil
}

// NodeExpandVolume for NFS volumes is a no-op (LINSTOR handles NFS resize automatically).
// This should not normally be called for NFS since ControllerExpandVolume returns
// NodeExpansionRequired=false, but we handle it gracefully as a safety net.
func (w *WrappedNodeService) NodeExpandVolume(ctx context.Context, req *csi.NodeExpandVolumeRequest) (*csi.NodeExpandVolumeResponse, error) {
	if isNFSMount(req.GetVolumePath(), w.mounter) {
		klog.V(3).Infof("NFS volume %s: skipping node expansion", req.GetVolumeId())
		return &csi.NodeExpandVolumeResponse{}, nil
	}
	return w.NodeService.NodeExpandVolume(ctx, req)
}

// isNFSMount checks if the given path is an NFS mount point.
func isNFSMount(path string, m mount.Interface) bool {
	mountPoints, err := m.List()
	if err != nil {
		return false
	}
	for _, mp := range mountPoints {
		if mp.Path == path && (mp.Type == "nfs" || mp.Type == "nfs4") {
			return true
		}
	}
	return false
}
