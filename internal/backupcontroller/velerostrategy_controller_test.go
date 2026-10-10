package backupcontroller

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
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
	velerov1 "github.com/vmware-tanzu/velero/pkg/apis/velero/v1"
)

// mockRESTMapper implements meta.RESTMapper for testing
type mockRESTMapper struct {
	mapping *meta.RESTMapping
}

func (m *mockRESTMapper) RESTMapping(gk schema.GroupKind, versions ...string) (*meta.RESTMapping, error) {
	return m.mapping, nil
}

func (m *mockRESTMapper) RESTMappings(gk schema.GroupKind, versions ...string) ([]*meta.RESTMapping, error) {
	return []*meta.RESTMapping{m.mapping}, nil
}

func (m *mockRESTMapper) KindFor(resource schema.GroupVersionResource) (schema.GroupVersionKind, error) {
	return m.mapping.GroupVersionKind, nil
}

func (m *mockRESTMapper) KindsFor(resource schema.GroupVersionResource) ([]schema.GroupVersionKind, error) {
	return []schema.GroupVersionKind{m.mapping.GroupVersionKind}, nil
}

func (m *mockRESTMapper) ResourceFor(input schema.GroupVersionResource) (schema.GroupVersionResource, error) {
	return m.mapping.Resource, nil
}

func (m *mockRESTMapper) ResourcesFor(input schema.GroupVersionResource) ([]schema.GroupVersionResource, error) {
	return []schema.GroupVersionResource{m.mapping.Resource}, nil
}

func (m *mockRESTMapper) ResourceSingularizer(resource string) (singular string, err error) {
	return resource, nil
}

func TestCreateVeleroBackup_TemplateContext(t *testing.T) {
	// Setup scheme
	testScheme := runtime.NewScheme()
	_ = scheme.AddToScheme(testScheme)
	_ = backupsv1alpha1.AddToScheme(testScheme)
	_ = strategyv1alpha1.AddToScheme(testScheme)
	_ = velerov1.AddToScheme(testScheme)

	// Create test application (VirtualMachine-like object)
	testApp := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-vm",
			Namespace: "default",
			Labels: map[string]string{
				"apps.cozystack.io/application.Kind": "VirtualMachine",
			},
		},
		Spec: corev1.PodSpec{
			Containers: []corev1.Container{
				{
					Name:  "test-container",
					Image: "test-image:latest",
				},
			},
		},
	}

	// Create dynamic client with the test application
	dynamicClient := dynamicfake.NewSimpleDynamicClient(testScheme, testApp)

	// Create REST mapping
	mapping := &meta.RESTMapping{
		Resource: schema.GroupVersionResource{
			Group:    "",
			Version:  "v1",
			Resource: "pods",
		},
		GroupVersionKind: schema.GroupVersionKind{
			Group:   "",
			Version: "v1",
			Kind:    "Pod",
		},
		Scope: meta.RESTScopeNamespace,
	}

	// Create BackupJob
	backupJob := &backupsv1alpha1.BackupJob{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-backup-job",
			Namespace: "default",
		},
		Spec: backupsv1alpha1.BackupJobSpec{
			ApplicationRef: corev1.TypedLocalObjectReference{
				APIGroup: new(""),
				Kind:     "Pod",
				Name:     "test-vm",
			},
			BackupClassName: "velero",
		},
	}

	// Create Velero strategy with template that uses Application and Parameters
	veleroStrategy := &strategyv1alpha1.Velero{
		ObjectMeta: metav1.ObjectMeta{
			Name: "velero-strategy",
		},
		Spec: strategyv1alpha1.VeleroSpec{
			Template: strategyv1alpha1.VeleroTemplate{
				Spec: velerov1.BackupSpec{
					// Use template variables to verify context is passed correctly
					LabelSelector: &metav1.LabelSelector{
						MatchLabels: map[string]string{
							"app": "{{ .Application.metadata.name }}",
						},
					},
					// Use Parameters in template
					StorageLocation: "{{ .Parameters.backupStorageLocationName }}",
				},
			},
		},
	}

	// Create ResolvedBackupConfig with parameters
	resolved := &ResolvedBackupConfig{
		StrategyRef: corev1.TypedLocalObjectReference{
			APIGroup: new("strategy.backups.cozystack.io"),
			Kind:     "Velero",
			Name:     "velero-strategy",
		},
		Parameters: map[string]string{
			"backupStorageLocationName": "default-storage",
		},
	}

	// Create fake client for controller
	fakeClient := clientfake.NewClientBuilder().
		WithScheme(testScheme).
		WithObjects(backupJob, veleroStrategy).
		Build()

	// Create reconciler with fake event recorder
	reconciler := &BackupJobReconciler{
		Client:     fakeClient,
		Interface:  dynamicClient,
		RESTMapper: &mockRESTMapper{mapping: mapping},
		Scheme:     testScheme,
		Recorder:   record.NewFakeRecorder(10),
	}

	// Create context with logger
	ctx := context.Background()

	// Call createVeleroBackup
	err := reconciler.createVeleroBackup(ctx, backupJob, veleroStrategy, resolved)
	if err != nil {
		t.Fatalf("createVeleroBackup() error = %v", err)
	}

	// Verify that the template was executed correctly by checking the created Velero Backup
	// The template should have replaced {{ .Application.metadata.name }} with "test-vm"
	// and {{ .Parameters.backupStorageLocationName }} with "default-storage"

	// Get the created Velero Backup
	veleroBackups := &velerov1.BackupList{}
	err = fakeClient.List(ctx, veleroBackups, client.InNamespace(veleroNamespace))
	if err != nil {
		t.Fatalf("Failed to list Velero Backups: %v", err)
	}

	if len(veleroBackups.Items) == 0 {
		t.Fatal("Expected Velero Backup to be created, but none found")
	}

	veleroBackup := veleroBackups.Items[0]

	// Verify template context was used correctly:
	// 1. Application.metadata.name should be replaced with "test-vm"
	if veleroBackup.Spec.LabelSelector == nil {
		t.Fatal("Expected LabelSelector to be set by template")
	}
	if appLabel, ok := veleroBackup.Spec.LabelSelector.MatchLabels["app"]; ok {
		if appLabel != "test-vm" {
			t.Errorf("Template context Application.metadata.name not applied correctly. Expected 'test-vm', got '%s'", appLabel)
		}
	} else {
		t.Error("Template context Application.metadata.name not found in label selector")
	}

	// 2. Parameters.backupStorageLocationName should be replaced with "default-storage"
	if veleroBackup.Spec.StorageLocation != "default-storage" {
		t.Errorf("Template context Parameters.backupStorageLocationName not applied correctly. Expected 'default-storage', got '%s'", veleroBackup.Spec.StorageLocation)
	}
}

func TestResolveRestoreTarget_NoTargetNamespace_InPlace(t *testing.T) {
	restoreJob := &backupsv1alpha1.RestoreJob{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "restore-test",
			Namespace: "tenant-root",
		},
		Spec: backupsv1alpha1.RestoreJobSpec{
			BackupRef: corev1.LocalObjectReference{Name: "my-backup"},
			TargetApplicationRef: &corev1.TypedLocalObjectReference{
				APIGroup: new("apps.cozystack.io"),
				Kind:     "VMInstance",
				Name:     "test-vm",
			},
		},
	}

	backup := &backupsv1alpha1.Backup{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "my-backup",
			Namespace: "tenant-root",
		},
		Spec: backupsv1alpha1.BackupSpec{
			ApplicationRef: corev1.TypedLocalObjectReference{
				APIGroup: new("apps.cozystack.io"),
				Kind:     "VMInstance",
				Name:     "test-vm",
			},
		},
	}

	// No targetNamespace in options → in-place restore
	opts := RestoreOptions{}
	target := resolveRestoreTarget(restoreJob, backup, opts)

	if target.IsCopy {
		t.Error("expected IsCopy=false when targetNamespace is omitted, got true")
	}
	if target.Namespace != "tenant-root" {
		t.Errorf("expected namespace 'tenant-root', got '%s'", target.Namespace)
	}
	if target.AppName != "test-vm" {
		t.Errorf("expected appName 'test-vm', got '%s'", target.AppName)
	}
	if target.AppKind != "VMInstance" {
		t.Errorf("expected appKind 'VMInstance', got '%s'", target.AppKind)
	}
}

func TestResolveRestoreTarget_SameTargetNamespace_InPlace(t *testing.T) {
	restoreJob := &backupsv1alpha1.RestoreJob{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "restore-test",
			Namespace: "tenant-root",
		},
		Spec: backupsv1alpha1.RestoreJobSpec{
			BackupRef: corev1.LocalObjectReference{Name: "my-backup"},
		},
	}

	backup := &backupsv1alpha1.Backup{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "my-backup",
			Namespace: "tenant-root",
		},
		Spec: backupsv1alpha1.BackupSpec{
			ApplicationRef: corev1.TypedLocalObjectReference{
				APIGroup: new("apps.cozystack.io"),
				Kind:     "VMInstance",
				Name:     "test-vm",
			},
		},
	}

	// targetNamespace equals backup namespace → still in-place
	opts := RestoreOptions{
		CommonRestoreOptions: CommonRestoreOptions{
			TargetNamespace: "tenant-root",
		},
	}
	target := resolveRestoreTarget(restoreJob, backup, opts)

	if target.IsCopy {
		t.Error("expected IsCopy=false when targetNamespace equals backup namespace, got true")
	}
	if target.Namespace != "tenant-root" {
		t.Errorf("expected namespace 'tenant-root', got '%s'", target.Namespace)
	}
}

func TestResolveRestoreTarget_DifferentTargetNamespace_Copy(t *testing.T) {
	restoreJob := &backupsv1alpha1.RestoreJob{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "restore-test",
			Namespace: "tenant-root",
		},
		Spec: backupsv1alpha1.RestoreJobSpec{
			BackupRef: corev1.LocalObjectReference{Name: "my-backup"},
		},
	}

	backup := &backupsv1alpha1.Backup{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "my-backup",
			Namespace: "tenant-root",
		},
		Spec: backupsv1alpha1.BackupSpec{
			ApplicationRef: corev1.TypedLocalObjectReference{
				APIGroup: new("apps.cozystack.io"),
				Kind:     "VMInstance",
				Name:     "test-vm",
			},
		},
	}

	opts := RestoreOptions{
		CommonRestoreOptions: CommonRestoreOptions{
			TargetNamespace: "tenant-copy",
		},
	}
	target := resolveRestoreTarget(restoreJob, backup, opts)

	if !target.IsCopy {
		t.Error("expected IsCopy=true when targetNamespace differs from backup namespace, got false")
	}
	if target.Namespace != "tenant-copy" {
		t.Errorf("expected namespace 'tenant-copy', got '%s'", target.Namespace)
	}
}

