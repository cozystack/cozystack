// SPDX-License-Identifier: Apache-2.0
package migrationcontroller

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	storagev1 "k8s.io/api/storage/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
	clientfake "sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	migrationv1alpha1 "github.com/cozystack/cozystack/api/migration/v1alpha1"
)

func storageClass(name string, mode storagev1.VolumeBindingMode, isDefault bool) *storagev1.StorageClass {
	sc := &storagev1.StorageClass{
		ObjectMeta:        metav1.ObjectMeta{Name: name},
		VolumeBindingMode: &mode,
	}
	if isDefault {
		sc.Annotations = map[string]string{defaultStorageClassAnnotation: "true"}
	}
	return sc
}

func readySource(name, namespace string) *migrationv1alpha1.VMImportSource {
	src := vsphereSource(name, namespace)
	src.Status.Conditions = []metav1.Condition{{
		Type:               migrationv1alpha1.SourceReadyCondition,
		Status:             metav1.ConditionTrue,
		Reason:             migrationv1alpha1.ReasonConnected,
		Message:            "ok",
		LastTransitionTime: metav1.Now(),
	}}
	return src
}

func task(name, namespace, source, class string, vms ...migrationv1alpha1.VMImportRequest) *migrationv1alpha1.VMImportTask {
	return &migrationv1alpha1.VMImportTask{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace, UID: types.UID(name + "-uid")},
		Spec: migrationv1alpha1.VMImportTaskSpec{
			SourceRef:    corev1.LocalObjectReference{Name: source},
			VMs:          vms,
			StorageClass: class,
		},
	}
}

// TestTaskRejectsWaitForFirstConsumer is the check that turns a hang into a
// message. A WaitForFirstConsumer class does not fail an import, it deadlocks
// it: nothing consumes the claim while CDI populates it. Failing at validation
// costs one edit; not failing costs a support ticket and a stuck transfer.
func TestTaskRejectsWaitForFirstConsumer(t *testing.T) {
	s := testScheme(t)
	src := readySource("vcenter", "tenant-foo")
	tk := task("import", "tenant-foo", "vcenter", "local",
		migrationv1alpha1.VMImportRequest{ID: "vm-1", Name: "web-01"})
	c := clientfake.NewClientBuilder().WithScheme(s).
		WithObjects(src, tk, storageClass("local", storagev1.VolumeBindingWaitForFirstConsumer, true)).
		WithStatusSubresource(src, tk).Build()

	r := &VMImportTaskReconciler{Client: c, Scheme: s, Recorder: record.NewFakeRecorder(10)}
	if _, err := r.Reconcile(context.Background(), reconcile.Request{
		NamespacedName: types.NamespacedName{Namespace: "tenant-foo", Name: "import"},
	}); err != nil {
		t.Fatalf("reconcile: %v", err)
	}

	got := &migrationv1alpha1.VMImportTask{}
	if err := c.Get(context.Background(), types.NamespacedName{Namespace: "tenant-foo", Name: "import"}, got); err != nil {
		t.Fatalf("get task: %v", err)
	}
	if got.Status.Phase != migrationv1alpha1.VMImportTaskPhaseFailed {
		t.Errorf("phase = %q, want Failed", got.Status.Phase)
	}
	if !strings.Contains(got.Status.Message, "WaitForFirstConsumer") {
		t.Errorf("message does not name the binding mode: %q", got.Status.Message)
	}
	// Nothing may have been created: a task that cannot succeed must not leave
	// Forklift objects behind for a tenant to wonder about.
	plan := newObject(planGVK)
	if err := c.Get(context.Background(), types.NamespacedName{
		Namespace: "tenant-foo", Name: planName("import", "vm-1"),
	}, plan); err == nil {
		t.Error("a Plan was created for a task that failed validation")
	}
}

// TestTaskWaitsForUnreadySource asserts a task does not start work while its
// connection is unusable, and says which connection and why.
func TestTaskWaitsForUnreadySource(t *testing.T) {
	s := testScheme(t)
	src := vsphereSource("vcenter", "tenant-foo") // no Ready condition
	tk := task("import", "tenant-foo", "vcenter", "replicated",
		migrationv1alpha1.VMImportRequest{ID: "vm-1", Name: "web-01"})
	c := clientfake.NewClientBuilder().WithScheme(s).
		WithObjects(src, tk).WithStatusSubresource(src, tk).Build()

	r := &VMImportTaskReconciler{Client: c, Scheme: s, Recorder: record.NewFakeRecorder(10)}
	if _, err := r.Reconcile(context.Background(), reconcile.Request{
		NamespacedName: types.NamespacedName{Namespace: "tenant-foo", Name: "import"},
	}); err != nil {
		t.Fatalf("reconcile: %v", err)
	}

	got := &migrationv1alpha1.VMImportTask{}
	if err := c.Get(context.Background(), types.NamespacedName{Namespace: "tenant-foo", Name: "import"}, got); err != nil {
		t.Fatalf("get task: %v", err)
	}
	if got.Status.Phase != migrationv1alpha1.VMImportTaskPhasePending {
		t.Errorf("phase = %q, want Pending", got.Status.Phase)
	}
	if !strings.Contains(got.Status.Message, "vcenter") {
		t.Errorf("message does not name the source: %q", got.Status.Message)
	}
}

