package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	csi "github.com/container-storage-interface/spec/lib/go/csi"
	mount "k8s.io/mount-utils"
)

// A path that does not exist, so the fake mounter's symlink resolution keeps
// it as is and matches it against its mount table.
const tempMountPath = "/nonexistent/nfs-init-pvc-1-123"

// forceFakeMounter adds mount.MounterForceUnmounter to the fake, as the real
// Linux mounter implements it.
type forceFakeMounter struct {
	*mount.FakeMounter
	forceErr      error
	forcedTargets []string
	forcedTimeout time.Duration
}

func (f *forceFakeMounter) UnmountWithForce(target string, timeout time.Duration) error {
	f.forcedTargets = append(f.forcedTargets, target)
	f.forcedTimeout = timeout
	return f.forceErr
}

func mountedFake(unmountErr error) *mount.FakeMounter {
	m := mount.NewFakeMounter([]mount.MountPoint{{Path: tempMountPath, Type: "nfs"}})
	if unmountErr != nil {
		m.UnmountFunc = func(string) error { return unmountErr }
	}
	return m
}

func TestUnmountTempMountUsesForceUnmounter(t *testing.T) {
	m := &forceFakeMounter{FakeMounter: mountedFake(errors.New("plain Unmount called"))}

	if err := unmountTempMount(m, tempMountPath); err != nil {
		t.Fatalf("unmountTempMount: %v", err)
	}
	if len(m.forcedTargets) != 1 || m.forcedTargets[0] != tempMountPath {
		t.Fatalf("UnmountWithForce targets = %v, want [%s]", m.forcedTargets, tempMountPath)
	}
	if m.forcedTimeout <= 0 || m.forcedTimeout >= kubeletCSIOperationTimeout {
		t.Errorf("UnmountWithForce timeout = %v, want within (0, %v)", m.forcedTimeout, kubeletCSIOperationTimeout)
	}
}

// main wires mount.New(""); without this the force path above is never taken
// in production and the fallback is silent.
func TestLinuxMounterIsForceUnmounter(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("mount.New returns the unsupported mounter off Linux")
	}
	if _, ok := mount.New("").(mount.MounterForceUnmounter); !ok {
		t.Fatal("mount.New(\"\") does not implement mount.MounterForceUnmounter")
	}
}

// blockingForceMounter stands for an umount -f that never returns:
// mount-utils runs the forced retry with no deadline of its own.
type blockingForceMounter struct {
	*mount.FakeMounter
	release chan struct{}
}

func (b *blockingForceMounter) UnmountWithForce(string, time.Duration) error {
	<-b.release
	return nil
}

func TestUnmountTempMountGivesUpOnAStalledForceRetry(t *testing.T) {
	m := &blockingForceMounter{FakeMounter: mountedFake(nil), release: make(chan struct{})}
	defer close(m.release)

	done := make(chan error, 1)
	go func() { done <- unmountWithin(m, tempMountPath, 50*time.Millisecond) }()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("unmountWithin reported success for an unmount still running")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("unmountWithin did not return within its budget while the forced retry stalled")
	}
}

func TestUnmountTempMountReportsForceFailure(t *testing.T) {
	m := &forceFakeMounter{FakeMounter: mountedFake(nil), forceErr: errors.New("device busy")}

	if err := unmountTempMount(m, tempMountPath); err == nil {
		t.Fatal("unmountTempMount succeeded although UnmountWithForce failed")
	}
}

func TestUnmountTempMountFallsBackToPlainUnmount(t *testing.T) {
	m := mountedFake(nil)
	if err := unmountTempMount(m, tempMountPath); err != nil {
		t.Fatalf("unmountTempMount: %v", err)
	}
	if mps, _ := m.List(); len(mps) != 0 {
		t.Errorf("mount points after unmount = %v, want none", mps)
	}

	if err := unmountTempMount(mountedFake(errors.New("device busy")), tempMountPath); err == nil {
		t.Error("unmountTempMount succeeded although Unmount failed")
	}
}