// newTestRestoreJobReconcilerWithDynamic builds a RestoreJobReconciler with
// both static and dynamic fake clients. Use dynamicObjects to pre-populate
// unstructured resources (e.g. HelmReleases).
func newTestRestoreJobReconcilerWithDynamic(t *testing.T, dynamicObjects []runtime.Object, objects ...client.Object) *RestoreJobReconciler {
	t.Helper()
	testScheme := runtime.NewScheme()
	_ = scheme.AddToScheme(testScheme)
	_ = backupsv1alpha1.AddToScheme(testScheme)
	_ = velerov1.AddToScheme(testScheme)

	fakeClient := clientfake.NewClientBuilder().
		WithScheme(testScheme).
		WithObjects(objects...).
		Build()

	dynamicClient := dynamicfake.NewSimpleDynamicClient(testScheme, dynamicObjects...)

	mapping := &meta.RESTMapping{
		Resource:         schema.GroupVersionResource{Group: "", Version: "v1", Resource: "pods"},
		GroupVersionKind: schema.GroupVersionKind{Group: "", Version: "v1", Kind: "Pod"},
		Scope:            meta.RESTScopeNamespace,
	}

	return &RestoreJobReconciler{
		Client:     fakeClient,
		Interface:  dynamicClient,
		RESTMapper: &mockRESTMapper{mapping: mapping},
		Scheme:     testScheme,
		Recorder:   record.NewFakeRecorder(100),
	}
}

// newTestRestoreJobReconciler builds a RestoreJobReconciler with fake clients for testing.
func newTestRestoreJobReconciler(t *testing.T, objects ...client.Object) *RestoreJobReconciler {
	t.Helper()
	testScheme := runtime.NewScheme()
	_ = scheme.AddToScheme(testScheme)
	_ = backupsv1alpha1.AddToScheme(testScheme)
	_ = velerov1.AddToScheme(testScheme)

	fakeClient := clientfake.NewClientBuilder().
		WithScheme(testScheme).
		WithObjects(objects...).
		Build()

	dynamicClient := dynamicfake.NewSimpleDynamicClient(testScheme)

	mapping := &meta.RESTMapping{
		Resource:         schema.GroupVersionResource{Group: "", Version: "v1", Resource: "pods"},
		GroupVersionKind: schema.GroupVersionKind{Group: "", Version: "v1", Kind: "Pod"},
		Scope:            meta.RESTScopeNamespace,
	}

	return &RestoreJobReconciler{
		Client:     fakeClient,
		Interface:  dynamicClient,
		RESTMapper: &mockRESTMapper{mapping: mapping},
		Scheme:     testScheme,
		Recorder:   record.NewFakeRecorder(100),
	}
}

// inPlaceVMRestoreWithoutKeep is an in-place VMInstance restore of one disk,
// vm-disk-ubuntu-source, with keepOriginalPVC=false.
func inPlaceVMRestoreWithoutKeep(ns string) (*backupsv1alpha1.RestoreJob, *backupsv1alpha1.Backup, *runtime.RawExtension, restoreTarget, RestoreOptions) {
	restoreJob := &backupsv1alpha1.RestoreJob{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "restore-test",
			Namespace: ns,
		},
		Spec: backupsv1alpha1.RestoreJobSpec{
			BackupRef: corev1.LocalObjectReference{Name: "my-backup"},
		},
	}

	backup := &backupsv1alpha1.Backup{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "my-backup",
			Namespace: ns,
		},
		Spec: backupsv1alpha1.BackupSpec{
			ApplicationRef: corev1.TypedLocalObjectReference{
				APIGroup: new("apps.cozystack.io"),
				Kind:     "VMInstance",
				Name:     "test-vm",
			},
		},
	}

	urData := vmInstanceResources{
		DataVolumes: []backupsv1alpha1.DataVolumeResource{
			{DataVolumeName: "vm-disk-ubuntu-source", ApplicationName: "ubuntu-source"},
		},
	}
	urRaw, _ := json.Marshal(urData)

	target := restoreTarget{
		Namespace: ns,
		AppName:   "test-vm",
		AppKind:   "VMInstance",
		IsCopy:    false,
	}

	return restoreJob, backup, &runtime.RawExtension{Raw: urRaw}, target, RestoreOptions{KeepOriginalPVC: new(false)}
}

// Velero only restores a PVC it creates: one still in place keeps its old data
// and blocks the DataVolume the HelmRelease recreates.
func TestPrepareForRestore_KeepOriginalPVCFalse_DeletesPVC(t *testing.T) {
	ns := "tenant-root"
	restoreJob, backup, ur, target, opts := inPlaceVMRestoreWithoutKeep(ns)

	pvc := &corev1.PersistentVolumeClaim{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "vm-disk-ubuntu-source",
			Namespace: ns,
		},
		Spec: corev1.PersistentVolumeClaimSpec{},
	}

	reconciler := newTestRestoreJobReconciler(t, pvc, restoreJob, backup)

	ctx := context.Background()
	ready, _, err := reconciler.prepareForRestore(ctx, restoreJob, backup, ur, target, opts)
	if err != nil {
		t.Fatalf("prepareForRestore() error = %v", err)
	}
	if !ready {
		t.Fatal("expected ready=true, got false")
	}

	err = reconciler.Get(ctx, client.ObjectKey{Namespace: ns, Name: "vm-disk-ubuntu-source"}, &corev1.PersistentVolumeClaim{})
	if !errors.IsNotFound(err) {
		t.Errorf("original PVC should be gone before Velero restores it when keepOriginalPVC=false, got error: %v", err)
	}

	origSuffix := "-orig-" + shortHash(restoreJob.Name)
	err = reconciler.Get(ctx, client.ObjectKey{Namespace: ns, Name: "vm-disk-ubuntu-source" + origSuffix}, &corev1.PersistentVolumeClaim{})
	if err == nil {
		t.Error("PVC should NOT have been renamed when keepOriginalPVC=false, but found renamed PVC")
	}
}

func TestPrepareForRestore_KeepOriginalPVCFalse_WaitsForTerminatingPVC(t *testing.T) {
	ns := "tenant-root"
	restoreJob, backup, ur, target, opts := inPlaceVMRestoreWithoutKeep(ns)

	pvc := &corev1.PersistentVolumeClaim{
		ObjectMeta: metav1.ObjectMeta{
			Name:       "vm-disk-ubuntu-source",
			Namespace:  ns,
			Finalizers: []string{"kubernetes.io/pvc-protection"},
		},
		Spec: corev1.PersistentVolumeClaimSpec{},
	}

	reconciler := newTestRestoreJobReconciler(t, pvc, restoreJob, backup)

	ctx := context.Background()
	ready, result, err := reconciler.prepareForRestore(ctx, restoreJob, backup, ur, target, opts)
	if err != nil {
		t.Fatalf("prepareForRestore() error = %v", err)
	}
	if ready {
		t.Fatal("expected ready=false while the PVC is still terminating, got true")
	}
	if result.RequeueAfter == 0 {
		t.Error("expected a requeue while the PVC is still terminating")
	}

	stuck := &corev1.PersistentVolumeClaim{}
	if err := reconciler.Get(ctx, client.ObjectKey{Namespace: ns, Name: "vm-disk-ubuntu-source"}, stuck); err != nil {
		t.Fatalf("terminating PVC should still be readable: %v", err)
	}
	if stuck.DeletionTimestamp == nil {
		t.Fatal("PVC should have been marked for deletion")
	}
	stuck.Finalizers = nil
	if err := reconciler.Update(ctx, stuck); err != nil {
		t.Fatalf("failed to release the PVC finalizer: %v", err)
	}

	ready, _, err = reconciler.prepareForRestore(ctx, restoreJob, backup, ur, target, opts)
	if err != nil {
		t.Fatalf("prepareForRestore() error = %v", err)
	}
	if !ready {
		t.Fatal("expected ready=true once the PVC is gone, got false")
	}
}

func makeUnstructuredDataVolume(name, namespace string) *unstructured.Unstructured {
	dv := &unstructured.Unstructured{}
	dv.SetAPIVersion("cdi.kubevirt.io/v1beta1")
	dv.SetKind("DataVolume")
	dv.SetName(name)
	dv.SetNamespace(namespace)
	return dv
}

// A DataVolume still in place makes CDI recreate the claim under its original
// name, so the PVC must survive until the DataVolume is confirmed gone.
func TestPrepareForRestore_KeepOriginalPVCFalse_KeepsPVCWhileDataVolumeRemains(t *testing.T) {
	tests := []struct {
		name      string
		deleteErr error
		wantErr   bool
	}{
		{
			name:      "DataVolume deletion fails",
			deleteErr: errors.NewForbidden(dataVolumeGVR.GroupResource(), "vm-disk-ubuntu-source", nil),
			wantErr:   true,
		},
		{
			name: "DataVolume deletion is still pending",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ns := "tenant-root"
			restoreJob, backup, ur, target, opts := inPlaceVMRestoreWithoutKeep(ns)
			pvc := &corev1.PersistentVolumeClaim{
				ObjectMeta: metav1.ObjectMeta{Name: "vm-disk-ubuntu-source", Namespace: ns},
			}
			reconciler := newTestRestoreJobReconcilerWithDynamic(t,
				[]runtime.Object{makeUnstructuredDataVolume("vm-disk-ubuntu-source", ns)},
				pvc, restoreJob, backup)
			reconciler.Interface.(*dynamicfake.FakeDynamicClient).PrependReactor("delete", "datavolumes",
				func(k8stesting.Action) (bool, runtime.Object, error) {
					return true, nil, tt.deleteErr
				})

			ctx := context.Background()
			ready, result, err := reconciler.prepareForRestore(ctx, restoreJob, backup, ur, target, opts)
			if tt.wantErr {
				if err == nil {
					t.Fatal("expected an error when the DataVolume cannot be deleted")
				}
			} else {
				if err != nil {
					t.Fatalf("prepareForRestore() error = %v", err)
				}
				if result.RequeueAfter == 0 {
					t.Error("expected a requeue while the DataVolume is still present")
				}
			}
			if ready {
				t.Fatal("expected ready=false while the DataVolume is still present")
			}

			got := &corev1.PersistentVolumeClaim{}
			if err := reconciler.Get(ctx, client.ObjectKeyFromObject(pvc), got); err != nil {
				t.Fatalf("original PVC should survive while its DataVolume remains: %v", err)
			}
			if got.DeletionTimestamp != nil {
				t.Error("original PVC should not be marked for deletion while its DataVolume remains")
			}
		})
	}
}

