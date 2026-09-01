// SPDX-License-Identifier: Apache-2.0

package migrationcontroller

import (
	"context"
	"testing"

	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
	clientfake "sigs.k8s.io/controller-runtime/pkg/client/fake"

	migrationv1alpha1 "github.com/cozystack/cozystack/api/migration/v1alpha1"
)

// The window this covers: the scaffolding VirtualMachine is deleted before the
// status write that records what the import produced, so dying between the two
// leaves a healthy VMInstance and its disks with nothing pointing at them. The
// old behaviour reported Failed on a machine that works.
func TestReclaimOutputsFindsWhatStatusNeverRecorded(t *testing.T) {
	s := testScheme(t)
	tk := task("import", "tenant-foo", "vcenter", "replicated",
		migrationv1alpha1.VMImportRequest{ID: "vm-1", Name: "web-01"})
	tk.UID = types.UID("task-uid")
	// Status knows nothing: this is exactly the crash being recovered from.
	tk.Status.VMs = nil

	instance := newObject(vmInstanceGVK)
	instance.SetName("web-01")
	instance.SetNamespace("tenant-foo")
	instance.SetLabels(outputMarkers(tk, "vm-1"))

	disk0 := newObject(vmDiskGVK)
	disk0.SetName("web-01-disk-0")
	disk0.SetNamespace("tenant-foo")
	disk0.SetLabels(outputMarkers(tk, "vm-1"))

	disk1 := newObject(vmDiskGVK)
	disk1.SetName("web-01-disk-1")
	disk1.SetNamespace("tenant-foo")
	disk1.SetLabels(outputMarkers(tk, "vm-1"))

	// Another task's output of a similar shape must not be reclaimed: the
	// markers are what make this a lookup rather than a guess.
	otherTask := task("other", "tenant-foo", "vcenter", "replicated")
	otherTask.UID = types.UID("other-uid")
	stray := newObject(vmInstanceGVK)
	stray.SetName("web-99")
	stray.SetNamespace("tenant-foo")
	stray.SetLabels(outputMarkers(otherTask, "vm-1"))

	c := clientfake.NewClientBuilder().WithScheme(s).
		WithObjects(tk, instance, disk0, disk1, stray).Build()
	r := &VMImportTaskReconciler{Client: c, Scheme: s, Recorder: record.NewFakeRecorder(10)}

	got, err := r.reclaimOutputs(context.Background(), tk,
		&migrationv1alpha1.VMImportRequest{ID: "vm-1", Name: "web-01"})
	if err != nil {
		t.Fatalf("reclaimOutputs: %v", err)
	}
	if got == nil {
		t.Fatal("reclaimed nothing; the import would be reported Failed with its outputs healthy")
	}
	if got.VMInstance != "web-01" {
		t.Errorf("VMInstance = %q, want web-01", got.VMInstance)
	}
	if len(got.Disks) != 2 || got.Disks[0] != "web-01-disk-0" || got.Disks[1] != "web-01-disk-1" {
		t.Errorf("disks = %v, want them in order", got.Disks)
	}
}

// With no marked outputs there is nothing to reclaim, and saying so is what
// lets the caller report the real failure instead of inventing a success.
func TestReclaimOutputsFindsNothingWhenThereIsNothing(t *testing.T) {
	s := testScheme(t)
	tk := task("import", "tenant-foo", "vcenter", "replicated",
		migrationv1alpha1.VMImportRequest{ID: "vm-1", Name: "web-01"})
	tk.UID = types.UID("task-uid")

	c := clientfake.NewClientBuilder().WithScheme(s).WithObjects(tk).Build()
	r := &VMImportTaskReconciler{Client: c, Scheme: s, Recorder: record.NewFakeRecorder(10)}

	got, err := r.reclaimOutputs(context.Background(), tk,
		&migrationv1alpha1.VMImportRequest{ID: "vm-1", Name: "web-01"})
	if err != nil {
		t.Fatalf("reclaimOutputs: %v", err)
	}
	if got != nil {
		t.Errorf("reclaimed %+v out of nowhere", got)
	}
}
