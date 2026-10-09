// SPDX-License-Identifier: Apache-2.0
package backupcontroller

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	velerov1 "github.com/vmware-tanzu/velero/pkg/apis/velero/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	"k8s.io/client-go/kubernetes/scheme"
	k8stesting "k8s.io/client-go/testing"
	"k8s.io/client-go/tools/record"
	"sigs.k8s.io/controller-runtime/pkg/client"
	clientfake "sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	strategyv1alpha1 "github.com/cozystack/cozystack/api/backups/strategy/v1alpha1"
	backupsv1alpha1 "github.com/cozystack/cozystack/api/backups/v1alpha1"
)

func vmDiskTestScheme() *runtime.Scheme {
	s := runtime.NewScheme()
	_ = scheme.AddToScheme(s)
	_ = backupsv1alpha1.AddToScheme(s)
	_ = strategyv1alpha1.AddToScheme(s)
	_ = velerov1.AddToScheme(s)
	return s
}

func diskSelector() *metav1.LabelSelector {
	return &metav1.LabelSelector{MatchLabels: map[string]string{"app.kubernetes.io/instance": "vm-disk-{{ .Application.metadata.name }}"}}
}

// Velero rejects a Backup or Restore that carries both labelSelector and
// orLabelSelectors, so a VMDisk strategy written with labelSelector must not
// get the per-disk orLabelSelectors a VMInstance needs.
func TestCreateVeleroBackup_VMDiskKeepsStrategySelector(t *testing.T) {
	s := vmDiskTestScheme()
	app := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "data", Namespace: "tenant-root"},
		Spec:       corev1.PodSpec{Containers: []corev1.Container{{Name: "c", Image: "i"}}},
	}
	mapping := &meta.RESTMapping{
		Resource:         schema.GroupVersionResource{Version: "v1", Resource: "pods"},
		GroupVersionKind: schema.GroupVersionKind{Version: "v1", Kind: "Pod"},
		Scope:            meta.RESTScopeNamespace,
	}
	bj := &backupsv1alpha1.BackupJob{
		ObjectMeta: metav1.ObjectMeta{Name: "bj", Namespace: "tenant-root"},
		Spec: backupsv1alpha1.BackupJobSpec{ApplicationRef: corev1.TypedLocalObjectReference{
			APIGroup: new("apps.cozystack.io"), Kind: vmDiskAppKind, Name: "data",
		}},
	}
	strategy := &strategyv1alpha1.Velero{
		ObjectMeta: metav1.ObjectMeta{Name: "s"},
		Spec: strategyv1alpha1.VeleroSpec{Template: strategyv1alpha1.VeleroTemplate{
			Spec: velerov1.BackupSpec{LabelSelector: diskSelector()},
		}},
	}
	c := clientfake.NewClientBuilder().WithScheme(s).WithObjects(bj, strategy).Build()
	r := &BackupJobReconciler{Client: c, Interface: dynamicfake.NewSimpleDynamicClient(s, app),
		RESTMapper: &mockRESTMapper{mapping: mapping}, Scheme: s, Recorder: record.NewFakeRecorder(10)}

	if err := r.createVeleroBackup(context.Background(), bj, strategy, &ResolvedBackupConfig{}); err != nil {
		t.Fatalf("createVeleroBackup: %v", err)
	}
	backups := &velerov1.BackupList{}
	if err := c.List(context.Background(), backups, client.InNamespace(veleroNamespace)); err != nil || len(backups.Items) != 1 {
		t.Fatalf("list Velero Backups: %v (%d items)", err, len(backups.Items))
	}
	spec := backups.Items[0].Spec
	if spec.LabelSelector == nil || len(spec.OrLabelSelectors) != 0 {
		t.Fatalf("labelSelector %v, orLabelSelectors %d; want the strategy's labelSelector alone", spec.LabelSelector, len(spec.OrLabelSelectors))
	}
}

func TestCreateVeleroRestore_VMDiskKeepsStrategySelector(t *testing.T) {
	s := vmDiskTestScheme()
	ns := "tenant-root"
	backup := &backupsv1alpha1.Backup{
		ObjectMeta: metav1.ObjectMeta{Name: "bk", Namespace: ns},
		Spec: backupsv1alpha1.BackupSpec{ApplicationRef: corev1.TypedLocalObjectReference{
			APIGroup: new("apps.cozystack.io"), Kind: vmDiskAppKind, Name: "data",
		}},
	}
	restoreJob := &backupsv1alpha1.RestoreJob{
		ObjectMeta: metav1.ObjectMeta{Name: "rj", Namespace: ns, UID: "uid-rj"},
		Spec:       backupsv1alpha1.RestoreJobSpec{BackupRef: corev1.LocalObjectReference{Name: "bk"}},
	}
	strategy := &strategyv1alpha1.Velero{
		ObjectMeta: metav1.ObjectMeta{Name: "s"},
		Spec: strategyv1alpha1.VeleroSpec{Template: strategyv1alpha1.VeleroTemplate{
			RestoreSpec: &velerov1.RestoreSpec{LabelSelector: diskSelector()},
		}},
	}
	urRaw, _ := json.Marshal(vmInstanceResources{
		DataVolumes: []backupsv1alpha1.DataVolumeResource{{DataVolumeName: "vm-disk-data", ApplicationName: "data"}},
	})
	c := clientfake.NewClientBuilder().WithScheme(s).WithObjects(backup, restoreJob, strategy).Build()
	r := &RestoreJobReconciler{Client: c, Scheme: s, Recorder: record.NewFakeRecorder(10)}

	if err := r.createVeleroRestore(context.Background(), restoreJob, backup, strategy, "velero-bk", &runtime.RawExtension{Raw: urRaw},
		restoreTarget{Namespace: ns, AppName: "data", AppKind: vmDiskAppKind}, RestoreOptions{}); err != nil {
		t.Fatalf("createVeleroRestore: %v", err)
	}
	restores := &velerov1.RestoreList{}
	if err := c.List(context.Background(), restores, client.InNamespace(veleroNamespace)); err != nil || len(restores.Items) != 1 {
		t.Fatalf("list Velero Restores: %v (%d items)", err, len(restores.Items))
	}
	spec := restores.Items[0].Spec
	if spec.LabelSelector == nil || len(spec.OrLabelSelectors) != 0 {
		t.Fatalf("labelSelector %v, orLabelSelectors %d; want the strategy's labelSelector alone", spec.LabelSelector, len(spec.OrLabelSelectors))
	}
}