// keepOriginalPVC=true must never delete the original claim, even when the
// rename that preserves it fails and leaves it under its original name.
func TestPrepareForRestore_KeepOriginalPVCTrue_FailedRenameKeepsPVC(t *testing.T) {
	ns := "tenant-root"
	restoreJob, backup, ur, target, _ := inPlaceVMRestoreWithoutKeep(ns)
	opts := RestoreOptions{KeepOriginalPVC: new(true)}

	pvc := &corev1.PersistentVolumeClaim{
		ObjectMeta: metav1.ObjectMeta{Name: "vm-disk-ubuntu-source", Namespace: ns},
		// The PV does not exist, so renamePVC fails before touching the claim.
		Spec: corev1.PersistentVolumeClaimSpec{VolumeName: "pv-missing"},
	}
	reconciler := newTestRestoreJobReconciler(t, pvc, restoreJob, backup)

	ctx := context.Background()
	if _, _, err := reconciler.prepareForRestore(ctx, restoreJob, backup, ur, target, opts); err != nil {
		t.Fatalf("prepareForRestore() error = %v", err)
	}

	if err := reconciler.Get(ctx, client.ObjectKeyFromObject(pvc), &corev1.PersistentVolumeClaim{}); err != nil {
		t.Errorf("original PVC should survive a failed rename when keepOriginalPVC=true, got error: %v", err)
	}
}

// A Backup artifact can outlive its Velero Backup, which Velero garbage-collects
// at TTL. The restore must fail before it halts the VM or deletes its disks.
func TestReconcileVeleroRestore_UnusableVeleroBackupTouchesNothing(t *testing.T) {
	tests := []struct {
		name         string
		veleroBackup *velerov1.Backup
		wantMessage  string
	}{
		{
			name:        "Velero Backup expired",
			wantMessage: "Velero Backup cozy-velero/vb not found",
		},
		{
			name: "Velero Backup being deleted",
			veleroBackup: &velerov1.Backup{
				ObjectMeta: metav1.ObjectMeta{Name: "vb", Namespace: veleroNamespace},
				Status:     velerov1.BackupStatus{Phase: velerov1.BackupPhaseDeleting},
			},
			wantMessage: `Velero Backup cozy-velero/vb is "Deleting"`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ns := "tenant-root"
			restoreJob, backup, ur, _, opts := inPlaceVMRestoreWithoutKeep(ns)
			rawOpts, err := json.Marshal(opts)
			if err != nil {
				t.Fatalf("marshal options: %v", err)
			}
			started := metav1.Now()
			restoreJob.Spec.Options = &runtime.RawExtension{Raw: rawOpts}
			restoreJob.Status = backupsv1alpha1.RestoreJobStatus{
				Phase:     backupsv1alpha1.RestoreJobPhaseRunning,
				StartedAt: &started,
			}
			backup.Spec.StrategyRef = corev1.TypedLocalObjectReference{Kind: "Velero", Name: "cozy-default-velero"}
			backup.Spec.DriverMetadata = map[string]string{veleroBackupNameMetadataKey: "vb"}
			backup.Status.UnderlyingResources = ur

			pvc := &corev1.PersistentVolumeClaim{
				ObjectMeta: metav1.ObjectMeta{Name: "vm-disk-ubuntu-source", Namespace: ns},
			}
			strategy := &strategyv1alpha1.Velero{ObjectMeta: metav1.ObjectMeta{Name: "cozy-default-velero"}}
			objs := []client.Object{restoreJob, backup, pvc, strategy}
			if tt.veleroBackup != nil {
				objs = append(objs, tt.veleroBackup)
			}

			vm := &unstructured.Unstructured{}
			vm.SetAPIVersion("kubevirt.io/v1")
			vm.SetKind("VirtualMachine")
			vm.SetName(vmNamePrefix + "test-vm")
			vm.SetNamespace(ns)
			if err := unstructured.SetNestedField(vm.Object, "Always", "spec", "runStrategy"); err != nil {
				t.Fatalf("set runStrategy: %v", err)
			}
			reconciler := newTestRestoreJobReconcilerWithDynamic(t,
				[]runtime.Object{vm, makeUnstructuredDataVolume("vm-disk-ubuntu-source", ns)})
			testScheme := runtime.NewScheme()
			_ = scheme.AddToScheme(testScheme)
			_ = backupsv1alpha1.AddToScheme(testScheme)
			_ = velerov1.AddToScheme(testScheme)
			_ = strategyv1alpha1.AddToScheme(testScheme)
			reconciler.Client = clientfake.NewClientBuilder().
				WithScheme(testScheme).
				WithObjects(objs...).
				WithStatusSubresource(&backupsv1alpha1.RestoreJob{}).
				Build()
			ctx := context.Background()

			if _, err := reconciler.reconcileVeleroRestore(ctx, restoreJob, backup); err != nil {
				t.Fatalf("reconcileVeleroRestore() error = %v", err)
			}

			got := &backupsv1alpha1.RestoreJob{}
			if err := reconciler.Get(ctx, client.ObjectKeyFromObject(restoreJob), got); err != nil {
				t.Fatalf("get RestoreJob: %v", err)
			}
			if got.Status.Phase != backupsv1alpha1.RestoreJobPhaseFailed {
				t.Fatalf("phase = %q, want %q", got.Status.Phase, backupsv1alpha1.RestoreJobPhaseFailed)
			}
			ready := meta.FindStatusCondition(got.Status.Conditions, "Ready")
			if ready == nil || !strings.Contains(ready.Message, tt.wantMessage) {
				t.Errorf("Ready condition = %+v, want a message containing %q", ready, tt.wantMessage)
			}

			if err := reconciler.Get(ctx, client.ObjectKeyFromObject(pvc), &corev1.PersistentVolumeClaim{}); err != nil {
				t.Errorf("original PVC should survive a restore that cannot run: %v", err)
			}
			if _, err := reconciler.Resource(dataVolumeGVR).Namespace(ns).Get(ctx, "vm-disk-ubuntu-source", metav1.GetOptions{}); err != nil {
				t.Errorf("DataVolume should survive a restore that cannot run: %v", err)
			}
			gotVM, err := reconciler.Resource(virtualMachineGVR).Namespace(ns).Get(ctx, vm.GetName(), metav1.GetOptions{})
			if err != nil {
				t.Fatalf("get VirtualMachine: %v", err)
			}
			if rs, _, _ := unstructured.NestedString(gotVM.Object, "spec", "runStrategy"); rs != "Always" {
				t.Errorf("VirtualMachine runStrategy = %q, want it left at %q", rs, "Always")
			}
		})
	}
}

func TestResolveRestoreTarget_VMDisk_CommonRestoreOptions(t *testing.T) {
	tests := []struct {
		name          string
		targetNS      string
		wantIsCopy    bool
		wantNamespace string
	}{
		{
			name:          "VMDisk in-place restore when targetNamespace is omitted",
			targetNS:      "",
			wantIsCopy:    false,
			wantNamespace: "tenant-root",
		},
		{
			name:          "VMDisk cross-namespace restore when targetNamespace differs",
			targetNS:      "tenant-copy",
			wantIsCopy:    true,
			wantNamespace: "tenant-copy",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			restoreJob := &backupsv1alpha1.RestoreJob{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "restore-vmdisk",
					Namespace: "tenant-root",
				},
				Spec: backupsv1alpha1.RestoreJobSpec{
					BackupRef: corev1.LocalObjectReference{Name: "disk-backup"},
					TargetApplicationRef: &corev1.TypedLocalObjectReference{
						APIGroup: new("apps.cozystack.io"),
						Kind:     "VMDisk",
						Name:     "ubuntu-source",
					},
				},
			}

			backup := &backupsv1alpha1.Backup{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "disk-backup",
					Namespace: "tenant-root",
				},
				Spec: backupsv1alpha1.BackupSpec{
					ApplicationRef: corev1.TypedLocalObjectReference{
						APIGroup: new("apps.cozystack.io"),
						Kind:     "VMDisk",
						Name:     "ubuntu-source",
					},
				},
			}

			// VMDisk uses only CommonRestoreOptions (no VMI-specific fields)
			opts := RestoreOptions{
				CommonRestoreOptions: CommonRestoreOptions{
					TargetNamespace: tt.targetNS,
				},
			}

			target := resolveRestoreTarget(restoreJob, backup, opts)

			if target.IsCopy != tt.wantIsCopy {
				t.Errorf("IsCopy = %v, want %v", target.IsCopy, tt.wantIsCopy)
			}
			if target.Namespace != tt.wantNamespace {
				t.Errorf("Namespace = %q, want %q", target.Namespace, tt.wantNamespace)
			}
			if target.AppKind != "VMDisk" {
				t.Errorf("AppKind = %q, want 'VMDisk'", target.AppKind)
			}
			if target.AppName != "ubuntu-source" {
				t.Errorf("AppName = %q, want 'ubuntu-source'", target.AppName)
			}
		})
	}
}

func TestParseRestoreOptions_VMDiskOnlyCommonFields(t *testing.T) {
	// Simulate a VMDisk restore where only CommonRestoreOptions fields are set
	raw, _ := json.Marshal(map[string]any{
		"targetNamespace":    "tenant-copy",
		"failIfTargetExists": true,
	})
	ext := &runtime.RawExtension{Raw: raw}

	opts, err := parseRestoreOptions(ext)
	if err != nil {
		t.Fatalf("parseRestoreOptions() error = %v", err)
	}

	if opts.TargetNamespace != "tenant-copy" {
		t.Errorf("TargetNamespace = %q, want 'tenant-copy'", opts.TargetNamespace)
	}
	if !opts.GetFailIfTargetExists() {
		t.Error("GetFailIfTargetExists() = false, want true")
	}
	// VMI-specific fields should be nil (not set by VMDisk options)
	if opts.KeepOriginalPVC != nil {
		t.Errorf("KeepOriginalPVC should be nil for VMDisk options, got %v", *opts.KeepOriginalPVC)
	}
	if opts.KeepOriginalIpAndMac != nil {
		t.Errorf("KeepOriginalIpAndMac should be nil for VMDisk options, got %v", *opts.KeepOriginalIpAndMac)
	}
}

