package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"

	csi "github.com/container-storage-interface/spec/lib/go/csi"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
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

func nfsPublishRequest(target string) *csi.NodePublishVolumeRequest {
	return &csi.NodePublishVolumeRequest{
		VolumeId:       "pvc-1",
		TargetPath:     target,
		PublishContext: map[string]string{nfsExportKey: "nfs://192.0.2.10:2049/export"},
	}
}

func hasRetryTuning(opts []string) bool {
	for _, o := range opts {
		if o == "soft" || strings.HasPrefix(o, "timeo=") || strings.HasPrefix(o, "retrans=") || strings.HasPrefix(o, "retry=") {
			return true
		}
	}
	return false
}

// A soft mount can report a failed write the server did apply, harmless for
// the metadata-only temp mount and not for the pods' data.
func TestNodePublishVolumeNFSMountsOnlyTheTempRootSoft(t *testing.T) {
	t.Setenv("TMPDIR", t.TempDir())

	// The fake drops the options of every mount an unmount leaves in place, so
	// both sets are read while the temp mount is being unmounted, after the
	// volume mount.
	var tempOpts, finalOpts []string
	m := mount.NewFakeMounter(nil)
	m.UnmountFunc = func(path string) error {
		for _, mp := range m.MountPoints {
			switch {
			case mp.Path == path:
				tempOpts = mp.Opts
			case mp.Device == "192.0.2.10:/export/data":
				finalOpts = mp.Opts
			}
		}
		return nil
	}
	w := &WrappedNodeService{mounter: m}
	if _, err := w.NodePublishVolume(context.Background(), nfsPublishRequest(filepath.Join(t.TempDir(), "target"))); err != nil {
		t.Fatalf("NodePublishVolume: %v", err)
	}

	for _, want := range []string{"soft", "retry=0"} {
		if !slices.Contains(tempOpts, want) {
			t.Errorf("temp mount options = %v, want %s", tempOpts, want)
		}
	}
	// The first mount of a server sets the TCP timeouts of the connection the
	// pods' mounts of it share.
	for _, o := range tempOpts {
		if strings.HasPrefix(o, "timeo=") || strings.HasPrefix(o, "retrans=") {
			t.Errorf("temp mount options = %v, want the default timeo and retrans", tempOpts)
		}
	}
	if finalOpts == nil {
		t.Fatal("the /data subdir was not mounted before the temp mount was unmounted")
	}
	if hasRetryTuning(finalOpts) {
		t.Errorf("volume mount options = %v, want a hard mount with default retry", finalOpts)
	}
}

// stalledMounter holds the first mount it gets until release is closed.
type stalledMounter struct {
	*mount.FakeMounter
	held    atomic.Bool
	entered chan struct{}
	release chan struct{}
}

func (b *stalledMounter) Mount(source, target, fstype string, opts []string) error {
	if b.held.CompareAndSwap(false, true) {
		close(b.entered)
		<-b.release
	}
	return b.FakeMounter.Mount(source, target, fstype, opts)
}

// kubelet retries a publish that outlives its deadline while the first call
// still runs; on a dead server each retry would block the same way.
func TestNodePublishVolumeNFSRefusesAPublishAlreadyInProgress(t *testing.T) {
	t.Setenv("TMPDIR", t.TempDir())
	m := &stalledMounter{
		FakeMounter: mount.NewFakeMounter(nil),
		entered:     make(chan struct{}),
		release:     make(chan struct{}),
	}
	w := &WrappedNodeService{mounter: m}
	target := filepath.Join(t.TempDir(), "target")

	first := make(chan error, 1)
	go func() {
		_, err := w.NodePublishVolume(context.Background(), nfsPublishRequest(target))
		first <- err
	}()
	<-m.entered

	_, err := w.NodePublishVolume(context.Background(), nfsPublishRequest(target))
	if status.Code(err) != codes.Aborted {
		t.Errorf("publish while one is in progress: err = %v, want code Aborted", err)
	}
	if _, err := w.NodePublishVolume(context.Background(), nfsPublishRequest(filepath.Join(t.TempDir(), "other"))); err != nil {
		t.Errorf("publish of the same volume at another target: %v", err)
	}

	close(m.release)
	if err := <-first; err != nil {
		t.Fatalf("first publish: %v", err)
	}
	if _, err := w.NodePublishVolume(context.Background(), nfsPublishRequest(target)); err != nil {
		t.Errorf("publish after the first one finished: %v", err)
	}
}

func TestNodePublishVolumeNFSReleasesTheTargetAfterAFailedPublish(t *testing.T) {
	t.Setenv("TMPDIR", t.TempDir())
	w := &WrappedNodeService{mounter: failingMounter{mount.NewFakeMounter(nil)}}
	req := nfsPublishRequest(filepath.Join(t.TempDir(), "target"))
	for i := range 2 {
		_, err := w.NodePublishVolume(context.Background(), req)
		if code := status.Code(err); code != codes.Internal {
			t.Fatalf("publish %d: err = %v, want code Internal from the failed mount", i+1, err)
		}
	}
}

// seedingMounter fills the first mount's target with what seed creates there.
type seedingMounter struct {
	*mount.FakeMounter
	seed func(target string) error
}

func (s *seedingMounter) Mount(source, target, fstype string, opts []string) error {
	if seed := s.seed; seed != nil {
		s.seed = nil
		if err := seed(target); err != nil {
			return err
		}
	}
	return s.FakeMounter.Mount(source, target, fstype, opts)
}