// A VMDisk Backup taken before underlying resources were recorded still
// restores its disk prepared, and the in-use refusal still applies to it.
func TestPrepareForRestore_VMDiskBackupWithoutUnderlyingResources(t *testing.T) {
	const ns = "tenant-root"
	backup := &backupsv1alpha1.Backup{
		ObjectMeta: metav1.ObjectMeta{Name: "bk", Namespace: ns},
		Spec: backupsv1alpha1.BackupSpec{ApplicationRef: corev1.TypedLocalObjectReference{
			APIGroup: new("apps.cozystack.io"), Kind: vmDiskAppKind, Name: "data",
		}},
	}
	restoreJob := &backupsv1alpha1.RestoreJob{
		ObjectMeta: metav1.ObjectMeta{Name: "rj", Namespace: ns},
		Spec:       backupsv1alpha1.RestoreJobSpec{BackupRef: corev1.LocalObjectReference{Name: "bk"}},
	}
	target := restoreTarget{Namespace: ns, AppName: "data", AppKind: vmDiskAppKind}

	t.Run("its disk is prepared", func(t *testing.T) {
		hr := makeUnstructuredHelmRelease("vm-disk-data", ns, nil)
		r := newTestRestoreJobReconcilerWithDynamic(t, []runtime.Object{hr}, restoreJob.DeepCopy(), backup)
		ready, _, err := r.prepareForRestore(context.Background(), restoreJob.DeepCopy(), backup, nil, target, RestoreOptions{KeepOriginalPVC: new(true)})
		if err != nil || !ready {
			t.Fatalf("prepareForRestore = ready %v, err %v; want ready", ready, err)
		}
		got, err := r.Resource(helmReleaseGVR).Namespace(ns).Get(context.Background(), "vm-disk-data", metav1.GetOptions{})
		if err != nil {
			t.Fatalf("get HelmRelease: %v", err)
		}
		if suspended, _, _ := unstructured.NestedBool(got.Object, "spec", "suspend"); !suspended {
			t.Fatalf("the disk's HelmRelease was not suspended")
		}
	})

	t.Run("a mounted disk is refused", func(t *testing.T) {
		pod := &corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{Name: "virt-launcher-web", Namespace: ns},
			Spec: corev1.PodSpec{
				Containers: []corev1.Container{{Name: "compute", Image: "virt-launcher"}},
				Volumes: []corev1.Volume{{Name: "disk", VolumeSource: corev1.VolumeSource{
					PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{ClaimName: "vm-disk-data"},
				}}},
			},
			Status: corev1.PodStatus{Phase: corev1.PodRunning},
		}
		r := newTestRestoreJobReconciler(t, restoreJob.DeepCopy(), backup, pod)
		if _, _, err := r.prepareForRestore(context.Background(), restoreJob.DeepCopy(), backup, nil, target, RestoreOptions{KeepOriginalPVC: new(true)}); err == nil {
			t.Fatalf("prepareForRestore accepted a mounted disk")
		}
	})
}

