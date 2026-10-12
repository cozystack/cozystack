package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	csi "github.com/container-storage-interface/spec/lib/go/csi"
	"k8s.io/klog/v2"
	mount "k8s.io/mount-utils"
)

func TestIsNFSMount(t *testing.T) {
	m := mount.NewFakeMounter([]mount.MountPoint{
		{Path: "/pods/nfs3", Type: "nfs"},
		{Path: "/pods/nfs4", Type: "nfs4"},
		{Path: "/pods/block", Type: "ext4"},
	})

	cases := map[string]bool{
		"/pods/nfs3":    true,
		"/pods/nfs4":    true,
		"/pods/block":   false,
		"/pods/missing": false,
	}
	for path, want := range cases {
		if got := isNFSMount(path, m); got != want {
			t.Errorf("isNFSMount(%q) = %v, want %v", path, got, want)
		}
	}
}

// The fake mounter mounts nothing, so the /data subdir the publish creates
// lands in the temp dir itself and its removal fails with ENOTEMPTY.
func TestNodePublishVolumeNFSLogsTempDirRemovalFailure(t *testing.T) {
	tmpRoot := t.TempDir()
	t.Setenv("TMPDIR", tmpRoot)

	var logs bytes.Buffer
	klog.LogToStderr(false)
	klog.SetOutput(&logs)
	t.Cleanup(func() {
		klog.SetOutput(os.Stderr)
		klog.LogToStderr(true)
	})

	w := &WrappedNodeService{mounter: mount.NewFakeMounter(nil)}
	req := &csi.NodePublishVolumeRequest{
		VolumeId:       "pvc-1",
		TargetPath:     filepath.Join(t.TempDir(), "target"),
		PublishContext: map[string]string{nfsExportKey: "nfs://192.0.2.10:2049/export"},
	}
	if _, err := w.NodePublishVolume(context.Background(), req); err != nil {
		t.Fatalf("NodePublishVolume: %v", err)
	}
	klog.Flush()

	leftovers, err := filepath.Glob(filepath.Join(tmpRoot, "nfs-init-pvc-1-*"))
	if err != nil || len(leftovers) != 1 {
		t.Fatalf("expected one leftover temp dir, got %v (err %v)", leftovers, err)
	}
	for _, line := range strings.Split(logs.String(), "\n") {
		if strings.HasPrefix(line, "W") && strings.Contains(line, leftovers[0]) {
			return
		}
	}
	t.Errorf("removal failure of %s was not logged as a warning; log output:\n%s", leftovers[0], logs.String())
}