func TestRestoreOptions_Defaults(t *testing.T) {
	t.Run("nil options default all bools to true", func(t *testing.T) {
		opts, err := parseRestoreOptions(nil)
		if err != nil {
			t.Fatalf("parseRestoreOptions(nil) error = %v", err)
		}

		if opts.TargetNamespace != "" {
			t.Errorf("TargetNamespace default should be empty, got %q", opts.TargetNamespace)
		}
		if !opts.GetFailIfTargetExists() {
			t.Error("GetFailIfTargetExists() should default to true")
		}
		if !opts.GetKeepOriginalPVC() {
			t.Error("GetKeepOriginalPVC() should default to true")
		}
		if !opts.GetKeepOriginalIpAndMac() {
			t.Error("GetKeepOriginalIpAndMac() should default to true")
		}
	})

	t.Run("empty JSON defaults all bools to true", func(t *testing.T) {
		raw, _ := json.Marshal(map[string]any{})
		opts, err := parseRestoreOptions(&runtime.RawExtension{Raw: raw})
		if err != nil {
			t.Fatalf("parseRestoreOptions({}) error = %v", err)
		}

		if !opts.GetFailIfTargetExists() {
			t.Error("GetFailIfTargetExists() should default to true")
		}
		if !opts.GetKeepOriginalPVC() {
			t.Error("GetKeepOriginalPVC() should default to true")
		}
		if !opts.GetKeepOriginalIpAndMac() {
			t.Error("GetKeepOriginalIpAndMac() should default to true")
		}
	})

	t.Run("explicit false overrides defaults", func(t *testing.T) {
		raw, _ := json.Marshal(map[string]any{
			"failIfTargetExists":   false,
			"keepOriginalPVC":      false,
			"keepOriginalIpAndMac": false,
		})
		opts, err := parseRestoreOptions(&runtime.RawExtension{Raw: raw})
		if err != nil {
			t.Fatalf("parseRestoreOptions() error = %v", err)
		}

		if opts.GetFailIfTargetExists() {
			t.Error("GetFailIfTargetExists() should be false when explicitly set")
		}
		if opts.GetKeepOriginalPVC() {
			t.Error("GetKeepOriginalPVC() should be false when explicitly set")
		}
		if opts.GetKeepOriginalIpAndMac() {
			t.Error("GetKeepOriginalIpAndMac() should be false when explicitly set")
		}
	})
}

func TestResolveRestoreTarget_IsRenamed(t *testing.T) {
	backup := &backupsv1alpha1.Backup{
		ObjectMeta: metav1.ObjectMeta{Name: "bk", Namespace: "tenant-root"},
		Spec: backupsv1alpha1.BackupSpec{
			ApplicationRef: corev1.TypedLocalObjectReference{
				APIGroup: new("apps.cozystack.io"),
				Kind:     "VMInstance",
				Name:     "test-alpine",
			},
		},
	}

	t.Run("same name is not renamed", func(t *testing.T) {
		rj := &backupsv1alpha1.RestoreJob{
			ObjectMeta: metav1.ObjectMeta{Name: "rj", Namespace: "tenant-root"},
			Spec: backupsv1alpha1.RestoreJobSpec{
				BackupRef: corev1.LocalObjectReference{Name: "bk"},
				TargetApplicationRef: &corev1.TypedLocalObjectReference{
					APIGroup: new("apps.cozystack.io"),
					Kind:     "VMInstance",
					Name:     "test-alpine",
				},
			},
		}
		target := resolveRestoreTarget(rj, backup, RestoreOptions{
			CommonRestoreOptions: CommonRestoreOptions{TargetNamespace: "tenant-foo"},
		})
		if target.IsRenamed {
			t.Error("IsRenamed should be false when names match")
		}
	})

	t.Run("different name is renamed", func(t *testing.T) {
		rj := &backupsv1alpha1.RestoreJob{
			ObjectMeta: metav1.ObjectMeta{Name: "rj", Namespace: "tenant-root"},
			Spec: backupsv1alpha1.RestoreJobSpec{
				BackupRef: corev1.LocalObjectReference{Name: "bk"},
				TargetApplicationRef: &corev1.TypedLocalObjectReference{
					APIGroup: new("apps.cozystack.io"),
					Kind:     "VMInstance",
					Name:     "test-new",
				},
			},
		}
		target := resolveRestoreTarget(rj, backup, RestoreOptions{
			CommonRestoreOptions: CommonRestoreOptions{TargetNamespace: "tenant-foo"},
		})
		if !target.IsRenamed {
			t.Error("IsRenamed should be true when names differ")
		}
		if target.AppName != "test-new" {
			t.Errorf("AppName = %q, want 'test-new'", target.AppName)
		}
		if !target.IsCopy {
			t.Error("IsCopy should be true")
		}
	})

	t.Run("no targetApplicationRef is not renamed", func(t *testing.T) {
		rj := &backupsv1alpha1.RestoreJob{
			ObjectMeta: metav1.ObjectMeta{Name: "rj", Namespace: "tenant-root"},
			Spec: backupsv1alpha1.RestoreJobSpec{
				BackupRef: corev1.LocalObjectReference{Name: "bk"},
			},
		}
		target := resolveRestoreTarget(rj, backup, RestoreOptions{
			CommonRestoreOptions: CommonRestoreOptions{TargetNamespace: "tenant-foo"},
		})
		if target.IsRenamed {
			t.Error("IsRenamed should be false when targetApplicationRef is nil")
		}
		if target.AppName != "test-alpine" {
			t.Errorf("AppName = %q, want 'test-alpine'", target.AppName)
		}
	})
}

// makeUnstructuredHelmRelease creates an unstructured HelmRelease for dynamic client tests.
func makeUnstructuredHelmRelease(name, namespace string, labels map[string]string) *unstructured.Unstructured {
	hr := &unstructured.Unstructured{}
	hr.SetAPIVersion("helm.toolkit.fluxcd.io/v2")
	hr.SetKind("HelmRelease")
	hr.SetName(name)
	hr.SetNamespace(namespace)
	hr.SetLabels(labels)
	return hr
}

func TestPostRestoreRename_RenamesHelmRelease(t *testing.T) {
	ns := "tenant-foo"
	sourceHR := makeUnstructuredHelmRelease("vm-instance-test-alpine", ns, map[string]string{
		appNameLabel:                  "test-alpine",
		"app.kubernetes.io/instance":  "vm-instance-test-alpine",
		"helm.toolkit.fluxcd.io/name": "vm-instance-test-alpine",
	})

	restoreJob := &backupsv1alpha1.RestoreJob{
		ObjectMeta: metav1.ObjectMeta{Name: "rj-rename", Namespace: "tenant-root"},
		Spec: backupsv1alpha1.RestoreJobSpec{
			BackupRef: corev1.LocalObjectReference{Name: "bk"},
		},
	}
	backup := &backupsv1alpha1.Backup{
		ObjectMeta: metav1.ObjectMeta{Name: "bk", Namespace: "tenant-root"},
		Spec: backupsv1alpha1.BackupSpec{
			ApplicationRef: corev1.TypedLocalObjectReference{
				APIGroup: new("apps.cozystack.io"),
				Kind:     "VMInstance",
				Name:     "test-alpine",
			},
		},
	}
	target := restoreTarget{
		Namespace: ns,
		AppName:   "test-new",
		AppKind:   "VMInstance",
		IsCopy:    true,
		IsRenamed: true,
	}

	reconciler := newTestRestoreJobReconcilerWithDynamic(t, []runtime.Object{sourceHR}, restoreJob, backup)
	ctx := context.Background()

	err := reconciler.postRestoreRename(ctx, restoreJob, backup, target)
	if err != nil {
		t.Fatalf("postRestoreRename() error = %v", err)
	}

	hrClient := reconciler.Resource(helmReleaseGVR).Namespace(ns)

	// New HelmRelease should exist with target name
	newHR, err := hrClient.Get(ctx, "vm-instance-test-new", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("new HelmRelease vm-instance-test-new not found: %v", err)
	}

	// Check labels were updated
	labels := newHR.GetLabels()
	if labels[appNameLabel] != "test-new" {
		t.Errorf("label %s = %q, want 'test-new'", appNameLabel, labels[appNameLabel])
	}
	if labels["app.kubernetes.io/instance"] != "vm-instance-test-new" {
		t.Errorf("label app.kubernetes.io/instance = %q, want 'vm-instance-test-new'", labels["app.kubernetes.io/instance"])
	}
	if labels["helm.toolkit.fluxcd.io/name"] != "vm-instance-test-new" {
		t.Errorf("label helm.toolkit.fluxcd.io/name = %q, want 'vm-instance-test-new'", labels["helm.toolkit.fluxcd.io/name"])
	}

	// Old HelmRelease should be deleted
	_, err = hrClient.Get(ctx, "vm-instance-test-alpine", metav1.GetOptions{})
	if err == nil {
		t.Error("old HelmRelease vm-instance-test-alpine should have been deleted")
	}
}

func TestPostRestoreRename_SkipsWhenSourceNotFound(t *testing.T) {
	restoreJob := &backupsv1alpha1.RestoreJob{
		ObjectMeta: metav1.ObjectMeta{Name: "rj-rename", Namespace: "tenant-root"},
		Spec: backupsv1alpha1.RestoreJobSpec{
			BackupRef: corev1.LocalObjectReference{Name: "bk"},
		},
	}
	backup := &backupsv1alpha1.Backup{
		ObjectMeta: metav1.ObjectMeta{Name: "bk", Namespace: "tenant-root"},
		Spec: backupsv1alpha1.BackupSpec{
			ApplicationRef: corev1.TypedLocalObjectReference{
				APIGroup: new("apps.cozystack.io"),
				Kind:     "VMInstance",
				Name:     "test-alpine",
			},
		},
	}
	target := restoreTarget{
		Namespace: "tenant-foo",
		AppName:   "test-new",
		AppKind:   "VMInstance",
		IsCopy:    true,
		IsRenamed: true,
	}

	// No HelmRelease in dynamic client — should skip gracefully
	reconciler := newTestRestoreJobReconcilerWithDynamic(t, nil, restoreJob, backup)
	ctx := context.Background()

	err := reconciler.postRestoreRename(ctx, restoreJob, backup, target)
	if err != nil {
		t.Fatalf("postRestoreRename() should skip gracefully when source HR not found, got error: %v", err)
	}
}