// Failing to list Pods must not read as "not in use": the restore waits and
// touches nothing.
func TestPrepareForRestore_VMDiskInUseCheckFailsClosed(t *testing.T) {
	const ns = "tenant-root"
	s := vmDiskTestScheme()
	backup := &backupsv1alpha1.Backup{
		ObjectMeta: metav1.ObjectMeta{Name: "bk", Namespace: ns},
		Spec: backupsv1alpha1.BackupSpec{ApplicationRef: corev1.TypedLocalObjectReference{
			APIGroup: new("apps.cozystack.io"), Kind: vmDiskAppKind, Name: "data",
		}},
	}
	restoreJob := &backupsv1alpha1.RestoreJob{ObjectMeta: metav1.ObjectMeta{Name: "rj", Namespace: ns}}
	c := clientfake.NewClientBuilder().WithScheme(s).WithObjects(backup, restoreJob).
		WithStatusSubresource(&backupsv1alpha1.RestoreJob{}).
		WithInterceptorFuncs(interceptor.Funcs{List: func(ctx context.Context, cl client.WithWatch, list client.ObjectList, opts ...client.ListOption) error {
			if _, ok := list.(*corev1.PodList); ok {
				return errors.New("pods is forbidden")
			}
			return cl.List(ctx, list, opts...)
		}}).Build()
	hr := makeUnstructuredHelmRelease("vm-disk-data", ns, nil)
	r := &RestoreJobReconciler{Client: c, Interface: dynamicfake.NewSimpleDynamicClient(s, hr), Scheme: s, Recorder: record.NewFakeRecorder(10)}

	ready, res, err := r.prepareForRestore(context.Background(), restoreJob, backup, nil,
		restoreTarget{Namespace: ns, AppName: "data", AppKind: vmDiskAppKind}, RestoreOptions{KeepOriginalPVC: new(true)})
	if err != nil || ready || res.RequeueAfter == 0 {
		t.Fatalf("prepareForRestore = ready %v, requeue %v, err %v; want a requeue", ready, res.RequeueAfter, err)
	}
	got, err := r.Resource(helmReleaseGVR).Namespace(ns).Get(context.Background(), "vm-disk-data", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("get HelmRelease: %v", err)
	}
	if suspended, _, _ := unstructured.NestedBool(got.Object, "spec", "suspend"); suspended {
		t.Fatalf("the HelmRelease was suspended although the in-use check could not run")
	}
}

// A restore that fails after preparation leaves the disk suspended and its data
// in the -orig- PVC; the failure has to say so, since nothing is rolled back.
func TestPreparedRestoreLeftovers(t *testing.T) {
	restoreJob := &backupsv1alpha1.RestoreJob{ObjectMeta: metav1.ObjectMeta{Name: "rj", Namespace: "tenant-root"}}
	vmDisk := &backupsv1alpha1.Backup{Spec: backupsv1alpha1.BackupSpec{ApplicationRef: corev1.TypedLocalObjectReference{Kind: vmDiskAppKind, Name: "data"}}}
	orig := "vm-disk-data-orig-" + shortHash("rj")

	got := preparedRestoreLeftovers(restoreJob, vmDisk, nil, restoreTarget{}, RestoreOptions{KeepOriginalPVC: new(true)})
	for _, want := range []string{"suspended HelmRelease vm-disk-data", "PVC " + orig} {
		if !strings.Contains(got, want) {
			t.Errorf("leftovers %q does not mention %q", got, want)
		}
	}
	if got := preparedRestoreLeftovers(restoreJob, vmDisk, nil, restoreTarget{}, RestoreOptions{KeepOriginalPVC: new(false)}); strings.Contains(got, "-orig-") {
		t.Errorf("leftovers %q names an -orig- PVC although keepOriginalPVC is off", got)
	}
	if got := preparedRestoreLeftovers(restoreJob, vmDisk, nil, restoreTarget{IsCopy: true}, RestoreOptions{KeepOriginalPVC: new(true)}); got != "" {
		t.Errorf("a restore to a copy prepares nothing, got %q", got)
	}
}

