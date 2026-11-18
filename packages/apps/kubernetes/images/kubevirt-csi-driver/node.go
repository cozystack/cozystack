package main

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
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
	// Target paths with an NFS publish running. kubelet retries a publish
	// that outlives its 2m deadline while the first call still runs, and on
	// a dead server every retry would block the same way: the soft temp mount
	// gives up only after minutes, and the stat of a target that is already
	// hard-mounted does not give up at all.
	publishing sync.Map
	// Replaced in tests, which cannot get the soft temp mount's EIO locally;
	// nil means the os function.
	lstat func(string) (os.FileInfo, error)
	mkdir func(string, os.FileMode) error
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

	lstat, mkdir := w.lstat, w.mkdir
	if lstat == nil {
		lstat = os.Lstat
	}
	if mkdir == nil {
		mkdir = os.Mkdir
	}

	targetPath := req.GetTargetPath()
	if _, busy := w.publishing.LoadOrStore(targetPath, struct{}{}); busy {
		return nil, status.Errorf(codes.Aborted, "publish of NFS volume %s at %s already in progress", req.GetVolumeId(), targetPath)
	}
	defer w.publishing.Delete(targetPath)

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
		return nil, status.Errorf(codes.Internal, "failed to create temp mount dir for volume %s: %v", req.GetVolumeId(), err)
	}

	rootSource := fmt.Sprintf("%s:%s", host, path)
	// nosharecache gives the temp mount a superblock of its own: the forced
	// unmount below kills every RPC in flight on its superblock, which would
	// otherwise be the one the pods on this node use for the same export.
	// The price is a cache of its own too: pods already holding /data may not
	// see the entries the migration moves in until their attribute cache
	// expires (acdirmax).
	//
	// soft lets an RPC fail with EIO instead of blocking forever. That is
	// safe here because this mount only does metadata work that a retried
	// publish redoes, and it is why the volume mount below stays hard. Over
	// NFSv4 a soft RPC fails only once the TCP connection is declared dead,
	// which takes minutes with the default timeo and retrans. Those are not
	// lowered: the first mount of a server sets them for the connection that
	// every later mount of it on this node shares, the pods' hard mounts
	// included. retry=0 stops mount.nfs from retrying a failed mount for 2m.
	rootOpts := []string{"nfsvers=4.2", fmt.Sprintf("port=%s", port), "nosharecache", "soft", "retry=0"}
	if err := w.mounter.Mount(rootSource, tmpMount, "nfs", rootOpts); err != nil {
		if rmErr := os.Remove(tmpMount); rmErr != nil {
			klog.Warningf("Failed to remove temp dir %s: %v", tmpMount, rmErr)
		}
		return nil, status.Errorf(codes.Internal, "NFS temp mount for volume %s failed: %v", req.GetVolumeId(), err)
	}
	defer func() {
		if err := unmountTempMount(w.mounter, tmpMount); err != nil {
			klog.Warningf("Failed to unmount temp dir %s: %v", tmpMount, err)
		}
	}()

	dataDir := filepath.Join(tmpMount, "data")
	// Not MkdirAll: with the server gone its stat fails on the mount point,
	// and it then reports the local temp dir as existing instead of the EIO.
	if err := mkdir(dataDir, 0777); err != nil && !errors.Is(err, fs.ErrExist) {
		return nil, status.Errorf(codes.Internal, "failed to create /data subdir for volume %s: %v", req.GetVolumeId(), err)
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
		// Lstat: a user's symlink in /data counts as present whatever it
		// points at, and is not replaced.
		if _, err := lstat(dst); err == nil {
			klog.Warningf("Not migrating %s for volume %s: /data/%s already exists, so %s stays in the export root", name, req.GetVolumeId(), name, name)
			continue
		} else if !os.IsNotExist(err) {
			// The rename would replace whatever the failed stat could not see.
			return nil, status.Errorf(codes.Internal, "failed to check /data/%s for volume %s: %v", name, req.GetVolumeId(), err)
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
	// Well inside the 2m kubelet puts on NodePublishVolume (csiTimeout in
	// kubernetes pkg/volume/csi/csi_plugin.go).
	tempUnmountTimeout = 30 * time.Second
	// How long umount's output pipe may stay open once umount has exited or
	// been killed; the umount.nfs helper it forked can hold it.
	umountPipeGrace = time.Second
)

// unmountTempMount bounds the unmount: a plain umount of an NFS mount whose
// server is gone blocks forever, which hangs the publish and leaks the mount.
func unmountTempMount(m mount.Interface, dir string) error {
	return unmountWithin(m, dir, tempUnmountTimeout)
}

// unmountWithin gives the whole unmount one deadline. On timeout the unmount
// keeps running in the background and logs its result when it ends; an umount
// stuck in uninterruptible sleep cannot be killed, so no deadline on the
// process itself can be relied on.
//
// The dir is removed by the worker once the unmount succeeds, so an unmount
// that finishes after the deadline still cleans up; removed by the caller it
// would still be a mount point at that moment. A failed unmount keeps the dir.
// Plain Remove rather than RemoveAll: if the unmount did not take, the dir's
// contents are the export's.
func unmountWithin(m mount.Interface, dir string, budget time.Duration) error {
	done := make(chan error)
	abandoned := make(chan struct{})
	go func() {
		var err error
		if _, ok := m.(*mount.Mounter); ok {
			err = umountForcedAfter(dir, budget/3)
		} else {
			err = m.Unmount(dir)
		}
		if err == nil {
			if rmErr := os.Remove(dir); rmErr != nil && !os.IsNotExist(rmErr) {
				klog.Warningf("Failed to remove temp dir %s: %v", dir, rmErr)
			}
		}
		select {
		case done <- err:
		case <-abandoned:
			if err != nil {
				klog.Warningf("unmount of %s failed after the deadline: %v", dir, err)
			} else {
				klog.Infof("unmount of %s finished after the deadline", dir)
			}
		}
	}()
	select {
	case err := <-done:
		return err
	case <-time.After(budget):
		close(abandoned)
		return fmt.Errorf("unmount of %s still running after %v", dir, budget)
	}
}

// umountForcedAfter replaces mount-utils' UnmountWithForce, which retries with
// -f only once its first umount has returned. That umount forks umount.nfs;
// killed on the deadline, it leaves the helper stuck on the vanished server
// holding the output pipe, and CombinedOutput waits for that pipe, so the -f
// never runs. WaitDelay closes the pipe instead.
func umountForcedAfter(dir string, timeout time.Duration) error {
	err := runUmount(timeout, dir)
	if !errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	klog.V(2).Infof("Timed out waiting for unmount of %s, trying with -f", dir)
	return runUmount(timeout, "-f", dir)
}

func runUmount(timeout time.Duration, args ...string) error {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "umount", args...)
	cmd.WaitDelay = umountPipeGrace
	out, err := cmd.CombinedOutput()
	if ctx.Err() != nil {
		return fmt.Errorf("umount %s: %w", strings.Join(args, " "), ctx.Err())
	}
	// umount2 detaches the mount before it blocks tearing the superblock down,
	// so the -f after a killed umount can find nothing left to unmount.
	if err != nil && !strings.Contains(string(out), "not mounted") {
		return fmt.Errorf("umount %s: %v: %s", strings.Join(args, " "), err, out)
	}
	return nil
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