func TestPostRestoreRename_IdempotentWhenTargetExists(t *testing.T) {
	ns := "tenant-foo"
	sourceHR := makeUnstructuredHelmRelease("vm-instance-test-alpine", ns, map[string]string{
		appNameLabel: "test-alpine",
	})
	// Target already exists (e.g. from a previous reconcile)
	targetHR := makeUnstructuredHelmRelease("vm-instance-test-new", ns, map[string]string{
		appNameLabel: "test-new",
	})

	restoreJob := &backupsv1alpha1.RestoreJob{
		ObjectMeta: metav1.ObjectMeta{Name: "rj-rename", Namespace: "tenant-root"},
		Spec: backupsv1alpha1.RestoreJobSpec{
			BackupRef: corev1.LocalObjectReference{Name: "bk"},
		},
	}
	backup := &backupsv1alpha1.Backup{
		ObjectMeta: metav1.ObjectMeta{Name: "bk", Namespace: "tenant-root"},
		Spec: backupsv1alpha1.BackupSpec{
			ApplicationRef: corev1.TypedLocalObjectReference{
				APIGroup: new("apps.cozystack.io"),
				Kind:     "VMInstance",
				Name:     "test-alpine",
			},
		},
	}
	target := restoreTarget{
		Namespace: ns,
		AppName:   "test-new",
		AppKind:   "VMInstance",
		IsCopy:    true,
		IsRenamed: true,
	}

	reconciler := newTestRestoreJobReconcilerWithDynamic(t, []runtime.Object{sourceHR, targetHR}, restoreJob, backup)
	ctx := context.Background()

	err := reconciler.postRestoreRename(ctx, restoreJob, backup, target)
	if err != nil {
		t.Fatalf("postRestoreRename() should be idempotent, got error: %v", err)
	}

	hrClient := reconciler.Resource(helmReleaseGVR).Namespace(ns)

	// Target should still exist
	_, err = hrClient.Get(ctx, "vm-instance-test-new", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("target HelmRelease should exist: %v", err)
	}

	// Source should be deleted
	_, err = hrClient.Get(ctx, "vm-instance-test-alpine", metav1.GetOptions{})
	if err == nil {
		t.Error("source HelmRelease should have been deleted")
	}
}

// --- failIfTargetExists enforcement tests ---

func TestTargetHelmReleaseExists_ReturnsTrueWhenPresent(t *testing.T) {
	ns := "tenant-copy"
	hr := makeUnstructuredHelmRelease("vm-instance-test", ns, nil)

	restoreJob := &backupsv1alpha1.RestoreJob{
		ObjectMeta: metav1.ObjectMeta{Name: "rj", Namespace: "tenant-root"},
	}
	backup := &backupsv1alpha1.Backup{ObjectMeta: metav1.ObjectMeta{Name: "bk", Namespace: "tenant-root"}}

	reconciler := newTestRestoreJobReconcilerWithDynamic(t, []runtime.Object{hr}, restoreJob, backup)
	ctx := context.Background()

	exists, err := reconciler.targetHelmReleaseExists(ctx, vmInstanceKind, "vm-instance-test", ns)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !exists {
		t.Error("expected exists=true when HelmRelease is present")
	}
}

func TestTargetHelmReleaseExists_ReturnsFalseWhenAbsent(t *testing.T) {
	ns := "tenant-copy"
	restoreJob := &backupsv1alpha1.RestoreJob{
		ObjectMeta: metav1.ObjectMeta{Name: "rj", Namespace: "tenant-root"},
	}
	backup := &backupsv1alpha1.Backup{ObjectMeta: metav1.ObjectMeta{Name: "bk", Namespace: "tenant-root"}}

	reconciler := newTestRestoreJobReconcilerWithDynamic(t, nil, restoreJob, backup)
	ctx := context.Background()

	exists, err := reconciler.targetHelmReleaseExists(ctx, vmInstanceKind, "vm-instance-test", ns)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if exists {
		t.Error("expected exists=false when HelmRelease is absent")
	}
}

func TestTargetHelmReleaseExists_UnsupportedKindViaHelper(t *testing.T) {
	ns := "tenant-copy"
	// Even though a HR exists, unsupported kinds produce an empty hrName
	// from helmReleaseNameForApp, which makes targetHelmReleaseExists return false.
	hr := makeUnstructuredHelmRelease("vm-instance-test", ns, nil)

	restoreJob := &backupsv1alpha1.RestoreJob{
		ObjectMeta: metav1.ObjectMeta{Name: "rj", Namespace: "tenant-root"},
	}
	backup := &backupsv1alpha1.Backup{ObjectMeta: metav1.ObjectMeta{Name: "bk", Namespace: "tenant-root"}}

	reconciler := newTestRestoreJobReconcilerWithDynamic(t, []runtime.Object{hr}, restoreJob, backup)
	ctx := context.Background()

	// Unsupported kinds get empty hrName from the helper
	hrName := helmReleaseNameForApp("MariaDB", "mydb")
	exists, err := reconciler.targetHelmReleaseExists(ctx, "MariaDB", hrName, ns)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if exists {
		t.Error("expected exists=false for unsupported kind (empty hrName)")
	}
}

// --- createResourceModifiersConfigMap tests ---

func makeTestBackup(ns, appName, appKind string) *backupsv1alpha1.Backup {
	return &backupsv1alpha1.Backup{
		ObjectMeta: metav1.ObjectMeta{Name: "bk", Namespace: ns},
		Spec: backupsv1alpha1.BackupSpec{
			ApplicationRef: corev1.TypedLocalObjectReference{
				APIGroup: new("apps.cozystack.io"),
				Kind:     appKind,
				Name:     appName,
			},
		},
	}
}

func makeTestRestoreJob(ns, name string) *backupsv1alpha1.RestoreJob {
	return &backupsv1alpha1.RestoreJob{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns},
		Spec: backupsv1alpha1.RestoreJobSpec{
			BackupRef: corev1.LocalObjectReference{Name: "bk"},
		},
	}
}

func makeVMInstanceUR(ip, mac string, dvs []backupsv1alpha1.DataVolumeResource) *runtime.RawExtension {
	data := vmInstanceResources{IP: ip, MAC: mac, DataVolumes: dvs}
	raw, _ := json.Marshal(data)
	return &runtime.RawExtension{Raw: raw}
}

func TestCreateResourceModifiersConfigMap_InPlace_WithOVN(t *testing.T) {
	ns := "tenant-root"
	restoreJob := makeTestRestoreJob(ns, "rj-inplace")
	backup := makeTestBackup(ns, "test-vm", "VMInstance")

	ur := makeVMInstanceUR("10.0.0.5", "aa:bb:cc:dd:ee:ff", nil)
	target := restoreTarget{Namespace: ns, AppName: "test-vm", AppKind: "VMInstance", IsCopy: false}
	opts := RestoreOptions{} // defaults: keepOriginalIpAndMac=true

	reconciler := newTestRestoreJobReconciler(t, restoreJob, backup)
	ctx := context.Background()

	cm, err := reconciler.createResourceModifiersConfigMap(ctx, restoreJob, backup, ur, target, opts)
	if err != nil {
		t.Fatalf("createResourceModifiersConfigMap() error = %v", err)
	}
	if cm == nil {
		t.Fatal("expected non-nil ConfigMap")
	}

	rulesYAML, ok := cm.Data["resource-modifier-rules.yaml"]
	if !ok {
		t.Fatal("ConfigMap missing resource-modifier-rules.yaml key")
	}

	// Should contain PVC adoption annotation
	if !containsString(rulesYAML, cdiAllowClaimAdoption) {
		t.Error("expected PVC adoption annotation in rules")
	}
	// Should contain OVN IP annotation (keepOriginalIpAndMac defaults to true)
	if !containsString(rulesYAML, ovnIPAnnotation) {
		t.Error("expected OVN IP annotation in rules for in-place restore with IP")
	}
	// Should NOT contain selector removal (in-place, not copy)
	if containsString(rulesYAML, "/spec/selector") {
		t.Error("selector removal rule should not be present for in-place restore")
	}
}

func TestCreateResourceModifiersConfigMap_Copy_NoOVN(t *testing.T) {
	sourceNS := "tenant-root"
	targetNS := "tenant-copy"
	restoreJob := makeTestRestoreJob(sourceNS, "rj-copy")
	backup := makeTestBackup(sourceNS, "test-vm", "VMInstance")

	// Copy restore: keepOriginalIpAndMac=false, no OVN annotations on copy
	opts := RestoreOptions{
		KeepOriginalIpAndMac: new(false),
	}
	ur := makeVMInstanceUR("10.0.0.5", "aa:bb:cc:dd:ee:ff", nil)
	target := restoreTarget{Namespace: targetNS, AppName: "test-vm", AppKind: "VMInstance", IsCopy: true}

	reconciler := newTestRestoreJobReconciler(t, restoreJob, backup)
	ctx := context.Background()

	cm, err := reconciler.createResourceModifiersConfigMap(ctx, restoreJob, backup, ur, target, opts)
	if err != nil {
		t.Fatalf("createResourceModifiersConfigMap() error = %v", err)
	}

	rulesYAML := cm.Data["resource-modifier-rules.yaml"]

	// Should contain PVC adoption annotation
	if !containsString(rulesYAML, cdiAllowClaimAdoption) {
		t.Error("expected PVC adoption annotation in rules")
	}
	// Should contain merge patch nulling out selector and volumeName (copy restore)
	if !containsString(rulesYAML, "selector: null") {
		t.Error("expected selector: null merge patch for copy restore")
	}
	if !containsString(rulesYAML, "volumeName: null") {
		t.Error("expected volumeName: null merge patch for copy restore")
	}
	// Should NOT contain OVN annotations (keepOriginalIpAndMac=false)
	if containsString(rulesYAML, ovnIPAnnotation) {
		t.Error("OVN IP annotation should not be present when keepOriginalIpAndMac=false")
	}
}

func TestCreateResourceModifiersConfigMap_AlreadyExists_IsIdempotent(t *testing.T) {
	ns := "tenant-root"
	restoreJob := makeTestRestoreJob(ns, "rj-idem")
	backup := makeTestBackup(ns, "test-vm", "VMInstance")

	target := restoreTarget{Namespace: ns, AppName: "test-vm", AppKind: "VMInstance", IsCopy: false}
	opts := RestoreOptions{}
	ur := makeVMInstanceUR("", "", nil)

	reconciler := newTestRestoreJobReconciler(t, restoreJob, backup)
	ctx := context.Background()

	// First call creates the ConfigMap.
	cm1, err := reconciler.createResourceModifiersConfigMap(ctx, restoreJob, backup, ur, target, opts)
	if err != nil {
		t.Fatalf("first call error = %v", err)
	}

	// Second call must not error (AlreadyExists → update path).
	cm2, err := reconciler.createResourceModifiersConfigMap(ctx, restoreJob, backup, ur, target, opts)
	if err != nil {
		t.Fatalf("second call error = %v", err)
	}
	if cm1.Name != cm2.Name {
		t.Errorf("ConfigMap name changed: %q → %q", cm1.Name, cm2.Name)
	}
}

// --- helmReleaseNameForApp tests ---