// TestTaskRefusesToOverwriteExistingInstance locks in that the controller never
// takes over a tenant's object. An import that would clobber a running VM must
// stop and say so.
func TestTaskRefusesToOverwriteExistingInstance(t *testing.T) {
	s := testScheme(t)
	src := readySource("vcenter", "tenant-foo")
	tk := task("import", "tenant-foo", "vcenter", "replicated",
		migrationv1alpha1.VMImportRequest{ID: "vm-1", Name: "web-01"})
	occupied := newObject(vmInstanceGVK)
	occupied.SetName("web-01")
	occupied.SetNamespace("tenant-foo")

	c := clientfake.NewClientBuilder().WithScheme(s).
		WithObjects(src, tk, occupied, storageClass("replicated", storagev1.VolumeBindingImmediate, false)).
		WithStatusSubresource(src, tk).Build()

	r := &VMImportTaskReconciler{Client: c, Scheme: s, Recorder: record.NewFakeRecorder(10)}
	if _, err := r.Reconcile(context.Background(), reconcile.Request{
		NamespacedName: types.NamespacedName{Namespace: "tenant-foo", Name: "import"},
	}); err != nil {
		t.Fatalf("reconcile: %v", err)
	}

	got := &migrationv1alpha1.VMImportTask{}
	if err := c.Get(context.Background(), types.NamespacedName{Namespace: "tenant-foo", Name: "import"}, got); err != nil {
		t.Fatalf("get task: %v", err)
	}
	if got.Status.Phase != migrationv1alpha1.VMImportTaskPhaseFailed {
		t.Errorf("phase = %q, want Failed", got.Status.Phase)
	}
	if !strings.Contains(got.Status.Message, "already exists") {
		t.Errorf("message does not explain the collision: %q", got.Status.Message)
	}
}

