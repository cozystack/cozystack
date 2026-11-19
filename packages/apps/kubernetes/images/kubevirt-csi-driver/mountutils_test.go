package main

import (
	"os"
	"path/filepath"
	"testing"

	"k8s.io/mount-utils"
)

// The upstream node service is compiled against the mount-utils this module
// pins, not the one upstream tests with. These pin the parts of it upstream
// relies on when it decides whether to mount and how to clean up.
func TestMountUtilsFakeMounterMountPointDetection(t *testing.T) {
	dir := resolvedTempDir(t)
	mounted := filepath.Join(dir, "mounted")
	plain := filepath.Join(dir, "plain")
	for _, d := range []string{mounted, plain} {
		if err := os.Mkdir(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	m := mount.NewFakeMounter([]mount.MountPoint{{Device: "/dev/vdb", Path: mounted, Type: "ext4"}})

	if notMnt, err := m.IsLikelyNotMountPoint(mounted); err != nil || notMnt {
		t.Errorf("IsLikelyNotMountPoint(mounted) = %v, %v; want false, nil", notMnt, err)
	}
	if notMnt, err := m.IsLikelyNotMountPoint(plain); err != nil || !notMnt {
		t.Errorf("IsLikelyNotMountPoint(plain) = %v, %v; want true, nil", notMnt, err)
	}
	if _, err := m.IsLikelyNotMountPoint(filepath.Join(dir, "missing")); !os.IsNotExist(err) {
		t.Errorf("IsLikelyNotMountPoint(missing) error = %v, want not-exist", err)
	}

	if err := m.Unmount(mounted); err != nil {
		t.Fatalf("Unmount: %v", err)
	}
	if notMnt, err := m.IsLikelyNotMountPoint(mounted); err != nil || !notMnt {
		t.Errorf("after Unmount: IsLikelyNotMountPoint = %v, %v; want true, nil", notMnt, err)
	}
}

// NodeUnpublishVolume upstream calls CleanupMountPoint: it unmounts a mounted
// target and removes the directory, and tolerates a target that is already gone.
func TestMountUtilsCleanupMountPoint(t *testing.T) {
	dir := resolvedTempDir(t)
	target := filepath.Join(dir, "target")
	if err := os.Mkdir(target, 0o755); err != nil {
		t.Fatal(err)
	}
	m := mount.NewFakeMounter([]mount.MountPoint{{Device: "/dev/vdb", Path: target, Type: "ext4"}})

	if err := mount.CleanupMountPoint(target, m, false); err != nil {
		t.Fatalf("CleanupMountPoint: %v", err)
	}
	if _, err := os.Stat(target); !os.IsNotExist(err) {
		t.Errorf("target still present after cleanup: %v", err)
	}
	if mps, _ := m.List(); len(mps) != 0 {
		t.Errorf("mount points after cleanup = %v, want none", mps)
	}
	if err := mount.CleanupMountPoint(target, m, false); err != nil {
		t.Errorf("CleanupMountPoint on a missing target: %v", err)
	}
}

// FakeMounter resolves symlinks before matching a path, and the temp dir is
// behind one on some systems.
func resolvedTempDir(t *testing.T) string {
	t.Helper()
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return dir
}