func TestHelmReleaseNameForApp(t *testing.T) {
	tests := []struct {
		kind, name, want string
	}{
		{vmInstanceKind, "test-alpine", "vm-instance-test-alpine"},
		{vmDiskAppKind, "ubuntu-source", "vm-disk-ubuntu-source"},
		{"MariaDB", "mydb", ""},
	}
	for _, tt := range tests {
		t.Run(tt.kind+"/"+tt.name, func(t *testing.T) {
			got := helmReleaseNameForApp(tt.kind, tt.name)
			if got != tt.want {
				t.Errorf("helmReleaseNameForApp(%q, %q) = %q, want %q", tt.kind, tt.name, got, tt.want)
			}
		})
	}
}

func TestVeleroRenameSupported(t *testing.T) {
	tests := []struct {
		kind string
		want bool
	}{
		{vmInstanceKind, true},
		{vmDiskAppKind, false},
		{"Postgres", false},
		{"MariaDB", false},
		{"ClickHouse", false},
		{"MongoDB", false},
		{"", false},
	}
	for _, tt := range tests {
		t.Run(tt.kind, func(t *testing.T) {
			if got := veleroRenameSupported(tt.kind); got != tt.want {
				t.Errorf("veleroRenameSupported(%q) = %v, want %v", tt.kind, got, tt.want)
			}
		})
	}
}

// TestReconcileVeleroRestore_RenameGuard covers the fix for a RestoreJob that
// asked for a new application name via targetApplicationRef.name but whose
// kind cannot be renamed by postRestoreRename: it used to report Succeeded
// with the application restored under the source name.
func TestReconcileVeleroRestore_RenameGuard(t *testing.T) {
	const (
		sourceNS = "tenant-src"
		targetNS = "tenant-dst"
	)

	tests := []struct {
		name        string
		appKind     string
		targetKind  string // targetApplicationRef.kind; defaults to appKind
		targetName  string // targetApplicationRef.name; "" omits targetApplicationRef
		targetNS    string
		wantPhase   backupsv1alpha1.RestoreJobPhase
		wantMessage string
		wantMissing string // a remedy the message must not offer for this kind
	}{
		{
			name:        "postgres cross-namespace rename is rejected",
			appKind:     "Postgres",
			targetName:  "dst",
			targetNS:    targetNS,
			wantPhase:   backupsv1alpha1.RestoreJobPhaseFailed,
			wantMessage: `restoring Postgres "src" under a different name ("dst") is not supported by the Velero strategy`,
		},
		{
			name:        "postgres rejection points at the engine-native strategy",
			appKind:     "Postgres",
			targetName:  "dst",
			targetNS:    targetNS,
			wantPhase:   backupsv1alpha1.RestoreJobPhaseFailed,
			wantMessage: "engine-native strategy",
		},
		{
			name:        "vmdisk cross-namespace rename is rejected",
			appKind:     vmDiskAppKind,
			targetName:  "dst",
			targetNS:    targetNS,
			wantPhase:   backupsv1alpha1.RestoreJobPhaseFailed,
			wantMessage: `restoring VMDisk "src" under a different name ("dst") is not supported by the Velero strategy`,
			wantMissing: "engine-native strategy",
		},
		{
			name:        "vmdisk rename is rejected by the backup kind, not the target kind",
			appKind:     vmDiskAppKind,
			targetKind:  vmInstanceKind,
			targetName:  "dst",
			targetNS:    targetNS,
			wantPhase:   backupsv1alpha1.RestoreJobPhaseFailed,
			wantMessage: `restoring VMDisk "src" under a different name ("dst") is not supported by the Velero strategy`,
		},
		{
			name:        "postgres same-namespace rename keeps the DataUpload message",
			appKind:     "Postgres",
			targetName:  "dst",
			targetNS:    "",
			wantPhase:   backupsv1alpha1.RestoreJobPhaseFailed,
			wantMessage: "restoring to the same namespace with a different application name is not supported",
		},
		{
			name:       "vminstance cross-namespace rename passes the guard",
			appKind:    vmInstanceKind,
			targetName: "dst",
			targetNS:   targetNS,
			wantPhase:  backupsv1alpha1.RestoreJobPhaseRunning,
		},
		{
			name:       "vmdisk cross-namespace copy keeping the source name passes the guard",
			appKind:    vmDiskAppKind,
			targetName: "src",
			targetNS:   targetNS,
			wantPhase:  backupsv1alpha1.RestoreJobPhaseRunning,
		},
		{
			name:      "vmdisk in-place restore without targetApplicationRef passes the guard",
			appKind:   vmDiskAppKind,
			targetNS:  "",
			wantPhase: backupsv1alpha1.RestoreJobPhaseRunning,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			backup := &backupsv1alpha1.Backup{
				ObjectMeta: metav1.ObjectMeta{Name: "src-sb", Namespace: sourceNS},
				Spec: backupsv1alpha1.BackupSpec{
					ApplicationRef: corev1.TypedLocalObjectReference{
						APIGroup: new("apps.cozystack.io"),
						Kind:     tt.appKind,
						Name:     "src",
					},
					StrategyRef: corev1.TypedLocalObjectReference{Kind: "Velero", Name: "cozy-default-velero"},
				},
			}
			restoreJob := &backupsv1alpha1.RestoreJob{
				ObjectMeta: metav1.ObjectMeta{Name: "src-restore", Namespace: sourceNS},
				Spec: backupsv1alpha1.RestoreJobSpec{
					BackupRef: corev1.LocalObjectReference{Name: "src-sb"},
				},
			}
			if tt.targetName != "" {
				targetKind := tt.targetKind
				if targetKind == "" {
					targetKind = tt.appKind
				}
				restoreJob.Spec.TargetApplicationRef = &corev1.TypedLocalObjectReference{
					APIGroup: new("apps.cozystack.io"),
					Kind:     targetKind,
					Name:     tt.targetName,
				}
			}
			if tt.targetNS != "" {
				raw, err := json.Marshal(RestoreOptions{CommonRestoreOptions: CommonRestoreOptions{TargetNamespace: tt.targetNS}})
				if err != nil {
					t.Fatalf("marshal options: %v", err)
				}
				restoreJob.Spec.Options = &runtime.RawExtension{Raw: raw}
			}
			targetNamespace := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: targetNS}}

			reconciler := newTestRestoreJobReconcilerWithDynamic(t, nil, restoreJob, backup, targetNamespace)
			reconciler.Client = newRestoreJobTestClient(t, restoreJob, backup, targetNamespace)
			ctx := context.Background()

			if _, err := reconciler.reconcileVeleroRestore(ctx, restoreJob, backup); err != nil {
				t.Fatalf("reconcileVeleroRestore() error = %v", err)
			}

			got := &backupsv1alpha1.RestoreJob{}
			if err := reconciler.Get(ctx, client.ObjectKeyFromObject(restoreJob), got); err != nil {
				t.Fatalf("get RestoreJob: %v", err)
			}
			if got.Status.Phase != tt.wantPhase {
				t.Fatalf("phase = %q, want %q", got.Status.Phase, tt.wantPhase)
			}
			if tt.wantMessage == "" {
				return
			}
			ready := meta.FindStatusCondition(got.Status.Conditions, "Ready")
			if ready == nil {
				t.Fatalf("Ready condition missing, conditions: %+v", got.Status.Conditions)
			}
			if !strings.Contains(ready.Message, tt.wantMessage) {
				t.Errorf("Ready message = %q, want it to contain %q", ready.Message, tt.wantMessage)
			}
			if tt.wantMissing != "" && strings.Contains(ready.Message, tt.wantMissing) {
				t.Errorf("Ready message = %q, must not offer %q", ready.Message, tt.wantMissing)
			}
		})
	}
}

// TestReconcileVeleroRestore_RenameGuard_InFlight covers a rename RestoreJob
// that was already Running when the guard was introduced: its Velero Restore
// has completed under the source name, so the job must fail with a message
// saying so and release its resource-modifier ConfigMap instead of waiting
// for the job's deletion. A VMInstance in the same state still succeeds.
func TestReconcileVeleroRestore_RenameGuard_InFlight(t *testing.T) {
	const (
		sourceNS = "tenant-src"
		targetNS = "tenant-dst"
	)
	tests := []struct {
		name        string
		appKind     string
		wantPhase   backupsv1alpha1.RestoreJobPhase
		wantMessage string
	}{
		{
			name:        "vmdisk in-flight rename fails and names the source-name copy",
			appKind:     vmDiskAppKind,
			wantPhase:   backupsv1alpha1.RestoreJobPhaseFailed,
			wantMessage: `This RestoreJob was already running, so "src" may have been restored into namespace "tenant-dst" under its source name`,
		},
		{
			name:      "vminstance in-flight rename still succeeds",
			appKind:   vmInstanceKind,
			wantPhase: backupsv1alpha1.RestoreJobPhaseSucceeded,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			started := metav1.Now()
			backup := &backupsv1alpha1.Backup{
				ObjectMeta: metav1.ObjectMeta{Name: "src-sb", Namespace: sourceNS},
				Spec: backupsv1alpha1.BackupSpec{
					ApplicationRef: corev1.TypedLocalObjectReference{
						APIGroup: new("apps.cozystack.io"),
						Kind:     tt.appKind,
						Name:     "src",
					},
					StrategyRef:    corev1.TypedLocalObjectReference{Kind: "Velero", Name: "cozy-default-velero"},
					DriverMetadata: map[string]string{veleroBackupNameMetadataKey: "vb"},
				},
			}
			raw, err := json.Marshal(RestoreOptions{CommonRestoreOptions: CommonRestoreOptions{TargetNamespace: targetNS}})
			if err != nil {
				t.Fatalf("marshal options: %v", err)
			}
			restoreJob := &backupsv1alpha1.RestoreJob{
				ObjectMeta: metav1.ObjectMeta{Name: "src-restore", Namespace: sourceNS},
				Spec: backupsv1alpha1.RestoreJobSpec{
					BackupRef: corev1.LocalObjectReference{Name: "src-sb"},
					TargetApplicationRef: &corev1.TypedLocalObjectReference{
						APIGroup: new("apps.cozystack.io"),
						Kind:     tt.appKind,
						Name:     "dst",
					},
					Options: &runtime.RawExtension{Raw: raw},
				},
				Status: backupsv1alpha1.RestoreJobStatus{
					Phase:     backupsv1alpha1.RestoreJobPhaseRunning,
					StartedAt: &started,
				},
			}
			owningLabels := map[string]string{
				backupsv1alpha1.OwningJobNameLabel:      restoreJob.Name,
				backupsv1alpha1.OwningJobNamespaceLabel: restoreJob.Namespace,
			}
			veleroRestore := &velerov1.Restore{
				ObjectMeta: metav1.ObjectMeta{Name: "vr", Namespace: veleroNamespace, Labels: owningLabels},
				Status:     velerov1.RestoreStatus{Phase: velerov1.RestorePhaseCompleted},
			}
			modifiers := &corev1.ConfigMap{
				ObjectMeta: metav1.ObjectMeta{Name: "restore-modifiers-tenant-src-src-restore", Namespace: veleroNamespace, Labels: owningLabels},
			}
			strategy := &strategyv1alpha1.Velero{ObjectMeta: metav1.ObjectMeta{Name: "cozy-default-velero"}}
			targetNamespace := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: targetNS}}

			reconciler := newTestRestoreJobReconcilerWithDynamic(t, nil)
			testScheme := runtime.NewScheme()
			_ = scheme.AddToScheme(testScheme)
			_ = backupsv1alpha1.AddToScheme(testScheme)
			_ = velerov1.AddToScheme(testScheme)
			_ = strategyv1alpha1.AddToScheme(testScheme)
			reconciler.Client = clientfake.NewClientBuilder().
				WithScheme(testScheme).
				WithObjects(restoreJob, backup, veleroRestore, modifiers, strategy, targetNamespace).
				WithStatusSubresource(&backupsv1alpha1.RestoreJob{}).
				Build()
			ctx := context.Background()

			if _, err := reconciler.reconcileVeleroRestore(ctx, restoreJob, backup); err != nil {
				t.Fatalf("reconcileVeleroRestore() error = %v", err)
			}

			got := &backupsv1alpha1.RestoreJob{}
			if err := reconciler.Get(ctx, client.ObjectKeyFromObject(restoreJob), got); err != nil {
				t.Fatalf("get RestoreJob: %v", err)
			}
			if got.Status.Phase != tt.wantPhase {
				t.Fatalf("phase = %q, want %q", got.Status.Phase, tt.wantPhase)
			}
			if err := reconciler.Get(ctx, client.ObjectKeyFromObject(modifiers), &corev1.ConfigMap{}); !errors.IsNotFound(err) {
				t.Errorf("resource-modifier ConfigMap should be deleted, get err = %v", err)
			}
			if tt.wantMessage == "" {
				return
			}
			ready := meta.FindStatusCondition(got.Status.Conditions, "Ready")
			if ready == nil {
				t.Fatalf("Ready condition missing, conditions: %+v", got.Status.Conditions)
			}
			if !strings.Contains(ready.Message, tt.wantMessage) {
				t.Errorf("Ready message = %q, want it to contain %q", ready.Message, tt.wantMessage)
			}
		})
	}
}

