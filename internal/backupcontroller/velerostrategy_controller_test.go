package backupcontroller

import (
	"context"
	"encoding/json"
	"testing"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/tools/record"
	"sigs.k8s.io/controller-runtime/pkg/client"
	clientfake "sigs.k8s.io/controller-runtime/pkg/client/fake"

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
				APIGroup: stringPtr(""),
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
			APIGroup: stringPtr("strategy.backups.cozystack.io"),
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
				APIGroup: stringPtr("apps.cozystack.io"),
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
				APIGroup: stringPtr("apps.cozystack.io"),
				Kind:     "VMInstance",
				Name:     "test-vm",
			},
		},
	}

	// No targetNamespace in options → in-place restore
	opts := VmiRestoreOptions{}
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
				APIGroup: stringPtr("apps.cozystack.io"),
				Kind:     "VMInstance",
				Name:     "test-vm",
			},
		},
	}

	// targetNamespace equals backup namespace → still in-place
	opts := VmiRestoreOptions{
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
				APIGroup: stringPtr("apps.cozystack.io"),
				Kind:     "VMInstance",
				Name:     "test-vm",
			},
		},
	}

	opts := VmiRestoreOptions{
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

func TestPrepareForRestore_KeepOriginalPVCFalse_SkipsRename(t *testing.T) {
	ns := "tenant-root"

	// Create a PVC that would be renamed if keepOriginalPVC were true
	pvc := &corev1.PersistentVolumeClaim{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "vm-disk-ubuntu-source",
			Namespace: ns,
		},
		Spec: corev1.PersistentVolumeClaimSpec{},
	}

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
				APIGroup: stringPtr("apps.cozystack.io"),
				Kind:     "VMInstance",
				Name:     "test-vm",
			},
		},
	}

	// underlyingResources with a DataVolume referencing the PVC
	urData := vmInstanceResources{
		DataVolumes: []backupsv1alpha1.DataVolumeResource{
			{DataVolumeName: "vm-disk-ubuntu-source", ApplicationName: "ubuntu-source"},
		},
	}
	urRaw, _ := json.Marshal(urData)
	ur := &runtime.RawExtension{Raw: urRaw}

	target := restoreTarget{
		Namespace: ns,
		AppName:   "test-vm",
		AppKind:   "VMInstance",
		IsCopy:    false,
	}

	// keepOriginalPVC = false → PVCs should NOT be renamed
	opts := VmiRestoreOptions{
		KeepOriginalPVC: boolPtr(false),
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

	// Verify the original PVC still exists with its original name (not renamed)
	origPVC := &corev1.PersistentVolumeClaim{}
	err = reconciler.Get(ctx, client.ObjectKey{Namespace: ns, Name: "vm-disk-ubuntu-source"}, origPVC)
	if err != nil {
		t.Errorf("original PVC should still exist with original name when keepOriginalPVC=false, got error: %v", err)
	}

	// Verify no -orig PVC was created
	origSuffix := "-orig-" + shortHash(restoreJob.Name)
	renamedPVC := &corev1.PersistentVolumeClaim{}
	err = reconciler.Get(ctx, client.ObjectKey{Namespace: ns, Name: "vm-disk-ubuntu-source" + origSuffix}, renamedPVC)
	if err == nil {
		t.Error("PVC should NOT have been renamed when keepOriginalPVC=false, but found renamed PVC")
	}
}

func TestResolveRestoreTarget_VMDisk_CommonRestoreOptions(t *testing.T) {
	tests := []struct {
		name           string
		targetNS       string
		wantIsCopy     bool
		wantNamespace  string
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
						APIGroup: stringPtr("apps.cozystack.io"),
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
						APIGroup: stringPtr("apps.cozystack.io"),
						Kind:     "VMDisk",
						Name:     "ubuntu-source",
					},
				},
			}

			// VMDisk uses only CommonRestoreOptions (no VMI-specific fields)
			opts := VmiRestoreOptions{
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

func TestParseRestoreOptions_VMDisk_OnlyCommonFields(t *testing.T) {
	// Simulate a VMDisk restore where only CommonRestoreOptions fields are set
	raw, _ := json.Marshal(map[string]interface{}{
		"targetNamespace":    "tenant-copy",
		"failIfTargetExists": true,
	})
	ext := &runtime.RawExtension{Raw: raw}

	opts, err := parseVmiRestoreOptions(ext)
	if err != nil {
		t.Fatalf("parseVmiRestoreOptions() error = %v", err)
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

func TestVmiRestoreOptions_Defaults(t *testing.T) {
	t.Run("nil options default all bools to true", func(t *testing.T) {
		opts, err := parseVmiRestoreOptions(nil)
		if err != nil {
			t.Fatalf("parseVmiRestoreOptions(nil) error = %v", err)
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
		raw, _ := json.Marshal(map[string]interface{}{})
		opts, err := parseVmiRestoreOptions(&runtime.RawExtension{Raw: raw})
		if err != nil {
			t.Fatalf("parseVmiRestoreOptions({}) error = %v", err)
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
		raw, _ := json.Marshal(map[string]interface{}{
			"failIfTargetExists": false,
			"keepOriginalPVC":    false,
			"keepOriginaIpAndMac": false,
		})
		opts, err := parseVmiRestoreOptions(&runtime.RawExtension{Raw: raw})
		if err != nil {
			t.Fatalf("parseVmiRestoreOptions() error = %v", err)
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
				APIGroup: stringPtr("apps.cozystack.io"),
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
					APIGroup: stringPtr("apps.cozystack.io"),
					Kind:     "VMInstance",
					Name:     "test-alpine",
				},
			},
		}
		target := resolveRestoreTarget(rj, backup, VmiRestoreOptions{
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
					APIGroup: stringPtr("apps.cozystack.io"),
					Kind:     "VMInstance",
					Name:     "test-new",
				},
			},
		}
		target := resolveRestoreTarget(rj, backup, VmiRestoreOptions{
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
		target := resolveRestoreTarget(rj, backup, VmiRestoreOptions{
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
		appNameLabel:                 "test-alpine",
		"app.kubernetes.io/instance": "vm-instance-test-alpine",
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
				APIGroup: stringPtr("apps.cozystack.io"),
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
				APIGroup: stringPtr("apps.cozystack.io"),
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
				APIGroup: stringPtr("apps.cozystack.io"),
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
