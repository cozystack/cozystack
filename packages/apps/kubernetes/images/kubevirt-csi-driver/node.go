package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"time"

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

// NodePublishVolume for NFS volumes: mounts the /data subdir of the NFS export
// at the target path, hiding internal artifacts (disk.img, lost+found).
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

	mountOptions := []string{
		"nfsvers=4.2",
		fmt.Sprintf("port=%s", port),
	}
	if req.GetReadonly() {
		mountOptions = append(mountOptions, "ro")
	}

	// Temp-mount the NFS root to ensure /data subdir exists and migrate any
	// user files that were written before this fix (backward compatibility).
	tmpMount, err := os.MkdirTemp("", fmt.Sprintf("nfs-init-%s-", req.GetVolumeId()))
	if err != nil {
		return nil, status.Errorf(codes.Internal, "failed to create temp mount dir: %v", err)
	}

	rootSource := fmt.Sprintf("%s:%s", host, path)
	// nosharecache gives the temp mount a superblock of its own: the forced
	// unmount below kills every RPC in flight on its superblock, which would
	// otherwise be the one the pods on this node use for the same export.
	rootOpts := []string{"nfsvers=4.2", fmt.Sprintf("port=%s", port), "nosharecache"}
	if err := w.mounter.Mount(rootSource, tmpMount, "nfs", rootOpts); err != nil {
		_ = os.Remove(tmpMount)
		return nil, status.Errorf(codes.Internal, "NFS temp mount failed: %v", err)
	}
	defer func() {
		if err := unmountTempMount(w.mounter, tmpMount); err != nil {
			klog.Warningf("Failed to unmount temp dir %s: %v", tmpMount, err)
		}
	}()

	dataDir := filepath.Join(tmpMount, "data")
	if err := os.MkdirAll(dataDir, 0777); err != nil {
		return nil, status.Errorf(codes.Internal, "failed to create /data subdir: %v", err)
	}

	// Auto-migrate: move user files from root into /data (skip internal artifacts).
	// Fail the publish if migration cannot complete to avoid hiding user data.
	entries, err := os.ReadDir(tmpMount)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "failed to read NFS root for migration (volume %s): %v", req.GetVolumeId(), err)
	}
	for _, entry := range entries {
		name := entry.Name()
		if name == "data" || name == "disk.img" || name == "lost+found" {
			continue
		}
		src := filepath.Join(tmpMount, name)
		dst := filepath.Join(dataDir, name)
		if _, err := os.Stat(dst); err == nil {
			continue // already exists in /data, skip
		}
		klog.Infof("Migrating %s to /data/%s for volume %s", name, name, req.GetVolumeId())
		if err := os.Rename(src, dst); err != nil {
			if os.IsNotExist(err) {
				continue // benign: concurrent publish already moved it
			}
			return nil, status.Errorf(codes.Internal, "failed to migrate %s for volume %s: %v", name, req.GetVolumeId(), err)
		}
	}

	// Mount only the /data subdir at the pod's target path.
	dataSource := fmt.Sprintf("%s:%s/data", host, path)
	klog.V(3).Infof("Mounting NFS %s at %s with options %v", dataSource, targetPath, mountOptions)
	if err := w.mounter.Mount(dataSource, targetPath, "nfs", mountOptions); err != nil {
		return nil, status.Errorf(codes.Internal, "NFS mount of %s at %s failed: %v", dataSource, targetPath, err)
	}

	return &csi.NodePublishVolumeResponse{}, nil
}

const (
	// csiTimeout in kubernetes pkg/volume/csi/csi_plugin.go: the deadline
	// kubelet puts on NodePublishVolume.
	kubeletCSIOperationTimeout = 2 * time.Minute
	// Leaves room inside kubeletCSIOperationTimeout for the mounts and the
	// migration that precede the unmount.
	tempUnmountTimeout = 30 * time.Second
)

// unmountTempMount bounds the unmount: a plain umount of an NFS mount whose
// server is gone blocks forever, which hangs the publish and leaks the mount.
func unmountTempMount(m mount.Interface, dir string) error {
	return unmountWithin(m, dir, tempUnmountTimeout)
}

// unmountWithin gives the whole unmount one deadline. UnmountWithForce bounds
// only its first umount, and only while that process can be killed; the -f
// retry it falls back to runs with no deadline at all (mount-utils v0.36.0,
// forceUmount). On timeout the umount keeps running in the background.
//
// The dir is removed by the worker once the unmount succeeds, so an unmount
// that finishes after the deadline still cleans up; removed by the caller it
// would still be a mount point at that moment. A failed unmount keeps the dir.
// Plain Remove rather than RemoveAll: if the unmount did not take, the dir's
// contents are the export's.
func unmountWithin(m mount.Interface, dir string, budget time.Duration) error {
	done := make(chan error, 1)
	go func() {
		var err error
		if fm, ok := m.(mount.MounterForceUnmounter); ok {
			err = fm.UnmountWithForce(dir, budget/2)
		} else {
			err = m.Unmount(dir)
		}
		if err == nil {
			if rmErr := os.Remove(dir); rmErr != nil && !os.IsNotExist(rmErr) {
				klog.Warningf("Failed to remove temp dir %s: %v", dir, rmErr)
			}
		}
		done <- err
	}()
	select {
	case err := <-done:
		return err
	case <-time.After(budget):
		return fmt.Errorf("unmount of %s still running after %v", dir, budget)
	}
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
		klog.Warningf("Failed to list mount points: %v", err)
		return false
	}
	for _, mp := range mountPoints {
		if mp.Path == path && (mp.Type == "nfs" || mp.Type == "nfs4") {
			return true
		}
	}
	return false
}