func TestTargetHelmReleaseExists_VMDisk(t *testing.T) {
	ns := "tenant-copy"
	hr := makeUnstructuredHelmRelease("vm-disk-ubuntu-source", ns, nil)

	restoreJob := &backupsv1alpha1.RestoreJob{
		ObjectMeta: metav1.ObjectMeta{Name: "rj", Namespace: "tenant-root"},
	}
	backup := &backupsv1alpha1.Backup{ObjectMeta: metav1.ObjectMeta{Name: "bk", Namespace: "tenant-root"}}

	reconciler := newTestRestoreJobReconcilerWithDynamic(t, []runtime.Object{hr}, restoreJob, backup)
	ctx := context.Background()

	hrName := helmReleaseNameForApp(vmDiskAppKind, "ubuntu-source")
	exists, err := reconciler.targetHelmReleaseExists(ctx, vmDiskAppKind, hrName, ns)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !exists {
		t.Error("expected exists=true for VMDisk HelmRelease")
	}
}

func TestTargetHelmReleaseExists_UnsupportedKind_ReturnsFalse(t *testing.T) {
	restoreJob := &backupsv1alpha1.RestoreJob{
		ObjectMeta: metav1.ObjectMeta{Name: "rj", Namespace: "tenant-root"},
	}
	backup := &backupsv1alpha1.Backup{ObjectMeta: metav1.ObjectMeta{Name: "bk", Namespace: "tenant-root"}}

	reconciler := newTestRestoreJobReconcilerWithDynamic(t, nil, restoreJob, backup)
	ctx := context.Background()

	// Unsupported kinds produce empty hrName which returns false
	hrName := helmReleaseNameForApp("MariaDB", "mydb")
	exists, err := reconciler.targetHelmReleaseExists(ctx, "MariaDB", hrName, "tenant-copy")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if exists {
		t.Error("expected exists=false for unsupported kind")
	}
}

// --- ResourceModifier merge patch test for copy restore ---

func TestCreateResourceModifiersConfigMap_Copy_UsesMergePatchForPVC(t *testing.T) {
	sourceNS := "tenant-root"
	targetNS := "tenant-copy"
	restoreJob := makeTestRestoreJob(sourceNS, "rj-copy-merge")
	backup := makeTestBackup(sourceNS, "test-vm", "VMInstance")

	opts := RestoreOptions{KeepOriginalIpAndMac: new(false)}
	ur := makeVMInstanceUR("", "", nil)
	target := restoreTarget{Namespace: targetNS, AppName: "test-vm", AppKind: "VMInstance", IsCopy: true}

	reconciler := newTestRestoreJobReconciler(t, restoreJob, backup)
	ctx := context.Background()

	cm, err := reconciler.createResourceModifiersConfigMap(ctx, restoreJob, backup, ur, target, opts)
	if err != nil {
		t.Fatalf("createResourceModifiersConfigMap() error = %v", err)
	}

	rulesYAML := cm.Data["resource-modifier-rules.yaml"]

	// Should use merge patch (patchData with null), NOT JSON patch (operation: remove)
	if containsString(rulesYAML, "operation: remove") {
		t.Error("should use merge patch instead of JSON Patch remove for PVC fields")
	}
	// The merge patch should null out selector and volumeName
	if !containsString(rulesYAML, "selector: null") {
		t.Error("expected selector: null in merge patch")
	}
	if !containsString(rulesYAML, "volumeName: null") {
		t.Error("expected volumeName: null in merge patch")
	}
}

// containsString reports whether substr appears in s.
func containsString(s, substr string) bool {
	if len(substr) == 0 || len(s) < len(substr) {
		return false
	}
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}

// keepPVCFixture is an in-place VMInstance restore of one disk, whose PVC
// vm-disk-ubuntu-source is bound to PV pv-current under a Delete policy.
type keepPVCFixture struct {
	ns         string
	restoreJob *backupsv1alpha1.RestoreJob
	backup     *backupsv1alpha1.Backup
	ur         *runtime.RawExtension
	target     restoreTarget
	opts       RestoreOptions
	livePVC    *corev1.PersistentVolumeClaim
	pv         *corev1.PersistentVolume
	origName   string
}