// Every way an in-place restore can fail once its target was prepared has to
// name what preparation left behind.
func TestReconcileVeleroRestore_FailureAfterPreparationNamesLeftovers(t *testing.T) {
	const ns = "tenant-root"
	orig := "vm-disk-data-orig-" + shortHash("rj")
	veleroRestore := func(name string, phase velerov1.RestorePhase) *velerov1.Restore {
		return &velerov1.Restore{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: veleroNamespace, Labels: map[string]string{
				backupsv1alpha1.OwningJobNameLabel:      "rj",
				backupsv1alpha1.OwningJobNamespaceLabel: ns,
			}},
			Status: velerov1.RestoreStatus{Phase: phase},
		}
	}
	failRestoreCreate := interceptor.Funcs{Create: func(ctx context.Context, cl client.WithWatch, obj client.Object, opts ...client.CreateOption) error {
		if _, ok := obj.(*velerov1.Restore); ok {
			return errors.New("admission webhook denied the request")
		}
		return cl.Create(ctx, obj, opts...)
	}}

	for _, tc := range []struct {
		name     string
		restores []client.Object
		funcs    interceptor.Funcs
		cause    string
	}{
		{name: "the Velero Restore partially failed", restores: []client.Object{veleroRestore("rj-a", velerov1.RestorePhasePartiallyFailed)}, cause: "PartiallyFailed"},
		{name: "several Velero Restores own the job", restores: []client.Object{veleroRestore("rj-a", ""), veleroRestore("rj-b", "")}, cause: "multiple Velero Restores"},
		{name: "Velero rejected the Restore", restores: []client.Object{func() client.Object {
			vr := veleroRestore("rj-a", velerov1.RestorePhaseFailedValidation)
			vr.Status.ValidationErrors = []string{"backup velero-bk not found"}
			return vr
		}()}, cause: "FailedValidation: backup velero-bk not found"},
		{name: "the Velero Restore could not be created", funcs: failRestoreCreate, cause: "failed to create Velero Restore"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := vmDiskTestScheme()
			started := metav1.Now()
			restoreJob := &backupsv1alpha1.RestoreJob{
				ObjectMeta: metav1.ObjectMeta{Name: "rj", Namespace: ns},
				Spec:       backupsv1alpha1.RestoreJobSpec{BackupRef: corev1.LocalObjectReference{Name: "bk"}},
				Status:     backupsv1alpha1.RestoreJobStatus{Phase: backupsv1alpha1.RestoreJobPhaseRunning, StartedAt: &started},
			}
			backup := &backupsv1alpha1.Backup{
				ObjectMeta: metav1.ObjectMeta{Name: "bk", Namespace: ns},
				Spec: backupsv1alpha1.BackupSpec{
					ApplicationRef: corev1.TypedLocalObjectReference{APIGroup: new("apps.cozystack.io"), Kind: vmDiskAppKind, Name: "data"},
					StrategyRef:    corev1.TypedLocalObjectReference{APIGroup: new(strategyv1alpha1.GroupVersion.Group), Kind: strategyv1alpha1.VeleroStrategyKind, Name: "s"},
					DriverMetadata: map[string]string{veleroBackupNameMetadataKey: "velero-bk"},
				},
			}
			strategy := &strategyv1alpha1.Velero{ObjectMeta: metav1.ObjectMeta{Name: "s"}}
			c := clientfake.NewClientBuilder().WithScheme(s).
				WithObjects(append([]client.Object{restoreJob, backup, strategy, completedVeleroBackup("velero-bk")}, tc.restores...)...).
				WithStatusSubresource(&backupsv1alpha1.RestoreJob{}).
				WithInterceptorFuncs(tc.funcs).Build()
			recorder := record.NewFakeRecorder(20)
			r := &RestoreJobReconciler{Client: c, Interface: dynamicfake.NewSimpleDynamicClient(s), Scheme: s, Recorder: recorder}

			rj := &backupsv1alpha1.RestoreJob{}
			if err := c.Get(context.Background(), client.ObjectKey{Namespace: ns, Name: "rj"}, rj); err != nil {
				t.Fatalf("get RestoreJob: %v", err)
			}
			if _, err := r.reconcileVeleroRestore(context.Background(), rj, backup); err != nil {
				t.Fatalf("reconcileVeleroRestore: %v", err)
			}
			if err := c.Get(context.Background(), client.ObjectKey{Namespace: ns, Name: "rj"}, rj); err != nil {
				t.Fatalf("get RestoreJob: %v", err)
			}
			if rj.Status.Phase != backupsv1alpha1.RestoreJobPhaseFailed {
				t.Fatalf("Phase = %q, want Failed", rj.Status.Phase)
			}
			for _, want := range []string{tc.cause, "suspended HelmRelease vm-disk-data", "PVC " + orig} {
				if !strings.Contains(rj.Status.Message, want) {
					t.Errorf("message %q does not mention %q", rj.Status.Message, want)
				}
			}
			warned := false
			for len(recorder.Events) > 0 {
				ev := <-recorder.Events
				if strings.HasPrefix(ev, "Warning RestoreFailed") && strings.Contains(ev, orig) {
					warned = true
				}
			}
			if !warned {
				t.Errorf("no Warning RestoreFailed event naming %s", orig)
			}
		})
	}
}

func completedVeleroBackup(name string) *velerov1.Backup {
	return &velerov1.Backup{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: veleroNamespace},
		Status:     velerov1.BackupStatus{Phase: velerov1.BackupPhaseCompleted},
	}
}