// TestAggregatePhase covers the fold from per-VM phases to the task's own. The
// property that matters: one failed VM must not stop its siblings, and the task
// only reaches a terminal phase when nothing can still progress.
func TestAggregatePhase(t *testing.T) {
	p := func(phases ...migrationv1alpha1.VMImportTaskPhase) []migrationv1alpha1.VMImportStatus {
		out := make([]migrationv1alpha1.VMImportStatus, 0, len(phases))
		for i, ph := range phases {
			out = append(out, migrationv1alpha1.VMImportStatus{ID: string(rune('a' + i)), Phase: ph})
		}
		return out
	}
	cases := []struct {
		name string
		in   []migrationv1alpha1.VMImportStatus
		want migrationv1alpha1.VMImportTaskPhase
	}{
		{"empty", nil, migrationv1alpha1.VMImportTaskPhasePending},
		{"all succeeded", p(migrationv1alpha1.VMImportTaskPhaseSucceeded, migrationv1alpha1.VMImportTaskPhaseSucceeded), migrationv1alpha1.VMImportTaskPhaseSucceeded},
		{"all failed", p(migrationv1alpha1.VMImportTaskPhaseFailed), migrationv1alpha1.VMImportTaskPhaseFailed},
		{"mixed terminal is failed", p(migrationv1alpha1.VMImportTaskPhaseSucceeded, migrationv1alpha1.VMImportTaskPhaseFailed), migrationv1alpha1.VMImportTaskPhaseFailed},
		{
			"one failed while another transfers keeps going",
			p(migrationv1alpha1.VMImportTaskPhaseFailed, migrationv1alpha1.VMImportTaskPhaseTransferring),
			migrationv1alpha1.VMImportTaskPhaseTransferring,
		},
		{
			"furthest-along non-terminal wins",
			p(migrationv1alpha1.VMImportTaskPhaseValidating, migrationv1alpha1.VMImportTaskPhaseCreating),
			migrationv1alpha1.VMImportTaskPhaseCreating,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := aggregatePhase(tc.in); got != tc.want {
				t.Errorf("aggregatePhase = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestAdoptVolumeReplicatesVolumeModeAndAccessModes locks in a constraint that
// only a live cluster revealed. Pre-binding through volumeName bypasses the
// volumeMode match check, so a claim that silently defaults to Filesystem
// against a Block volume *binds anyway* and fails much later at attach time
// with a message pointing nowhere near the cause. The replacement claim must
// therefore copy the volume's own modes rather than assume a default.
func TestAdoptVolumeReplicatesVolumeModeAndAccessModes(t *testing.T) {
	block := corev1.PersistentVolumeBlock
	s := testScheme(t)
	tk := task("import", "tenant-foo", "vcenter", "replicated")

	pv := &corev1.PersistentVolume{
		ObjectMeta: metav1.ObjectMeta{Name: "pv-1"},
		Spec: corev1.PersistentVolumeSpec{
			AccessModes:                   []corev1.PersistentVolumeAccessMode{corev1.ReadWriteMany},
			VolumeMode:                    &block,
			StorageClassName:              "replicated",
			PersistentVolumeReclaimPolicy: corev1.PersistentVolumeReclaimDelete,
			Capacity:                      corev1.ResourceList{corev1.ResourceStorage: resource.MustParse("20Gi")},
		},
	}
	transferred := &corev1.PersistentVolumeClaim{
		ObjectMeta: metav1.ObjectMeta{Name: "forklift-claim", Namespace: "tenant-foo"},
		Spec: corev1.PersistentVolumeClaimSpec{
			VolumeName: "pv-1",
			Resources: corev1.VolumeResourceRequirements{
				Requests: corev1.ResourceList{corev1.ResourceStorage: resource.MustParse("20Gi")},
			},
		},
	}
	c := clientfake.NewClientBuilder().WithScheme(s).WithObjects(pv, transferred, tk).Build()
	r := &VMImportTaskReconciler{Client: c, Scheme: s, Recorder: record.NewFakeRecorder(10)}

	if err := r.adoptVolume(context.Background(), tk, "vm-1", "forklift-claim", "web-01-disk-0"); err != nil {
		t.Fatalf("adoptVolume: %v", err)
	}

	adopted := &corev1.PersistentVolumeClaim{}
	if err := c.Get(context.Background(), types.NamespacedName{
		Namespace: "tenant-foo", Name: "vm-disk-web-01-disk-0",
	}, adopted); err != nil {
		t.Fatalf("replacement claim was not created: %v", err)
	}
	if adopted.Spec.VolumeMode == nil || *adopted.Spec.VolumeMode != corev1.PersistentVolumeBlock {
		t.Errorf("volumeMode = %v, want Block copied from the volume", adopted.Spec.VolumeMode)
	}
	if len(adopted.Spec.AccessModes) != 1 || adopted.Spec.AccessModes[0] != corev1.ReadWriteMany {
		t.Errorf("accessModes = %v, want ReadWriteMany copied from the volume", adopted.Spec.AccessModes)
	}
	if adopted.Spec.VolumeName != "pv-1" {
		t.Errorf("volumeName = %q, want the claim pre-bound to the retained volume", adopted.Spec.VolumeName)
	}
	if adopted.Annotations[populatedForAnnotation] != "vm-disk-web-01-disk-0" {
		t.Errorf("populatedFor = %q, want it to equal the DataVolume name",
			adopted.Annotations[populatedForAnnotation])
	}

	// Retain is not advice: it is the only thing standing between a routine
	// DataVolume deletion and data loss, because CDI takes a controller owner
	// reference on the claim it adopts.
	gotPV := &corev1.PersistentVolume{}
	if err := c.Get(context.Background(), types.NamespacedName{Name: "pv-1"}, gotPV); err != nil {
		t.Fatalf("get pv: %v", err)
	}
	if gotPV.Spec.PersistentVolumeReclaimPolicy != corev1.PersistentVolumeReclaimRetain {
		t.Errorf("reclaim policy = %q, want Retain", gotPV.Spec.PersistentVolumeReclaimPolicy)
	}
	if gotPV.Spec.ClaimRef == nil || gotPV.Spec.ClaimRef.Name != "vm-disk-web-01-disk-0" {
		t.Fatalf("claimRef was not re-pointed: %+v", gotPV.Spec.ClaimRef)
	}
	if gotPV.Spec.ClaimRef.UID != adopted.UID {
		t.Error("claimRef carries no UID, leaving a window where another claim could bind the volume")
	}

	// The DataVolume the VMDisk release will adopt must exist and must not
	// re-fetch anything: a blank source is what makes the handoff free.
	dv := newObject(dataVolumeGVK)
	if err := c.Get(context.Background(), types.NamespacedName{
		Namespace: "tenant-foo", Name: "vm-disk-web-01-disk-0",
	}, dv); err != nil {
		t.Fatalf("DataVolume was not created: %v", err)
	}
	if _, found, _ := unstructured.NestedMap(dv.Object, "spec", "source", "blank"); !found {
		t.Error("DataVolume source is not blank; the handoff would trigger a second copy")
	}
	if dv.GetAnnotations()[helmReleaseNameAnnotation] != "vm-disk-web-01-disk-0" {
		t.Error("DataVolume lacks Helm ownership metadata, so the VMDisk release would collide with it")
	}

	// And the tenant-facing object, with no owner reference to the task: that
	// is what lets a finished task be deleted without losing the disk.
	disk := newObject(vmDiskGVK)
	if err := c.Get(context.Background(), types.NamespacedName{
		Namespace: "tenant-foo", Name: "web-01-disk-0",
	}, disk); err != nil {
		t.Fatalf("VMDisk was not created: %v", err)
	}
	if len(disk.GetOwnerReferences()) != 0 {
		t.Error("VMDisk carries an owner reference; deleting the task would destroy the imported disk")
	}
}

// TestAdoptVolumeRemovesOwningDataVolumeFirst locks in the second live-only
// finding. Deleting a claim while its DataVolume still exists makes CDI
// recreate the claim and provision a second volume, re-running the whole
// import — the exact duplicate copy this design exists to remove.
func TestAdoptVolumeRemovesOwningDataVolumeFirst(t *testing.T) {
	s := testScheme(t)
	tk := task("import", "tenant-foo", "vcenter", "replicated")

	pv := &corev1.PersistentVolume{
		ObjectMeta: metav1.ObjectMeta{Name: "pv-1"},
		Spec: corev1.PersistentVolumeSpec{
			StorageClassName: "replicated",
			Capacity:         corev1.ResourceList{corev1.ResourceStorage: resource.MustParse("10Gi")},
		},
	}
	// The transferred claim as CDI leaves it: owned by its DataVolume.
	controller := true
	transferred := &corev1.PersistentVolumeClaim{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "forklift-claim",
			Namespace: "tenant-foo",
			OwnerReferences: []metav1.OwnerReference{{
				APIVersion: "cdi.kubevirt.io/v1beta1",
				Kind:       "DataVolume",
				Name:       "forklift-claim",
				UID:        "dv-uid",
				Controller: &controller,
			}},
		},
		Spec: corev1.PersistentVolumeClaimSpec{
			VolumeName: "pv-1",
			Resources: corev1.VolumeResourceRequirements{
				Requests: corev1.ResourceList{corev1.ResourceStorage: resource.MustParse("10Gi")},
			},
		},
	}
	forkliftDV := newObject(dataVolumeGVK)
	forkliftDV.SetName("forklift-claim")
	forkliftDV.SetNamespace("tenant-foo")

	c := clientfake.NewClientBuilder().WithScheme(s).
		WithObjects(pv, transferred, forkliftDV, tk).Build()
	r := &VMImportTaskReconciler{Client: c, Scheme: s, Recorder: record.NewFakeRecorder(10)}

	if err := r.adoptVolume(context.Background(), tk, "vm-1", "forklift-claim", "web-01-disk-0"); err != nil {
		t.Fatalf("adoptVolume: %v", err)
	}

	gone := newObject(dataVolumeGVK)
	err := c.Get(context.Background(), types.NamespacedName{Namespace: "tenant-foo", Name: "forklift-claim"}, gone)
	if err == nil {
		t.Fatal("the transferring DataVolume still exists; CDI would recreate the claim and run a second full copy")
	}

	oldClaim := &corev1.PersistentVolumeClaim{}
	if err := c.Get(context.Background(), types.NamespacedName{
		Namespace: "tenant-foo", Name: "forklift-claim",
	}, oldClaim); err == nil {
		t.Error("the original claim was left behind")
	}
}

// TestAdoptVolumeIsIdempotent asserts a second pass over an already-adopted
// disk is a no-op rather than a second swap: the controller is re-entered on
// every requeue and after every restart.
func TestAdoptVolumeIsIdempotent(t *testing.T) {
	s := testScheme(t)
	tk := task("import", "tenant-foo", "vcenter", "replicated")
	already := &corev1.PersistentVolumeClaim{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "vm-disk-web-01-disk-0",
			Namespace: "tenant-foo",
			// The markers are what make this a resume rather than a collision:
			// an unmarked claim of the same name belongs to the tenant.
			Labels: map[string]string{
				migrationv1alpha1.OutputTaskUIDLabel: "import-uid",
				migrationv1alpha1.OutputVMIDLabel:    "vm-1",
			},
		},
		Spec: corev1.PersistentVolumeClaimSpec{
			VolumeName: "pv-1",
			Resources: corev1.VolumeResourceRequirements{
				Requests: corev1.ResourceList{corev1.ResourceStorage: resource.MustParse("20Gi")},
			},
		},
	}
	c := clientfake.NewClientBuilder().WithScheme(s).WithObjects(already, tk).Build()
	r := &VMImportTaskReconciler{Client: c, Scheme: s, Recorder: record.NewFakeRecorder(10)}

	// The source claim is deliberately absent: a second pass must not need it.
	if err := r.adoptVolume(context.Background(), tk, "vm-1", "forklift-claim", "web-01-disk-0"); err != nil {
		t.Fatalf("second pass over an adopted disk failed: %v", err)
	}
	disk := newObject(vmDiskGVK)
	if err := c.Get(context.Background(), types.NamespacedName{
		Namespace: "tenant-foo", Name: "web-01-disk-0",
	}, disk); err != nil {
		t.Fatalf("VMDisk was not created on the second pass: %v", err)
	}
}

// TestSourceVMResources asserts the source VM's shape is read off the object
// Forklift already built, which is why no inventory client is needed.
func TestSourceVMResources(t *testing.T) {
	vm := newObject(virtualMachineGVK)
	if err := unstructured.SetNestedMap(vm.Object, map[string]interface{}{
		"cores":   int64(2),
		"sockets": int64(2),
	}, "spec", "template", "spec", "domain", "cpu"); err != nil {
		t.Fatalf("set cpu: %v", err)
	}
	if err := unstructured.SetNestedField(vm.Object, "8Gi",
		"spec", "template", "spec", "domain", "memory", "guest"); err != nil {
		t.Fatalf("set memory: %v", err)
	}

	// Cores and sockets stay separate. vm-instance maps them to
	// domain.cpu.cores and domain.cpu.sockets and refuses to render unless both
	// are present, so multiplying them into one number both loses the topology
	// and fails the chart — observed live: "Either instanceType or resources
	// (cpu, sockets, memory) must be specified".
	cores, sockets, memory := sourceVMResources(vm)
	if cores != "2" {
		t.Errorf("cores = %q, want 2", cores)
	}
	if sockets != "2" {
		t.Errorf("sockets = %q, want 2", sockets)
	}
	if memory != "8Gi" {
		t.Errorf("memory = %q, want 8Gi", memory)
	}
}

// A source that reports cores but no socket count is single-socket. Saying so
// explicitly is what lets the chart render at all.
func TestSourceVMResourcesDefaultsSingleSocket(t *testing.T) {
	vm := newObject(virtualMachineGVK)
	if err := unstructured.SetNestedField(vm.Object, int64(4),
		"spec", "template", "spec", "domain", "cpu", "cores"); err != nil {
		t.Fatalf("set cores: %v", err)
	}
	cores, sockets, _ := sourceVMResources(vm)
	if cores != "4" || sockets != "1" {
		t.Errorf("cores/sockets = %q/%q, want 4/1", cores, sockets)
	}
}

// TestMigrationProgressReadsTheTransferStep asserts progress reflects the disk
// transfer rather than an average over every pipeline step, which would report
// a four-step pipeline as 25% done before any data moved.
func TestMigrationProgressReadsTheTransferStep(t *testing.T) {
	m := newObject(migrationGVK)
	if err := unstructured.SetNestedSlice(m.Object, []interface{}{
		map[string]interface{}{
			"id": "vm-1",
			"pipeline": []interface{}{
				map[string]interface{}{
					"name":     "Initialize",
					"progress": map[string]interface{}{"completed": int64(1), "total": int64(1)},
				},
				map[string]interface{}{
					"name":     "DiskTransfer",
					"progress": map[string]interface{}{"completed": int64(3), "total": int64(10)},
				},
			},
		},
	}, "status", "vms"); err != nil {
		t.Fatalf("set vms: %v", err)
	}

	done, progress, failure := migrationProgress(m)
	if done {
		t.Error("migration reported done with no completion timestamp")
	}
	if failure != "" {
		t.Errorf("unexpected failure: %q", failure)
	}
	if progress != 30 {
		t.Errorf("progress = %d, want 30", progress)
	}
}

// TestMigrationProgressFallsBackWhenStepsAreUnnamed guards against pegging
// progress at zero for a whole transfer. The pipeline step names are not part
// of any contract, and a release that renames them would silently break a
// name-only match — so an unrecognised pipeline still has to report something.
func TestMigrationProgressFallsBackWhenStepsAreUnnamed(t *testing.T) {
	m := newObject(migrationGVK)
	if err := unstructured.SetNestedSlice(m.Object, []interface{}{
		map[string]interface{}{
			"id": "vm-1",
			"pipeline": []interface{}{
				map[string]interface{}{
					"name":     "SomeFutureStepName",
					"progress": map[string]interface{}{"completed": int64(4096), "total": int64(16384)},
				},
			},
		},
	}, "status", "vms"); err != nil {
		t.Fatalf("set vms: %v", err)
	}
	_, progress, _ := migrationProgress(m)
	if progress != 25 {
		t.Errorf("progress = %d, want 25 from the unnamed step", progress)
	}
}

// TestMigrationProgressAcceptsCompletedPhase asserts the phase the engine
// settles on is treated as terminal. Waiting only on a completion timestamp
// would leave a finished transfer stuck in Transferring.
func TestMigrationProgressAcceptsCompletedPhase(t *testing.T) {
	m := newObject(migrationGVK)
	if err := unstructured.SetNestedSlice(m.Object, []interface{}{
		map[string]interface{}{"id": "vm-1", "phase": "Completed"},
	}, "status", "vms"); err != nil {
		t.Fatalf("set vms: %v", err)
	}
	done, progress, failure := migrationProgress(m)
	if !done {
		t.Error("a Completed phase must count as done")
	}
	if progress != 100 || failure != "" {
		t.Errorf("progress = %d, failure = %q", progress, failure)
	}
}

// TestAdoptVolumeOrphansAClaimWithNoDataVolume is the populator path, which is
// the path Forklift actually takes: the transferred disk arrives as a populated
// claim with no DataVolume, carrying an ownerReference to the VirtualMachine
// Forklift built. Anything that later deletes that VM takes the disk with it,
// and garbage collection wins any race against a copy — so the reference has to
// be cleared even though there is no DataVolume to trigger the cleanup.
func TestAdoptVolumeOrphansAClaimWithNoDataVolume(t *testing.T) {
	s := testScheme(t)
	tk := task("import", "tenant-foo", "vcenter", "replicated")

	pv := &corev1.PersistentVolume{
		ObjectMeta: metav1.ObjectMeta{Name: "pv-1"},
		Spec: corev1.PersistentVolumeSpec{
			StorageClassName: "replicated",
			Capacity:         corev1.ResourceList{corev1.ResourceStorage: resource.MustParse("16Gi")},
		},
	}
	blockOwnerDeletion := true
	notController := false
	populated := &corev1.PersistentVolumeClaim{
		ObjectMeta: metav1.ObjectMeta{
			// Forklift's own naming: <plan>-vm-<id>-<random>.
			Name:      "import-vm-1234-pdbdm",
			Namespace: "tenant-foo",
			OwnerReferences: []metav1.OwnerReference{{
				APIVersion:         "kubevirt.io/v1",
				Kind:               "VirtualMachine",
				Name:               "test-vm",
				UID:                "vm-uid",
				Controller:         &notController,
				BlockOwnerDeletion: &blockOwnerDeletion,
			}},
		},
		Spec: corev1.PersistentVolumeClaimSpec{
			VolumeName: "pv-1",
			Resources: corev1.VolumeResourceRequirements{
				Requests: corev1.ResourceList{corev1.ResourceStorage: resource.MustParse("16Gi")},
			},
		},
	}
	// Deliberately no DataVolume: that is what the populator path looks like.
	c := clientfake.NewClientBuilder().WithScheme(s).WithObjects(pv, populated, tk).Build()
	r := &VMImportTaskReconciler{Client: c, Scheme: s, Recorder: record.NewFakeRecorder(10)}

	if err := r.adoptVolume(context.Background(), tk, "vm-1", "import-vm-1234-pdbdm", "web-01-disk-0"); err != nil {
		t.Fatalf("adoptVolume on the populator path: %v", err)
	}

	adopted := &corev1.PersistentVolumeClaim{}
	if err := c.Get(context.Background(), types.NamespacedName{
		Namespace: "tenant-foo", Name: "vm-disk-web-01-disk-0",
	}, adopted); err != nil {
		t.Fatalf("replacement claim was not created: %v", err)
	}
	if len(adopted.OwnerReferences) != 0 {
		t.Error("the adopted claim carries an owner reference; deleting the Forklift VM would destroy the disk")
	}
	gotPV := &corev1.PersistentVolume{}
	if err := c.Get(context.Background(), types.NamespacedName{Name: "pv-1"}, gotPV); err != nil {
		t.Fatalf("get pv: %v", err)
	}
	if gotPV.Spec.PersistentVolumeReclaimPolicy != corev1.PersistentVolumeReclaimRetain {
		t.Error("the volume was not retained")
	}
}

// TestMigrationProgressSurfacesForkliftErrors asserts a failed transfer carries
// Forklift's own words rather than a paraphrase.
func TestMigrationProgressSurfacesForkliftErrors(t *testing.T) {
	m := newObject(migrationGVK)
	if err := unstructured.SetNestedSlice(m.Object, []interface{}{
		map[string]interface{}{
			"id": "vm-1",
			"error": map[string]interface{}{
				"reasons": []interface{}{"vddk: failed to open disk"},
			},
		},
	}, "status", "vms"); err != nil {
		t.Fatalf("set vms: %v", err)
	}
	done, _, failure := migrationProgress(m)
	if done {
		t.Error("a failed migration must not report done")
	}
	if !strings.Contains(failure, "failed to open disk") {
		t.Errorf("failure = %q, want Forklift's own reason", failure)
	}
}

// The topology comes out of an inventory record, and this is the shape the
// inventory actually returns — verified against a live Forklift v2.11.5
// inventory service on 2026-08-31, where a vSphere VM reported
// networks:[{kind:Network,id:"HaNetwork-..."}] and
// disks:[{...,datastore:{kind:Datastore,id:"61f94272-..."}}].
//
// An earlier revision read these from the Plan's VMNetworksNotMapped /
// VMStorageNotMapped conditions instead. That cannot work: those conditions
// carry the VM's own reference, not the network or datastore, so the map was
// built from parsed VM names and never resolved. The test that covered it
// asserted bare IDs Forklift never emits and so locked in the wrong contract.
func TestInventoryVMYieldsNetworksAndDatastores(t *testing.T) {
	const record = `{
	  "id": "86",
	  "name": "test-matthieu",
	  "firmware": "efi",
	  "powerState": "poweredOff",
	  "networks": [{"kind": "Network", "id": "HaNetwork-Cluster Hidora tools"}],
	  "disks": [
	    {"datastore": {"kind": "Datastore", "id": "61f94272-8f3a3584-b45f-3448edf98902"}},
	    {"datastore": {"kind": "Datastore", "id": "61f94272-8f3a3584-b45f-3448edf98902"}}
	  ]
	}`

	vm := &inventoryVM{}
	if err := json.Unmarshal([]byte(record), vm); err != nil {
		t.Fatalf("decoding the inventory record: %v", err)
	}
	if vm.Firmware != "efi" || vm.PowerState != "poweredOff" {
		t.Errorf("firmware/power = %q/%q", vm.Firmware, vm.PowerState)
	}
	if got := vm.networkIDs(); len(got) != 1 || got[0] != "HaNetwork-Cluster Hidora tools" {
		t.Errorf("networkIDs = %v", got)
	}
	// Two disks on one datastore yield one map entry, not two.
	if got := vm.datastoreIDs(); len(got) != 1 || got[0] != "61f94272-8f3a3584-b45f-3448edf98902" {
		t.Errorf("datastoreIDs = %v", got)
	}
}

// A Plan that does not pin the target power state lets Forklift match the
// source's: a production cutover migrates running machines, so the imported VM
// would boot the instant the transfer finished — a duplicate of a live machine
// on the network, started on the very volume the handoff is about to re-point.
func TestPlanKeepsTheImportedVMPoweredOff(t *testing.T) {
	s := testScheme(t)
	tk := task("import", "tenant-foo", "vcenter", "replicated")
	src := readySource("vcenter", "tenant-foo")
	c := clientfake.NewClientBuilder().WithScheme(s).WithObjects(tk, src).Build()
	r := &VMImportTaskReconciler{Client: c, Scheme: s, Recorder: record.NewFakeRecorder(10)}

	req := &migrationv1alpha1.VMImportRequest{ID: "vm-1234"}
	if err := r.createPlan(context.Background(), tk, src, req); err != nil {
		t.Fatalf("createPlan: %v", err)
	}

	plan := newObject(planGVK)
	if err := c.Get(context.Background(), types.NamespacedName{
		Namespace: "tenant-foo", Name: planName("import", "vm-1234"),
	}, plan); err != nil {
		t.Fatalf("Plan was not created: %v", err)
	}
	vms, _, _ := unstructured.NestedSlice(plan.Object, "spec", "vms")
	if len(vms) != 1 {
		t.Fatalf("spec.vms = %d entries, want 1", len(vms))
	}
	entry, _ := vms[0].(map[string]interface{})
	got, _, _ := unstructured.NestedString(entry, "targetPowerState")
	if got != "off" {
		t.Errorf("targetPowerState = %q, want \"off\" — Forklift would boot the migrated guest before the handoff", got)
	}
}

// The source's boot mode has to reach the VMInstance. A UEFI guest rendered
// with a BIOS bootloader imports "successfully" and then does not boot, which
// is the whole reason the vm-instance firmware field is a merge-order
// dependency of this controller.
func TestSourceFirmwareIsCarriedAcross(t *testing.T) {
	cases := []struct {
		name string
		set  func(vm *unstructured.Unstructured)
		want map[string]interface{}
	}{
		{"efi", func(vm *unstructured.Unstructured) {
			_ = unstructured.SetNestedMap(vm.Object, map[string]interface{}{},
				"spec", "template", "spec", "domain", "firmware", "bootloader", "efi")
		}, map[string]interface{}{"bootloader": "uefi"}},
		{"efi with secureBoot", func(vm *unstructured.Unstructured) {
			_ = unstructured.SetNestedField(vm.Object, true,
				"spec", "template", "spec", "domain", "firmware", "bootloader", "efi", "secureBoot")
		}, map[string]interface{}{"bootloader": "uefi", "secureBoot": true}},
		{"bios", func(vm *unstructured.Unstructured) {
			_ = unstructured.SetNestedMap(vm.Object, map[string]interface{}{},
				"spec", "template", "spec", "domain", "firmware", "bootloader", "bios")
		}, map[string]interface{}{"bootloader": "bios"}},
		// Nothing on the source means nothing written: the instance profile's
		// own default stands rather than being overridden with a guess.
		{"no firmware at all", func(vm *unstructured.Unstructured) {}, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			vm := newObject(virtualMachineGVK)
			tc.set(vm)
			got := sourceFirmware(vm)
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("sourceFirmware = %v, want %v", got, tc.want)
			}
		})
	}
}

// The failure this marker exists to stop: a first import half-succeeds, the
// tenant deletes the VMInstance but keeps the disks, and retries under the same
// name. Without an identity check the leftover claim reads as this task's own
// finished work, adoptVolume early-returns, and the freshly transferred claim
// keeps its ownerRef to the scaffolding VM — which fulfill then deletes, taking
// the new transfer with it. The import reports success over stale data.
func TestAdoptVolumeRefusesAClaimItDidNotCreate(t *testing.T) {
	s := testScheme(t)
	tk := task("import", "tenant-foo", "vcenter", "replicated")
	foreign := &corev1.PersistentVolumeClaim{
		ObjectMeta: metav1.ObjectMeta{Name: "vm-disk-web-01-disk-0", Namespace: "tenant-foo"},
		Spec: corev1.PersistentVolumeClaimSpec{
			VolumeName: "pv-1",
			Resources: corev1.VolumeResourceRequirements{
				Requests: corev1.ResourceList{corev1.ResourceStorage: resource.MustParse("20Gi")},
			},
		},
	}
	c := clientfake.NewClientBuilder().WithScheme(s).WithObjects(foreign, tk).Build()
	r := &VMImportTaskReconciler{Client: c, Scheme: s, Recorder: record.NewFakeRecorder(10)}

	err := r.adoptVolume(context.Background(), tk, "vm-1", "forklift-claim", "web-01-disk-0")
	if err == nil {
		t.Fatal("adoptVolume accepted a claim this task never created; the transfer would be garbage-collected")
	}
	var verr *validationError
	if !asValidationError(err, &verr) {
		t.Fatalf("error = %v, want a tenant-visible validationError", err)
	}
	if verr.reason != migrationv1alpha1.ReasonOutputExists {
		t.Errorf("reason = %q, want %q", verr.reason, migrationv1alpha1.ReasonOutputExists)
	}
}

// A claim carrying another task's marker is just as foreign as an unmarked one:
// names are reused, so the UID is what decides.
func TestAdoptVolumeRefusesAnotherTasksOutput(t *testing.T) {
	s := testScheme(t)
	tk := task("import", "tenant-foo", "vcenter", "replicated")
	other := &corev1.PersistentVolumeClaim{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "vm-disk-web-01-disk-0",
			Namespace: "tenant-foo",
			Labels: map[string]string{
				migrationv1alpha1.OutputTaskUIDLabel: "some-older-task-uid",
				migrationv1alpha1.OutputVMIDLabel:    "vm-1",
			},
		},
		Spec: corev1.PersistentVolumeClaimSpec{VolumeName: "pv-1"},
	}
	c := clientfake.NewClientBuilder().WithScheme(s).WithObjects(other, tk).Build()
	r := &VMImportTaskReconciler{Client: c, Scheme: s, Recorder: record.NewFakeRecorder(10)}

	if err := r.adoptVolume(context.Background(), tk, "vm-1", "forklift-claim", "web-01-disk-0"); err == nil {
		t.Fatal("adoptVolume adopted an output belonging to a different task")
	}
}