func newKeepPVCFixture(t *testing.T) *keepPVCFixture {
	t.Helper()
	ns := "tenant-root"
	restoreJob := &backupsv1alpha1.RestoreJob{
		ObjectMeta: metav1.ObjectMeta{Name: "restore-test", Namespace: ns, UID: "restore-job-uid"},
		Spec:       backupsv1alpha1.RestoreJobSpec{BackupRef: corev1.LocalObjectReference{Name: "my-backup"}},
	}
	backup := &backupsv1alpha1.Backup{
		ObjectMeta: metav1.ObjectMeta{Name: "my-backup", Namespace: ns},
		Spec: backupsv1alpha1.BackupSpec{
			ApplicationRef: corev1.TypedLocalObjectReference{
				APIGroup: new("apps.cozystack.io"),
				Kind:     "VMInstance",
				Name:     "test-vm",
			},
		},
	}
	urRaw, err := json.Marshal(vmInstanceResources{
		DataVolumes: []backupsv1alpha1.DataVolumeResource{
			{DataVolumeName: "vm-disk-ubuntu-source", ApplicationName: "ubuntu-source"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	return &keepPVCFixture{
		ns:         ns,
		restoreJob: restoreJob,
		backup:     backup,
		ur:         &runtime.RawExtension{Raw: urRaw},
		target:     restoreTarget{Namespace: ns, AppName: "test-vm", AppKind: "VMInstance"},
		opts:       RestoreOptions{KeepOriginalPVC: new(true)},
		livePVC: &corev1.PersistentVolumeClaim{
			ObjectMeta: metav1.ObjectMeta{Name: "vm-disk-ubuntu-source", Namespace: ns, UID: "live-pvc-uid"},
			Spec:       corev1.PersistentVolumeClaimSpec{VolumeName: "pv-current"},
		},
		pv: &corev1.PersistentVolume{
			ObjectMeta: metav1.ObjectMeta{Name: "pv-current"},
			Spec: corev1.PersistentVolumeSpec{
				PersistentVolumeReclaimPolicy: corev1.PersistentVolumeReclaimDelete,
				ClaimRef: &corev1.ObjectReference{
					Kind: "PersistentVolumeClaim", Namespace: ns, Name: "vm-disk-ubuntu-source", UID: "live-pvc-uid",
				},
			},
		},
		origName: "vm-disk-ubuntu-source-orig-" + shortHash(restoreJob.Name),
	}
}

func (f *keepPVCFixture) reconciler(t *testing.T, objects ...client.Object) *RestoreJobReconciler {
	t.Helper()
	dv := &unstructured.Unstructured{}
	dv.SetAPIVersion("cdi.kubevirt.io/v1beta1")
	dv.SetKind("DataVolume")
	dv.SetName("vm-disk-ubuntu-source")
	dv.SetNamespace(f.ns)
	return newTestRestoreJobReconcilerWithDynamic(t, []runtime.Object{dv},
		append([]client.Object{f.restoreJob, f.backup}, objects...)...)
}

func (f *keepPVCFixture) dataVolumeExists(t *testing.T, r *RestoreJobReconciler) bool {
	t.Helper()
	_, err := r.Resource(dataVolumeGVR).Namespace(f.ns).Get(context.Background(), "vm-disk-ubuntu-source", metav1.GetOptions{})
	if err != nil && !errors.IsNotFound(err) {
		t.Fatalf("get DataVolume: %v", err)
	}
	return err == nil
}

// A -orig PVC this RestoreJob did not create, such as one left by a deleted
// RestoreJob of the same name, must stop the restore before the DataVolume is
// deleted: skipping the rename would leave the live disk under its own name
// for the restore to overwrite.
func TestPrepareForRestore_KeepOriginalPVC_RefusesForeignOrigPVC(t *testing.T) {
	for _, tc := range []struct {
		name        string
		annotations map[string]string
	}{
		{name: "no restore job annotation"},
		{name: "another restore job with the same name", annotations: map[string]string{origPVCRestoreJobUIDAnnotation: "previous-restore-job-uid"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newKeepPVCFixture(t)
			foreign := &corev1.PersistentVolumeClaim{
				ObjectMeta: metav1.ObjectMeta{Name: f.origName, Namespace: f.ns, Annotations: tc.annotations},
				Spec:       corev1.PersistentVolumeClaimSpec{VolumeName: "pv-previous"},
			}
			r := f.reconciler(t, f.livePVC, f.pv, foreign)
			ctx := context.Background()

			ready, _, err := r.prepareForRestore(ctx, f.restoreJob, f.backup, f.ur, f.target, f.opts)
			if err == nil {
				t.Errorf("prepareForRestore() = ready %v, nil error; want an error naming %s", ready, f.origName)
			} else if !strings.Contains(err.Error(), f.origName) {
				t.Errorf("error %q does not name the taken PVC %s", err, f.origName)
			}

			if !f.dataVolumeExists(t, r) {
				t.Error("DataVolume was deleted although the disk could not be kept")
			}
			live := &corev1.PersistentVolumeClaim{}
			if err := r.Get(ctx, client.ObjectKeyFromObject(f.livePVC), live); err != nil {
				t.Fatalf("live PVC is gone: %v", err)
			}
			pv := &corev1.PersistentVolume{}
			if err := r.Get(ctx, client.ObjectKey{Name: "pv-current"}, pv); err != nil {
				t.Fatal(err)
			}
			if pv.Spec.PersistentVolumeReclaimPolicy != corev1.PersistentVolumeReclaimDelete || pv.Spec.ClaimRef.Name != f.livePVC.Name {
				t.Errorf("PV was modified: policy %s, claim %s", pv.Spec.PersistentVolumeReclaimPolicy, pv.Spec.ClaimRef.Name)
			}
			got := &corev1.PersistentVolumeClaim{}
			if err := r.Get(ctx, client.ObjectKey{Namespace: f.ns, Name: f.origName}, got); err != nil {
				t.Fatal(err)
			}
			if got.Spec.VolumeName != "pv-previous" {
				t.Errorf("foreign -orig PVC now points at %q", got.Spec.VolumeName)
			}
		})
	}
}

// A rename interrupted after the -orig PVC was created must complete on the
// next reconcile, and a rename already complete must be left alone.
func TestPrepareForRestore_KeepOriginalPVC_ResumesOwnRename(t *testing.T) {
	f := newKeepPVCFixture(t)
	f.pv.Spec.PersistentVolumeReclaimPolicy = corev1.PersistentVolumeReclaimRetain
	f.pv.Annotations = map[string]string{originalReclaimPolicyAnnotation: "Delete"}
	own := &corev1.PersistentVolumeClaim{
		ObjectMeta: metav1.ObjectMeta{
			Name: f.origName, Namespace: f.ns,
			Annotations: map[string]string{
				origPVCRestoreJobUIDAnnotation:  string(f.restoreJob.UID),
				originalReclaimPolicyAnnotation: "Delete",
			},
		},
		Spec: corev1.PersistentVolumeClaimSpec{VolumeName: "pv-current"},
	}
	r := f.reconciler(t, f.livePVC, f.pv, own)
	ctx := context.Background()

	for pass := 1; pass <= 2; pass++ {
		ready, _, err := r.prepareForRestore(ctx, f.restoreJob, f.backup, f.ur, f.target, f.opts)
		if err != nil || !ready {
			t.Fatalf("pass %d: prepareForRestore() = ready %v, error %v; want ready", pass, ready, err)
		}
		if err := r.Get(ctx, client.ObjectKeyFromObject(f.livePVC), &corev1.PersistentVolumeClaim{}); !errors.IsNotFound(err) {
			t.Errorf("pass %d: original PVC still exists (err %v)", pass, err)
		}
		kept := &corev1.PersistentVolumeClaim{}
		if err := r.Get(ctx, client.ObjectKey{Namespace: f.ns, Name: f.origName}, kept); err != nil {
			t.Fatalf("pass %d: -orig PVC is gone: %v", pass, err)
		}
		pv := &corev1.PersistentVolume{}
		if err := r.Get(ctx, client.ObjectKey{Name: "pv-current"}, pv); err != nil {
			t.Fatal(err)
		}
		if ref := pv.Spec.ClaimRef; ref == nil || ref.Name != f.origName || ref.UID != kept.UID {
			t.Errorf("pass %d: PV claimRef = %+v, want %s", pass, ref, f.origName)
		}
	}
	if f.dataVolumeExists(t, r) {
		t.Error("DataVolume was not deleted after the rename completed")
	}
}

// The PV goes to Retain so it outlives the original PVC; the policy it had
// must be recorded, or nobody can set it back on the -orig PVC's PV.
func TestRenamePVC_RecordsOriginalReclaimPolicy(t *testing.T) {
	for _, policy := range []corev1.PersistentVolumeReclaimPolicy{
		corev1.PersistentVolumeReclaimDelete,
		corev1.PersistentVolumeReclaimRetain,
	} {
		t.Run(string(policy), func(t *testing.T) {
			f := newKeepPVCFixture(t)
			f.pv.Spec.PersistentVolumeReclaimPolicy = policy
			r := f.reconciler(t, f.livePVC, f.pv)
			ctx := context.Background()

			if err := r.renamePVC(ctx, f.restoreJob, f.ns, f.livePVC.Name, f.origName); err != nil {
				t.Fatalf("renamePVC() error = %v", err)
			}

			pv := &corev1.PersistentVolume{}
			if err := r.Get(ctx, client.ObjectKey{Name: "pv-current"}, pv); err != nil {
				t.Fatal(err)
			}
			if pv.Spec.PersistentVolumeReclaimPolicy != corev1.PersistentVolumeReclaimRetain {
				t.Errorf("PV policy = %s, want Retain while the -orig PVC holds it", pv.Spec.PersistentVolumeReclaimPolicy)
			}
			if got := pv.Annotations[originalReclaimPolicyAnnotation]; got != string(policy) {
				t.Errorf("PV %s = %q, want %q", originalReclaimPolicyAnnotation, got, policy)
			}
			kept := &corev1.PersistentVolumeClaim{}
			if err := r.Get(ctx, client.ObjectKey{Namespace: f.ns, Name: f.origName}, kept); err != nil {
				t.Fatal(err)
			}
			if got := kept.Annotations[originalReclaimPolicyAnnotation]; got != string(policy) {
				t.Errorf("-orig PVC %s = %q, want %q", originalReclaimPolicyAnnotation, got, policy)
			}
		})
	}
}

// Any failure to keep the disk must hold the restore back before its
// DataVolume is deleted, not only a taken -orig name, and the retry must
// finish the rename once the cause is gone.
func TestPrepareForRestore_KeepOriginalPVC_FailedRenameKeepsDataVolume(t *testing.T) {
	f := newKeepPVCFixture(t)
	r := f.reconciler(t, f.livePVC) // PV pv-current is missing
	ctx := context.Background()

	ready, result, err := r.prepareForRestore(ctx, f.restoreJob, f.backup, f.ur, f.target, f.opts)
	if err != nil || ready || result.RequeueAfter == 0 {
		t.Errorf("prepareForRestore() = ready %v, %+v, error %v; want a requeue without failing the restore", ready, result, err)
	}
	if !f.dataVolumeExists(t, r) {
		t.Fatal("DataVolume was deleted although the disk could not be kept")
	}

	if err := r.Create(ctx, f.pv); err != nil {
		t.Fatal(err)
	}
	ready, _, err = r.prepareForRestore(ctx, f.restoreJob, f.backup, f.ur, f.target, f.opts)
	if err != nil || !ready {
		t.Fatalf("retry: prepareForRestore() = ready %v, error %v; want ready", ready, err)
	}
	if err := r.Get(ctx, client.ObjectKey{Namespace: f.ns, Name: f.origName}, &corev1.PersistentVolumeClaim{}); err != nil {
		t.Errorf("retry: -orig PVC: %v", err)
	}
	if f.dataVolumeExists(t, r) {
		t.Error("retry: DataVolume was not deleted after the rename completed")
	}
}

// laggingCacheReconciler serves every cached read from the objects as they
// were seeded, as an informer that has not caught up with this reconcile's
// own writes would; APIReader sees the writes.
func (f *keepPVCFixture) laggingCacheReconciler(t *testing.T, objects ...client.Object) *RestoreJobReconciler {
	t.Helper()
	r := f.reconciler(t, objects...)
	seeded := append([]client.Object{f.restoreJob, f.backup}, objects...)
	for i, obj := range seeded {
		seeded[i] = obj.DeepCopyObject().(client.Object)
	}
	cache := clientfake.NewClientBuilder().WithScheme(r.Scheme).WithObjects(seeded...).Build()
	apiServer := r.Client.(client.WithWatch)
	r.Client = interceptor.NewClient(apiServer, interceptor.Funcs{
		Get: func(ctx context.Context, _ client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
			return cache.Get(ctx, key, obj, opts...)
		},
	})
	r.APIReader = apiServer
	return r
}

// On the lab, a two-disk restore failed one disk with "failed to get -orig
// PVC ...: not found": the PVC had just been created, and the cache had not
// seen it yet. The rename must not depend on the cache seeing its own writes.
func TestPrepareForRestore_KeepOriginalPVC_CacheLagsBehindWrites(t *testing.T) {
	f := newKeepPVCFixture(t)
	r := f.laggingCacheReconciler(t, f.livePVC, f.pv)
	ctx := context.Background()

	ready, _, err := r.prepareForRestore(ctx, f.restoreJob, f.backup, f.ur, f.target, f.opts)
	if err != nil || !ready {
		t.Fatalf("prepareForRestore() = ready %v, error %v; want ready", ready, err)
	}
	kept := &corev1.PersistentVolumeClaim{}
	if err := r.APIReader.Get(ctx, client.ObjectKey{Namespace: f.ns, Name: f.origName}, kept); err != nil {
		t.Fatalf("-orig PVC: %v", err)
	}
	if err := r.APIReader.Get(ctx, client.ObjectKeyFromObject(f.livePVC), &corev1.PersistentVolumeClaim{}); !errors.IsNotFound(err) {
		t.Errorf("original PVC still exists (err %v)", err)
	}
	pv := &corev1.PersistentVolume{}
	if err := r.APIReader.Get(ctx, client.ObjectKey{Name: "pv-current"}, pv); err != nil {
		t.Fatal(err)
	}
	if ref := pv.Spec.ClaimRef; ref == nil || ref.Name != f.origName || ref.UID != kept.UID {
		t.Errorf("PV claimRef = %+v, want %s", ref, f.origName)
	}
	if pv.Spec.PersistentVolumeReclaimPolicy != corev1.PersistentVolumeReclaimRetain {
		t.Errorf("PV policy = %s, want Retain", pv.Spec.PersistentVolumeReclaimPolicy)
	}
	if f.dataVolumeExists(t, r) {
		t.Error("DataVolume was not deleted after the rename completed")
	}
}
