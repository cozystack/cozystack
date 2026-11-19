package main

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	csi "github.com/container-storage-interface/spec/lib/go/csi"
	"k8s.io/klog/v2"
	mount "k8s.io/mount-utils"
)

// A path that does not exist, so the fake mounter's symlink resolution keeps
// it as is and matches it against its mount table.
const tempMountPath = "/nonexistent/nfs-init-pvc-1-123"

// On Linux mount.New runs umount on a scratch dir to detect how it reports
// "not mounted", so it is built once, before any test puts a fake umount on
// PATH.
var execMounter = mount.New("")

func mountedFake(unmountErr error) *mount.FakeMounter {
	m := mount.NewFakeMounter([]mount.MountPoint{{Path: tempMountPath, Type: "nfs"}})
	if unmountErr != nil {
		m.UnmountFunc = func(string) error { return unmountErr }
	}
	return m
}

// fakeUmount puts an umount on PATH that appends its arguments to the returned
// log and then runs plainBody for a plain unmount or forceBody for umount -f.
func fakeUmount(t *testing.T, plainBody string) string {
	return fakeUmountForce(t, plainBody, "exit 0")
}

func fakeUmountForce(t *testing.T, plainBody, forceBody string) string {
	t.Helper()
	bin := t.TempDir()
	log := filepath.Join(bin, "calls")
	script := "#!/bin/sh\necho \"$*\" >> " + log + "\n" +
		"if [ \"$1\" = \"-f\" ]; then " + forceBody + "; fi\n" + plainBody + "\n"
	if err := os.WriteFile(filepath.Join(bin, "umount"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	return log
}

func umountCalls(t *testing.T, log string) []string {
	t.Helper()
	b, err := os.ReadFile(log)
	if err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	return strings.FieldsFunc(string(b), func(r rune) bool { return r == '\n' })
}

func tempDir(t *testing.T) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "nfs-init-pvc-1-123")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	return dir
}

// main wires mount.New(""); unmountWithin runs its own umount only for that
// type, so a different one would silently lose the forced retry.
func TestMountNewIsTheExecMounter(t *testing.T) {
	if _, ok := execMounter.(*mount.Mounter); !ok {
		t.Fatal("mount.New(\"\") is not *mount.Mounter")
	}
}

func TestUnmountWithinTriesAPlainUmountFirst(t *testing.T) {
	log := fakeUmount(t, "exit 0")
	dir := tempDir(t)

	if err := unmountWithin(execMounter, dir, 6*time.Second); err != nil {
		t.Fatalf("unmountWithin: %v", err)
	}
	if calls := umountCalls(t, log); !slices.Equal(calls, []string{dir}) {
		t.Errorf("umount calls = %v, want just the plain one", calls)
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Errorf("temp dir still present after a successful unmount: %v", err)
	}
}

// The plain umount runs the umount.nfs helper as a child. Against a vanished
// server the helper never returns from umount2(), and killing umount on the
// deadline orphans it with the output pipe still open.
func TestUnmountWithinForcesWhenThePlainUmountHangsWithAnOrphan(t *testing.T) {
	log := fakeUmount(t, "sleep 30 &\nwait")
	dir := tempDir(t)
	budget := 6 * time.Second

	start := time.Now()
	if err := unmountWithin(execMounter, dir, budget); err != nil {
		t.Fatalf("unmountWithin: %v", err)
	}
	if elapsed := time.Since(start); elapsed >= budget {
		t.Errorf("unmountWithin took %v, want under its %v budget", elapsed, budget)
	}
	if calls := umountCalls(t, log); !slices.Equal(calls, []string{dir, "-f " + dir}) {
		t.Errorf("umount calls = %v, want the plain one then -f", calls)
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Errorf("temp dir still present after the forced unmount: %v", err)
	}
}

func TestUnmountWithinTakesNotMountedFromTheForcedRetryAsDone(t *testing.T) {
	fakeUmountForce(t, "sleep 30 &\nwait", "echo \"umount: $2: not mounted.\" >&2; exit 32")
	dir := tempDir(t)

	if err := unmountWithin(execMounter, dir, 6*time.Second); err != nil {
		t.Fatalf("unmountWithin: %v", err)
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Errorf("temp dir still present after the forced retry found it unmounted: %v", err)
	}
}

func TestUnmountWithinReportsAFailedForcedRetry(t *testing.T) {
	log := fakeUmountForce(t, "sleep 30 &\nwait", "echo busy >&2; exit 32")
	dir := tempDir(t)

	err := unmountWithin(execMounter, dir, 6*time.Second)
	if err == nil || !strings.Contains(err.Error(), "busy") {
		t.Fatalf("unmountWithin error = %v, want the umount -f failure with its output", err)
	}
	if calls := umountCalls(t, log); !slices.Equal(calls, []string{dir, "-f " + dir}) {
		t.Errorf("umount calls = %v, want the plain one then -f", calls)
	}
	if _, err := os.Stat(dir); err != nil {
		t.Errorf("temp dir removed although it may still be mounted: %v", err)
	}
}