// A Velero Backup that is gone or failed cannot be restored; the job fails
// before preparation takes the target apart.
func TestReconcileVeleroRestore_UnrestorableVeleroBackupFailsBeforePreparation(t *testing.T) {
	const ns = "tenant-root"
	for _, tc := range []struct {
		name  string
		vb    []client.Object
		cause string
	}{
		{name: "the Velero Backup is gone", cause: "no longer exists"},
		{name: "the Velero Backup failed", vb: []client.Object{&velerov1.Backup{
			ObjectMeta: metav1.ObjectMeta{Name: "velero-bk", Namespace: veleroNamespace},
			Status:     velerov1.BackupStatus{Phase: velerov1.BackupPhaseFailed},
		}}, cause: `is "Failed"`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := vmDiskTestScheme()
			started := metav1.Now()
			restoreJob := &backupsv1alpha1.RestoreJob{
				ObjectMeta: metav1.ObjectMeta{Name: "rj", Namespace: ns},
				Spec:       backupsv1alpha1.RestoreJobSpec{BackupRef: corev1.LocalObjectReference{Name: "bk"}},
				Status:     backupsv1alpha1.RestoreJobStatus{Phase: backupsv1alpha1.RestoreJobPhaseRunning, StartedAt: &started},
			}
			backup := &backupsv1alpha1.Backup{
				ObjectMeta: metav1.ObjectMeta{Name: "bk", Namespace: ns},
				Spec: backupsv1alpha1.BackupSpec{
					ApplicationRef: corev1.TypedLocalObjectReference{APIGroup: new("apps.cozystack.io"), Kind: vmDiskAppKind, Name: "data"},
					StrategyRef:    corev1.TypedLocalObjectReference{APIGroup: new(strategyv1alpha1.GroupVersion.Group), Kind: strategyv1alpha1.VeleroStrategyKind, Name: "s"},
					DriverMetadata: map[string]string{veleroBackupNameMetadataKey: "velero-bk"},
				},
			}
			c := clientfake.NewClientBuilder().WithScheme(s).
				WithObjects(append([]client.Object{restoreJob, backup, &strategyv1alpha1.Velero{ObjectMeta: metav1.ObjectMeta{Name: "s"}}}, tc.vb...)...).
				WithStatusSubresource(&backupsv1alpha1.RestoreJob{}).Build()
			hr := makeUnstructuredHelmRelease("vm-disk-data", ns, nil)
			r := &RestoreJobReconciler{Client: c, Interface: dynamicfake.NewSimpleDynamicClient(s, hr), Scheme: s, Recorder: record.NewFakeRecorder(10)}

			rj := &backupsv1alpha1.RestoreJob{}
			if err := c.Get(context.Background(), client.ObjectKey{Namespace: ns, Name: "rj"}, rj); err != nil {
				t.Fatalf("get RestoreJob: %v", err)
			}
			if _, err := r.reconcileVeleroRestore(context.Background(), rj, backup); err != nil {
				t.Fatalf("reconcileVeleroRestore: %v", err)
			}
			if err := c.Get(context.Background(), client.ObjectKey{Namespace: ns, Name: "rj"}, rj); err != nil {
				t.Fatalf("get RestoreJob: %v", err)
			}
			if rj.Status.Phase != backupsv1alpha1.RestoreJobPhaseFailed || !strings.Contains(rj.Status.Message, tc.cause) {
				t.Fatalf("phase %q, message %q; want Failed mentioning %q", rj.Status.Phase, rj.Status.Message, tc.cause)
			}
			got, err := r.Resource(helmReleaseGVR).Namespace(ns).Get(context.Background(), "vm-disk-data", metav1.GetOptions{})
			if err != nil {
				t.Fatalf("get HelmRelease: %v", err)
			}
			if suspended, _, _ := unstructured.NestedBool(got.Object, "spec", "suspend"); suspended {
				t.Fatalf("the disk was prepared although its Velero Backup cannot be restored")
			}
		})
	}
}

// A suspend or halt that failed must stop preparation: renaming the PVC and
// deleting the DataVolume under a running HelmRelease or VM is the race the
// preparation exists to prevent.
func TestPrepareForRestore_StopsWhenSuspendOrHaltFails(t *testing.T) {
	const ns = "tenant-root"
	urRaw, _ := json.Marshal(vmInstanceResources{
		DataVolumes: []backupsv1alpha1.DataVolumeResource{{DataVolumeName: "vm-disk-data", ApplicationName: "data"}},
	})
	ur := &runtime.RawExtension{Raw: urRaw}
	restoreJob := &backupsv1alpha1.RestoreJob{ObjectMeta: metav1.ObjectMeta{Name: "rj", Namespace: ns}}
	pvc := &corev1.PersistentVolumeClaim{ObjectMeta: metav1.ObjectMeta{Name: "vm-disk-data", Namespace: ns}}
	dataVolume := &unstructured.Unstructured{}
	dataVolume.SetAPIVersion("cdi.kubevirt.io/v1beta1")
	dataVolume.SetKind("DataVolume")
	dataVolume.SetName("vm-disk-data")
	dataVolume.SetNamespace(ns)
	vm := &unstructured.Unstructured{}
	vm.SetAPIVersion("kubevirt.io/v1")
	vm.SetKind("VirtualMachine")
	vm.SetName("vm-instance-web")
	vm.SetNamespace(ns)

	for _, tc := range []struct {
		name     string
		kind     string
		app      string
		resource string
	}{
		{name: "the HelmRelease suspend fails", kind: vmDiskAppKind, app: "data", resource: "helmreleases"},
		{name: "the VM halt fails", kind: vmInstanceKind, app: "web", resource: "virtualmachines"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			backup := &backupsv1alpha1.Backup{
				ObjectMeta: metav1.ObjectMeta{Name: "bk", Namespace: ns},
				Spec:       backupsv1alpha1.BackupSpec{ApplicationRef: corev1.TypedLocalObjectReference{APIGroup: new("apps.cozystack.io"), Kind: tc.kind, Name: tc.app}},
			}
			r := newTestRestoreJobReconcilerWithDynamic(t, []runtime.Object{
				makeUnstructuredHelmRelease("vm-disk-data", ns, nil),
				makeUnstructuredHelmRelease("vm-instance-web", ns, nil),
				dataVolume.DeepCopy(), vm.DeepCopy(),
			}, restoreJob.DeepCopy(), backup, pvc.DeepCopy())
			r.Interface.(*dynamicfake.FakeDynamicClient).PrependReactor("update", tc.resource, func(k8stesting.Action) (bool, runtime.Object, error) {
				return true, nil, errors.New("conflict")
			})

			ready, res, err := r.prepareForRestore(context.Background(), restoreJob.DeepCopy(), backup, ur,
				restoreTarget{Namespace: ns, AppName: tc.app, AppKind: tc.kind}, RestoreOptions{KeepOriginalPVC: new(true)})
			if err != nil || ready || res.RequeueAfter == 0 {
				t.Fatalf("prepareForRestore = ready %v, requeue %v, err %v; want a requeue", ready, res.RequeueAfter, err)
			}
			if err := r.Get(context.Background(), client.ObjectKey{Namespace: ns, Name: "vm-disk-data"}, &corev1.PersistentVolumeClaim{}); err != nil {
				t.Errorf("the PVC was renamed: %v", err)
			}
			if _, err := r.Resource(dataVolumeGVR).Namespace(ns).Get(context.Background(), "vm-disk-data", metav1.GetOptions{}); err != nil {
				t.Errorf("the DataVolume was deleted: %v", err)
			}
		})
	}
}

