package backupcontroller

import (
	"context"
	"testing"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
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
	if veleroBackup.Spec.LabelSelector != nil {
		if appLabel, ok := veleroBackup.Spec.LabelSelector.MatchLabels["app"]; ok {
			if appLabel != "test-vm" {
				t.Errorf("Template context Application.metadata.name not applied correctly. Expected 'test-vm', got '%s'", appLabel)
			}
		} else {
			t.Error("Template context Application.metadata.name not found in label selector")
		}
	}

	// 2. Parameters.backupStorageLocationName should be replaced with "default-storage"
	if veleroBackup.Spec.StorageLocation != "default-storage" {
		t.Errorf("Template context Parameters.backupStorageLocationName not applied correctly. Expected 'default-storage', got '%s'", veleroBackup.Spec.StorageLocation)
	}
}