func TestUnmountWithinDoesNotForceAfterAFailedUmount(t *testing.T) {
	log := fakeUmount(t, "echo busy >&2; exit 32")
	dir := tempDir(t)

	err := unmountWithin(execMounter, dir, 6*time.Second)
	if err == nil || !strings.Contains(err.Error(), "busy") {
		t.Fatalf("unmountWithin error = %v, want the umount failure with its output", err)
	}
	if calls := umountCalls(t, log); !slices.Equal(calls, []string{dir}) {
		t.Errorf("umount calls = %v, want no -f after a failure that was not a timeout", calls)
	}
	if _, err := os.Stat(dir); err != nil {
		t.Errorf("temp dir removed although it may still be mounted: %v", err)
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

// blockingMounter stands for an unmount that does not return on its own, such
// as an umount stuck in uninterruptible sleep, which no signal ends.
type blockingMounter struct {
	*mount.FakeMounter
	release chan struct{}
	err     error
}

func (b *blockingMounter) Unmount(string) error {
	<-b.release
	return b.err
}

func TestUnmountWithinGivesUpOnAStalledUnmount(t *testing.T) {
	m := &blockingMounter{FakeMounter: mountedFake(nil), release: make(chan struct{})}
	defer close(m.release)

	done := make(chan error, 1)
	go func() { done <- unmountWithin(m, tempMountPath, 50*time.Millisecond) }()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("unmountWithin reported success for an unmount still running")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("unmountWithin did not return within its budget while the unmount stalled")
	}
}

// An unmount that outlives its budget still finishes in the background, and the
// temp dir can only go once it has: removed any earlier it is still a mount
// point and the remove fails, and nothing would try again.
func TestUnmountWithinRemovesTheDirAfterALateUnmount(t *testing.T) {
	dir := tempDir(t)
	m := &blockingMounter{FakeMounter: mountedFake(nil), release: make(chan struct{})}

	if err := unmountWithin(m, dir, 50*time.Millisecond); err == nil {
		t.Fatal("unmountWithin reported success for an unmount still running")
	}
	if _, err := os.Stat(dir); err != nil {
		t.Fatalf("temp dir gone while its unmount was still running: %v", err)
	}

	close(m.release)
	waitFor(t, "temp dir removed after the late unmount succeeded", func() bool {
		_, err := os.Stat(dir)
		return os.IsNotExist(err)
	})
}

type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.buf.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.buf.String()
}

// The caller has already logged "still running"; without a line from the
// worker nobody can tell a mount that cleaned up late from one that leaked.
func TestUnmountWithinLogsALateResult(t *testing.T) {
	var logs syncBuffer
	klog.LogToStderr(false)
	klog.SetOutput(&logs)
	t.Cleanup(func() {
		klog.SetOutput(os.Stderr)
		klog.LogToStderr(true)
	})

	for _, late := range []error{nil, errors.New("device busy")} {
		m := &blockingMounter{FakeMounter: mountedFake(nil), release: make(chan struct{}), err: late}
		if err := unmountWithin(m, tempMountPath, 50*time.Millisecond); err == nil {
			t.Fatal("unmountWithin reported success for an unmount still running")
		}
		close(m.release)
		want := "unmount of " + tempMountPath + " finished after the deadline"
		if late != nil {
			want = "unmount of " + tempMountPath + " failed after the deadline: device busy"
		}
		waitFor(t, "log line "+want, func() bool { return strings.Contains(logs.String(), want) })
	}
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for: %s", what)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestNodePublishVolumeNFSMountsTheTempRootWithNosharecache(t *testing.T) {
	tmpRoot := t.TempDir()
	t.Setenv("TMPDIR", tmpRoot)

	var tempOpts []string
	m := mount.NewFakeMounter(nil)
	m.UnmountFunc = func(path string) error {
		for _, mp := range m.MountPoints {
			if mp.Path == path {
				tempOpts = mp.Opts
			}
		}
		return nil
	}
	w := &WrappedNodeService{mounter: m}
	req := &csi.NodePublishVolumeRequest{
		VolumeId:       "pvc-1",
		TargetPath:     filepath.Join(t.TempDir(), "target"),
		PublishContext: map[string]string{nfsExportKey: "nfs://192.0.2.10:2049/export"},
	}
	if _, err := w.NodePublishVolume(context.Background(), req); err != nil {
		t.Fatalf("NodePublishVolume: %v", err)
	}

	// umount -f runs nfs_umount_begin on the superblock, killing every RPC in
	// flight on it. Without nosharecache the temp mount shares that superblock
	// with the pods' mounts of the same export on this node.
	if !slices.Contains(tempOpts, "nosharecache") {
		t.Errorf("temp mount options = %v, want nosharecache so a forced unmount stays on its own superblock", tempOpts)
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