func TestRestoreDisks(t *testing.T) {
	backupOf := func(kind string) *backupsv1alpha1.Backup {
		return &backupsv1alpha1.Backup{Spec: backupsv1alpha1.BackupSpec{ApplicationRef: corev1.TypedLocalObjectReference{Kind: kind, Name: "data"}}}
	}
	if got := restoreDisks(backupOf(vmDiskAppKind), nil); got == nil || len(got.DataVolumes) != 1 || got.DataVolumes[0].DataVolumeName != "vm-disk-data" {
		t.Errorf("VMDisk without underlying resources: %+v, want vm-disk-data", got)
	}
	for _, kind := range []string{vmInstanceKind, "Postgres"} {
		if got := restoreDisks(backupOf(kind), nil); got != nil {
			t.Errorf("%s without underlying resources: %+v, want none; a disk must not be derived for another kind", kind, got)
		}
	}
}

// Kinds other than VMDisk and VMInstance have no disks to prepare and must pass
// through preparation untouched.
func TestPrepareForRestore_OtherKindWithoutDisks(t *testing.T) {
	const ns = "tenant-root"
	backup := &backupsv1alpha1.Backup{
		ObjectMeta: metav1.ObjectMeta{Name: "bk", Namespace: ns},
		Spec:       backupsv1alpha1.BackupSpec{ApplicationRef: corev1.TypedLocalObjectReference{APIGroup: new("apps.cozystack.io"), Kind: "Postgres", Name: "data"}},
	}
	restoreJob := &backupsv1alpha1.RestoreJob{ObjectMeta: metav1.ObjectMeta{Name: "rj", Namespace: ns}}
	r := newTestRestoreJobReconcilerWithDynamic(t, []runtime.Object{makeUnstructuredHelmRelease("vm-disk-data", ns, nil)}, restoreJob.DeepCopy(), backup)
	ready, _, err := r.prepareForRestore(context.Background(), restoreJob.DeepCopy(), backup, nil,
		restoreTarget{Namespace: ns, AppName: "data", AppKind: "Postgres"}, RestoreOptions{KeepOriginalPVC: new(true)})
	if err != nil || !ready {
		t.Fatalf("prepareForRestore = ready %v, err %v; want ready", ready, err)
	}
	got, err := r.Resource(helmReleaseGVR).Namespace(ns).Get(context.Background(), "vm-disk-data", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("get HelmRelease: %v", err)
	}
	if suspended, _, _ := unstructured.NestedBool(got.Object, "spec", "suspend"); suspended {
		t.Fatalf("an unrelated vm-disk-data HelmRelease was suspended")
	}
}

func TestPreparedRestoreLeftovers_VMInstanceNamesItsHelmRelease(t *testing.T) {
	restoreJob := &backupsv1alpha1.RestoreJob{ObjectMeta: metav1.ObjectMeta{Name: "rj"}}
	backup := &backupsv1alpha1.Backup{Spec: backupsv1alpha1.BackupSpec{ApplicationRef: corev1.TypedLocalObjectReference{Kind: vmInstanceKind, Name: "web"}}}
	urRaw, _ := json.Marshal(vmInstanceResources{
		DataVolumes: []backupsv1alpha1.DataVolumeResource{{DataVolumeName: "vm-disk-root", ApplicationName: "root"}},
	})
	got := preparedRestoreLeftovers(restoreJob, backup, &runtime.RawExtension{Raw: urRaw}, restoreTarget{}, RestoreOptions{KeepOriginalPVC: new(true)})
	if !strings.Contains(got, "vm-instance-web, vm-disk-root") {
		t.Errorf("leftovers %q do not name the VM's HelmRelease and its disk's", got)
	}
}

// CDI can recreate an empty PVC under the original name between the rename and
// the DataVolume delete. Velero skips restoring a PVC that exists, so the
// restore would report success with a blank disk; preparation has to clear it.
func TestPrepareForRestore_ClearsPVCRecreatedAfterRename(t *testing.T) {
	f := newKeepPVCFixture(t)
	r := f.reconciler(t, f.livePVC, f.pv)
	ctx := context.Background()
	recreated := &corev1.PersistentVolumeClaim{ObjectMeta: metav1.ObjectMeta{Name: f.livePVC.Name, Namespace: f.ns}}
	recreatedOnce := false
	r.Interface.(*dynamicfake.FakeDynamicClient).PrependReactor("delete", "datavolumes", func(k8stesting.Action) (bool, runtime.Object, error) {
		if !recreatedOnce {
			recreatedOnce = true
			if err := r.Create(ctx, recreated.DeepCopy()); err != nil {
				t.Errorf("recreate PVC: %v", err)
			}
		}
		return false, nil, nil
	})

	ready, res, err := r.prepareForRestore(ctx, f.restoreJob, f.backup, f.ur, f.target, f.opts)
	if err != nil || ready || res.RequeueAfter == 0 {
		t.Fatalf("pass 1: prepareForRestore = ready %v, requeue %v, err %v; want a requeue while the recreated PVC goes", ready, res.RequeueAfter, err)
	}
	if err := r.Get(ctx, client.ObjectKeyFromObject(recreated), &corev1.PersistentVolumeClaim{}); !apierrors.IsNotFound(err) {
		t.Fatalf("the recreated PVC is still there (err %v); Velero would skip restoring it", err)
	}

	ready, _, err = r.prepareForRestore(ctx, f.restoreJob, f.backup, f.ur, f.target, f.opts)
	if err != nil || !ready {
		t.Fatalf("pass 2: prepareForRestore = ready %v, err %v; want ready", ready, err)
	}
	kept := &corev1.PersistentVolumeClaim{}
	if err := r.Get(ctx, client.ObjectKey{Namespace: f.ns, Name: f.origName}, kept); err != nil || kept.Spec.VolumeName != "pv-current" {
		t.Fatalf("the original disk is not kept as %s on pv-current: %+v, err %v", f.origName, kept.Spec, err)
	}
}