func seedRootAndData(root *string, link string) func(string) error {
	return func(target string) error {
		*root = target
		if err := os.WriteFile(filepath.Join(target, "f"), nil, 0o600); err != nil {
			return err
		}
		if err := os.Mkdir(filepath.Join(target, "data"), 0o750); err != nil {
			return err
		}
		return os.Symlink(link, filepath.Join(target, "data", "f"))
	}
}

func assertNotMigrated(t *testing.T, root string) {
	t.Helper()
	if _, err := os.Stat(filepath.Join(root, "f")); err != nil {
		t.Errorf("source was moved: %v", err)
	}
	if fi, err := os.Lstat(filepath.Join(root, "data", "f")); err != nil || fi.Mode()&os.ModeSymlink == 0 {
		t.Errorf("destination was replaced: %v, %v", fi, err)
	}
}

// A soft temp mount answers a stat with EIO while the server is unreachable;
// a rename issued once it is back would replace the file the stat missed.
func TestNodePublishVolumeNFSDoesNotMigrateOverAnUncheckedDestination(t *testing.T) {
	t.Setenv("TMPDIR", t.TempDir())
	var root string
	w := &WrappedNodeService{
		mounter: &seedingMounter{FakeMounter: mount.NewFakeMounter(nil), seed: seedRootAndData(&root, "elsewhere")},
		lstat:   func(string) (os.FileInfo, error) { return nil, syscall.EIO },
	}

	_, err := w.NodePublishVolume(context.Background(), nfsPublishRequest(filepath.Join(t.TempDir(), "target")))
	if status.Code(err) != codes.Internal {
		t.Fatalf("err = %v, want code Internal from the failed stat", err)
	}
	assertNotMigrated(t, root)
}

// The symlinks are the user's: one that loops or dangles is still an entry
// in /data, neither a reason to fail the publish nor one to replace it.
func TestNodePublishVolumeNFSLeavesASymlinkInDataAlone(t *testing.T) {
	for name, link := range map[string]string{"loop": "f", "dangling": "missing"} {
		t.Run(name, func(t *testing.T) {
			t.Setenv("TMPDIR", t.TempDir())
			var root string
			w := &WrappedNodeService{mounter: &seedingMounter{FakeMounter: mount.NewFakeMounter(nil), seed: seedRootAndData(&root, link)}}
			if _, err := w.NodePublishVolume(context.Background(), nfsPublishRequest(filepath.Join(t.TempDir(), "target"))); err != nil {
				t.Fatalf("NodePublishVolume: %v", err)
			}
			assertNotMigrated(t, root)
		})
	}
}

// The root entry stays where the pod cannot see it, so the skip has to be
// findable by an operator.
func TestNodePublishVolumeNFSWarnsAboutAnEntryLeftInTheRoot(t *testing.T) {
	t.Setenv("TMPDIR", t.TempDir())
	var logs bytes.Buffer
	klog.LogToStderr(false)
	klog.SetOutput(&logs)
	t.Cleanup(func() {
		klog.SetOutput(os.Stderr)
		klog.LogToStderr(true)
	})
	var root string
	w := &WrappedNodeService{mounter: &seedingMounter{FakeMounter: mount.NewFakeMounter(nil), seed: seedRootAndData(&root, "elsewhere")}}
	if _, err := w.NodePublishVolume(context.Background(), nfsPublishRequest(filepath.Join(t.TempDir(), "target"))); err != nil {
		t.Fatalf("NodePublishVolume: %v", err)
	}
	klog.Flush()
	for _, line := range strings.Split(logs.String(), "\n") {
		if strings.HasPrefix(line, "W") && strings.Contains(line, "volume pvc-1") && strings.Contains(line, "/data/f") {
			return
		}
	}
	t.Errorf("the skipped migration of f was not logged as a warning; log output:\n%s", logs.String())
}

func TestNodePublishVolumeNFSTempMountErrorsNameTheVolume(t *testing.T) {
	cases := map[string]struct {
		tmpdir func(t *testing.T) string
		w      *WrappedNodeService
	}{
		"temp dir": {
			tmpdir: func(t *testing.T) string { return filepath.Join(t.TempDir(), "missing") },
			w:      &WrappedNodeService{mounter: mount.NewFakeMounter(nil)},
		},
		"mount": {
			tmpdir: func(t *testing.T) string { return t.TempDir() },
			w:      &WrappedNodeService{mounter: failingMounter{mount.NewFakeMounter(nil)}},
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			t.Setenv("TMPDIR", tc.tmpdir(t))
			_, err := tc.w.NodePublishVolume(context.Background(), nfsPublishRequest(filepath.Join(t.TempDir(), "target")))
			if status.Code(err) != codes.Internal || !strings.Contains(err.Error(), "volume pvc-1") {
				t.Errorf("err = %v, want code Internal naming volume pvc-1", err)
			}
		})
	}
}

// On a dead server the stat inside MkdirAll fails on the temp mount point
// itself, and MkdirAll then reports the local temp dir as already existing
// instead of the errno the server's absence produced.
func TestNodePublishVolumeNFSReportsTheDataDirErrno(t *testing.T) {
	t.Setenv("TMPDIR", t.TempDir())
	w := &WrappedNodeService{
		mounter: mount.NewFakeMounter(nil),
		mkdir:   func(string, os.FileMode) error { return syscall.EIO },
	}
	_, err := w.NodePublishVolume(context.Background(), nfsPublishRequest(filepath.Join(t.TempDir(), "target")))
	if err == nil || !strings.Contains(err.Error(), syscall.EIO.Error()) || !strings.Contains(err.Error(), "volume pvc-1") {
		t.Errorf("err = %v, want the EIO from mkdir, naming volume pvc-1", err)
	}
}