// A deleted task must take the engine's transfer volumes with it. They carry no
// owner reference — the engine tracks them by label — so deleting the task
// removes the Plan and leaves the volume behind, still retrying the transfer
// against the source with nothing left to collect it. Observed live: 37 minutes
// and 8 restarts after its task was gone.
//
// The outputs must survive the same sweep, which is the other half of the test:
// they are what the tenant keeps.
func TestFinalizerRemovesTransferVolumesButNotOutputs(t *testing.T) {
	s := testScheme(t)
	tk := task("import", "tenant-foo", "vcenter", "replicated",
		migrationv1alpha1.VMImportRequest{ID: "vm-1", Name: "web-01"})
	tk.Finalizers = []string{migrationv1alpha1.TaskFinalizer}
	now := metav1.Now()
	tk.DeletionTimestamp = &now

	plan := newObject(planGVK)
	plan.SetName(planName("import", "vm-1"))
	plan.SetNamespace("tenant-foo")
	plan.SetUID(types.UID("plan-uid-1"))

	transfer := newObject(dataVolumeGVK)
	transfer.SetName("import-vm-1-abcde")
	transfer.SetNamespace("tenant-foo")
	transfer.SetLabels(map[string]string{"plan": "plan-uid-1", "vmID": "vm-1"})

	// The imported disk: created by this controller, carries Helm metadata and
	// no engine label, and must be left alone.
	output := newObject(dataVolumeGVK)
	output.SetName("vm-disk-web-01-disk-0")
	output.SetNamespace("tenant-foo")
	output.SetLabels(map[string]string{"app.kubernetes.io/instance": "vm-disk-web-01-disk-0"})

	c := clientfake.NewClientBuilder().WithScheme(s).
		WithObjects(tk, plan, transfer, output).Build()
	r := &VMImportTaskReconciler{Client: c, Scheme: s, Recorder: record.NewFakeRecorder(10)}

	if _, err := r.finalize(context.Background(), tk); err != nil {
		t.Fatalf("finalize: %v", err)
	}

	got := newObject(dataVolumeGVK)
	err := c.Get(context.Background(), types.NamespacedName{
		Namespace: "tenant-foo", Name: "import-vm-1-abcde",
	}, got)
	if !apierrors.IsNotFound(err) {
		t.Errorf("the engine's transfer volume survived the task, got err %v — it would keep retrying forever", err)
	}

	if err := c.Get(context.Background(), types.NamespacedName{
		Namespace: "tenant-foo", Name: "vm-disk-web-01-disk-0",
	}, newObject(dataVolumeGVK)); err != nil {
		t.Errorf("the imported disk was removed with the task: %v", err)
	}

	if controllerutil.ContainsFinalizer(tk, migrationv1alpha1.TaskFinalizer) {
		t.Error("the finalizer was not released; the task would never delete")
	}
}

// Outputs must carry the marker, or the checks above can never recognise them
// on a later pass and every resume becomes a collision.
func TestAdoptedOutputsCarryTheTaskMarker(t *testing.T) {
	tk := task("import", "tenant-foo", "vcenter", "replicated")
	obj := newObject(vmDiskGVK)
	stampOutput(obj, tk, "vm-1")

	if !isOwnOutput(obj, tk, "vm-1") {
		t.Error("a freshly stamped output is not recognised as this task's own")
	}
	if isOwnOutput(obj, tk, "vm-2") {
		t.Error("an output for one source VM is recognised as another's")
	}
	other := task("import", "tenant-foo", "vcenter", "replicated")
	other.UID = "different-uid"
	if isOwnOutput(obj, other, "vm-1") {
		t.Error("an output is recognised as belonging to a different task")
	}
}