// reconcilePreparedVMDisk runs reconcileVeleroRestore for an in-place VMDisk
// restore that has no Velero Restore yet; started, when set, is the time the
// RestoreJob recorded PreparationStarted.
func reconcilePreparedVMDisk(t *testing.T, started *metav1.Time, extra ...client.Object) (*backupsv1alpha1.RestoreJob, *record.FakeRecorder) {
	t.Helper()
	const ns = "tenant-root"
	s := vmDiskTestScheme()
	now := metav1.Now()
	restoreJob := &backupsv1alpha1.RestoreJob{
		ObjectMeta: metav1.ObjectMeta{Name: "rj", Namespace: ns},
		Spec:       backupsv1alpha1.RestoreJobSpec{BackupRef: corev1.LocalObjectReference{Name: "bk"}},
		Status:     backupsv1alpha1.RestoreJobStatus{Phase: backupsv1alpha1.RestoreJobPhaseRunning, StartedAt: &now},
	}
	if started != nil {
		restoreJob.Status.Conditions = []metav1.Condition{{
			Type: restoreCondPreparationStarted, Status: metav1.ConditionTrue, Reason: "Preparing", LastTransitionTime: *started,
		}}
	}
	backup := &backupsv1alpha1.Backup{
		ObjectMeta: metav1.ObjectMeta{Name: "bk", Namespace: ns},
		Spec: backupsv1alpha1.BackupSpec{
			ApplicationRef: corev1.TypedLocalObjectReference{APIGroup: new("apps.cozystack.io"), Kind: vmDiskAppKind, Name: "data"},
			StrategyRef:    corev1.TypedLocalObjectReference{APIGroup: new(strategyv1alpha1.GroupVersion.Group), Kind: strategyv1alpha1.VeleroStrategyKind, Name: "s"},
			DriverMetadata: map[string]string{veleroBackupNameMetadataKey: "velero-bk"},
		},
	}
	c := clientfake.NewClientBuilder().WithScheme(s).
		WithObjects(append([]client.Object{restoreJob, backup, &strategyv1alpha1.Velero{ObjectMeta: metav1.ObjectMeta{Name: "s"}}}, extra...)...).
		WithStatusSubresource(&backupsv1alpha1.RestoreJob{}).Build()
	recorder := record.NewFakeRecorder(20)
	r := &RestoreJobReconciler{Client: c, Interface: dynamicfake.NewSimpleDynamicClient(s, makeUnstructuredHelmRelease("vm-disk-data", ns, nil)), Scheme: s, Recorder: recorder}
	rj := &backupsv1alpha1.RestoreJob{}
	if err := c.Get(context.Background(), client.ObjectKey{Namespace: ns, Name: "rj"}, rj); err != nil {
		t.Fatalf("get RestoreJob: %v", err)
	}
	if _, err := r.reconcileVeleroRestore(context.Background(), rj, backup); err != nil {
		t.Fatalf("reconcileVeleroRestore: %v", err)
	}
	if err := c.Get(context.Background(), client.ObjectKey{Namespace: ns, Name: "rj"}, rj); err != nil {
		t.Fatalf("get RestoreJob: %v", err)
	}
	return rj, recorder
}

func mountingVMDiskPod(name string, deleting bool) *corev1.Pod {
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "tenant-root"},
		Spec: corev1.PodSpec{
			Containers: []corev1.Container{{Name: "compute", Image: "virt-launcher"}},
			Volumes: []corev1.Volume{{Name: "disk", VolumeSource: corev1.VolumeSource{
				PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{ClaimName: "vm-disk-data"},
			}}},
		},
		Status: corev1.PodStatus{Phase: corev1.PodRunning},
	}
	if deleting {
		now := metav1.Now()
		pod.DeletionTimestamp = &now
		pod.Finalizers = []string{"kubernetes"}
	}
	return pod
}