func TestNodePublishVolumeNFSUnmountsTempMountWithForce(t *testing.T) {
	tmpRoot := t.TempDir()
	t.Setenv("TMPDIR", tmpRoot)

	m := &forceFakeMounter{FakeMounter: mount.NewFakeMounter(nil)}
	w := &WrappedNodeService{mounter: m}
	req := &csi.NodePublishVolumeRequest{
		VolumeId:       "pvc-1",
		TargetPath:     filepath.Join(t.TempDir(), "target"),
		PublishContext: map[string]string{nfsExportKey: "nfs://192.0.2.10:2049/export"},
	}
	if _, err := w.NodePublishVolume(context.Background(), req); err != nil {
		t.Fatalf("NodePublishVolume: %v", err)
	}

	if len(m.forcedTargets) != 1 || !strings.HasPrefix(filepath.Base(m.forcedTargets[0]), "nfs-init-pvc-1-") {
		t.Fatalf("UnmountWithForce targets = %v, want the one nfs-init-pvc-1-* temp mount", m.forcedTargets)
	}

	// umount -f runs nfs_umount_begin on the superblock, killing every RPC in
	// flight on it. Without nosharecache the temp mount shares that superblock
	// with the pods' mounts of the same export on this node.
	var tempOpts []string
	for _, mp := range m.MountPoints {
		if strings.HasPrefix(filepath.Base(mp.Path), "nfs-init-pvc-1-") {
			tempOpts = mp.Opts
		}
	}
	if !slices.Contains(tempOpts, "nosharecache") {
		t.Errorf("temp mount options = %v, want nosharecache so a forced unmount stays on its own superblock", tempOpts)
	}
}

// An unmount that outlives its budget still finishes in the background, and the
// temp dir can only go once it has: removed any earlier it is still a mount
// point and the remove fails, and nothing would try again.
func TestUnmountWithinRemovesTheDirAfterALateUnmount(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "nfs-init-pvc-1-123")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	m := &blockingForceMounter{FakeMounter: mountedFake(nil), release: make(chan struct{})}

	if err := unmountWithin(m, dir, 50*time.Millisecond); err == nil {
		t.Fatal("unmountWithin reported success for an unmount still running")
	}
	if _, err := os.Stat(dir); err != nil {
		t.Fatalf("temp dir gone while its unmount was still running: %v", err)
	}

	close(m.release)
	deadline := time.Now().Add(5 * time.Second)
	for {
		if _, err := os.Stat(dir); os.IsNotExist(err) {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("temp dir still present after the late unmount succeeded")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestUnmountWithinKeepsTheDirWhenUnmountFails(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "nfs-init-pvc-1-123")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	m := &forceFakeMounter{FakeMounter: mountedFake(nil), forceErr: errors.New("device busy")}

	if err := unmountWithin(m, dir, time.Second); err == nil {
		t.Fatal("unmountWithin succeeded although UnmountWithForce failed")
	}
	if _, err := os.Stat(dir); err != nil {
		t.Errorf("temp dir removed although it may still be mounted: %v", err)
	}
}

type failingMounter struct{ *mount.FakeMounter }

func (failingMounter) Mount(string, string, string, []string) error {
	return errors.New("connection refused")
}

// The dir is removed only after a successful unmount, so the path that never
// mounts has to remove it on its own. The mounted path cannot be checked the same
// way: the fake mounts nothing, so /data lands in the temp dir itself and a
// plain remove, deliberately not RemoveAll, leaves it.
func TestNodePublishVolumeNFSRemovesTheTempDirWhenTheMountFails(t *testing.T) {
	tmpRoot := t.TempDir()
	t.Setenv("TMPDIR", tmpRoot)
	w := &WrappedNodeService{mounter: failingMounter{mount.NewFakeMounter(nil)}}
	req := &csi.NodePublishVolumeRequest{
		VolumeId:       "pvc-1",
		TargetPath:     filepath.Join(t.TempDir(), "target"),
		PublishContext: map[string]string{nfsExportKey: "nfs://192.0.2.10:2049/export"},
	}
	if _, err := w.NodePublishVolume(context.Background(), req); err == nil {
		t.Fatal("NodePublishVolume succeeded although the temp mount failed")
	}
	if left, _ := filepath.Glob(filepath.Join(tmpRoot, "nfs-init-*")); len(left) != 0 {
		t.Errorf("temp dirs left behind: %v", left)
	}
}