// Preparation spans several passes. A refusal on a later pass must say what
// preparation already left behind, not that nothing was changed.
func TestReconcileVeleroRestore_RefusalAfterPreparationStartedNamesLeftovers(t *testing.T) {
	started := metav1.Now()
	for _, tc := range []struct {
		name  string
		extra []client.Object
		cause string
	}{
		{name: "the Velero Backup expired meanwhile", cause: "no longer exists"},
		{name: "a VM started on the disk meanwhile", extra: []client.Object{completedVeleroBackup("velero-bk"), mountingVMDiskPod("virt-launcher-web", false)}, cause: "in use by pod/virt-launcher-web"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rj, recorder := reconcilePreparedVMDisk(t, &started, tc.extra...)
			if rj.Status.Phase != backupsv1alpha1.RestoreJobPhaseFailed {
				t.Fatalf("Phase = %q, want Failed", rj.Status.Phase)
			}
			for _, want := range []string{tc.cause, "suspended HelmRelease vm-disk-data", "PVC vm-disk-data-orig-" + shortHash("rj")} {
				if !strings.Contains(rj.Status.Message, want) {
					t.Errorf("message %q does not mention %q", rj.Status.Message, want)
				}
			}
			if strings.Contains(rj.Status.Message, "nothing was changed") {
				t.Errorf("message %q claims nothing was changed after preparation started", rj.Status.Message)
			}
			warned := false
			for len(recorder.Events) > 0 {
				if strings.HasPrefix(<-recorder.Events, "Warning RestoreFailed") {
					warned = true
				}
			}
			if !warned {
				t.Error("no Warning RestoreFailed event")
			}
		})
	}
}

// Before preparation starts, a refusal really has changed nothing and must
// leave PreparationStarted unset.
func TestReconcileVeleroRestore_RefusalBeforePreparationChangesNothing(t *testing.T) {
	rj, _ := reconcilePreparedVMDisk(t, nil, completedVeleroBackup("velero-bk"), mountingVMDiskPod("virt-launcher-web", false))
	if rj.Status.Phase != backupsv1alpha1.RestoreJobPhaseFailed || !strings.Contains(rj.Status.Message, "nothing was changed") {
		t.Fatalf("phase %q, message %q; want Failed saying nothing was changed", rj.Status.Phase, rj.Status.Message)
	}
	if meta.IsStatusConditionTrue(rj.Status.Conditions, restoreCondPreparationStarted) {
		t.Error("PreparationStarted was recorded although the restore was refused first")
	}
}

func TestReconcileVeleroRestore_PreparationDeadline(t *testing.T) {
	waiting := []client.Object{completedVeleroBackup("velero-bk"), mountingVMDiskPod("virt-launcher-web", true)}

	t.Run("a preparation still waiting past the deadline fails and names the leftovers", func(t *testing.T) {
		long := metav1.NewTime(time.Now().Add(-restorePreparationDeadline - time.Minute))
		rj, _ := reconcilePreparedVMDisk(t, &long, waiting...)
		if rj.Status.Phase != backupsv1alpha1.RestoreJobPhaseFailed || !strings.Contains(rj.Status.Message, "did not finish within") ||
			!strings.Contains(rj.Status.Message, "suspended HelmRelease vm-disk-data") {
			t.Fatalf("phase %q, message %q; want Failed past the deadline, naming the leftovers", rj.Status.Phase, rj.Status.Message)
		}
	})

	t.Run("a preparation waiting within the deadline keeps running", func(t *testing.T) {
		recent := metav1.Now()
		rj, _ := reconcilePreparedVMDisk(t, &recent, waiting...)
		if rj.Status.Phase != backupsv1alpha1.RestoreJobPhaseRunning {
			t.Fatalf("Phase = %q, message %q; want Running", rj.Status.Phase, rj.Status.Message)
		}
	})
}

func TestPrepareForRestore_RecordsPreparationStarted(t *testing.T) {
	f := newKeepPVCFixture(t)
	r := f.reconciler(t, f.livePVC, f.pv)
	if ready, _, err := r.prepareForRestore(context.Background(), f.restoreJob, f.backup, f.ur, f.target, f.opts); err != nil || !ready {
		t.Fatalf("prepareForRestore = ready %v, err %v; want ready", ready, err)
	}
	got := &backupsv1alpha1.RestoreJob{}
	if err := r.Get(context.Background(), client.ObjectKeyFromObject(f.restoreJob), got); err != nil {
		t.Fatal(err)
	}
	if !meta.IsStatusConditionTrue(got.Status.Conditions, restoreCondPreparationStarted) {
		t.Errorf("PreparationStarted not recorded: %+v", got.Status.Conditions)
	}
}

// Step 5 waits for the DataVolume to be gone: while it exists, CDI can create
// the PVC again after Step 5 cleared it.
func TestPrepareForRestore_WaitsForDataVolumeGone(t *testing.T) {
	f := newKeepPVCFixture(t)
	r := f.reconciler(t, f.livePVC, f.pv)
	r.Interface.(*dynamicfake.FakeDynamicClient).PrependReactor("delete", "datavolumes", func(k8stesting.Action) (bool, runtime.Object, error) {
		return true, nil, nil
	})
	ready, res, err := r.prepareForRestore(context.Background(), f.restoreJob, f.backup, f.ur, f.target, f.opts)
	if err != nil || ready || res.RequeueAfter == 0 {
		t.Fatalf("prepareForRestore = ready %v, requeue %v, err %v; want a requeue while the DataVolume exists", ready, res.RequeueAfter, err)
	}
}
