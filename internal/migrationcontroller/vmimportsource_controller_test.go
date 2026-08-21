// SPDX-License-Identifier: Apache-2.0
package migrationcontroller

import (
	"context"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/tools/record"
	clientfake "sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	migrationv1alpha1 "github.com/cozystack/cozystack/api/migration/v1alpha1"
)

func testScheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	s := runtime.NewScheme()
	if err := scheme.AddToScheme(s); err != nil {
		t.Fatalf("add client-go scheme: %v", err)
	}
	if err := migrationv1alpha1.AddToScheme(s); err != nil {
		t.Fatalf("add migration scheme: %v", err)
	}
	// Forklift, KubeVirt and CDI objects are unstructured at runtime, so the
	// fake client needs both the object and list kinds registered to serve them.
	s.AddKnownTypeWithName(providerGVK, &unstructured.Unstructured{})
	s.AddKnownTypeWithName(providerGVK.GroupVersion().WithKind("ProviderList"), &unstructured.UnstructuredList{})
	s.AddKnownTypeWithName(planGVK, &unstructured.Unstructured{})
	s.AddKnownTypeWithName(planGVK.GroupVersion().WithKind("PlanList"), &unstructured.UnstructuredList{})
	s.AddKnownTypeWithName(migrationGVK, &unstructured.Unstructured{})
	s.AddKnownTypeWithName(migrationGVK.GroupVersion().WithKind("MigrationList"), &unstructured.UnstructuredList{})
	s.AddKnownTypeWithName(networkMapGVK, &unstructured.Unstructured{})
	s.AddKnownTypeWithName(networkMapGVK.GroupVersion().WithKind("NetworkMapList"), &unstructured.UnstructuredList{})
	s.AddKnownTypeWithName(storageMapGVK, &unstructured.Unstructured{})
	s.AddKnownTypeWithName(storageMapGVK.GroupVersion().WithKind("StorageMapList"), &unstructured.UnstructuredList{})
	s.AddKnownTypeWithName(dataVolumeGVK, &unstructured.Unstructured{})
	s.AddKnownTypeWithName(dataVolumeGVK.GroupVersion().WithKind("DataVolumeList"), &unstructured.UnstructuredList{})
	s.AddKnownTypeWithName(virtualMachineGVK, &unstructured.Unstructured{})
	s.AddKnownTypeWithName(virtualMachineGVK.GroupVersion().WithKind("VirtualMachineList"), &unstructured.UnstructuredList{})
	s.AddKnownTypeWithName(vmDiskGVK, &unstructured.Unstructured{})
	s.AddKnownTypeWithName(vmDiskGVK.GroupVersion().WithKind("VMDiskList"), &unstructured.UnstructuredList{})
	s.AddKnownTypeWithName(vmInstanceGVK, &unstructured.Unstructured{})
	s.AddKnownTypeWithName(vmInstanceGVK.GroupVersion().WithKind("VMInstanceList"), &unstructured.UnstructuredList{})
	return s
}

func vsphereSource(name, namespace string) *migrationv1alpha1.VMImportSource {
	return &migrationv1alpha1.VMImportSource{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace, UID: types.UID(name + "-uid")},
		Spec: migrationv1alpha1.VMImportSourceSpec{
			Type: migrationv1alpha1.ProviderVSphere,
			URL:  "https://vcenter.example.com/sdk",
			Credentials: migrationv1alpha1.ProviderCredentials{
				Username:   "migration@vsphere.local",
				Password:   "hunter2",
				Thumbprint: "AA:BB:CC",
			},
		},
	}
}

// TestSourceWithoutVDDKIsUnavailableNotBroken locks in the policy that a
// missing VDDK image is reported on the object at registration time. The whole
// point is that the tenant learns the VMware path is unconfigured before a
// transfer starts, so this must never silently create a Provider.
func TestSourceWithoutVDDKIsUnavailableNotBroken(t *testing.T) {
	s := testScheme(t)
	src := vsphereSource("vcenter", "tenant-foo")
	c := clientfake.NewClientBuilder().WithScheme(s).
		WithObjects(src).WithStatusSubresource(src).Build()

	r := &VMImportSourceReconciler{Client: c, Scheme: s, Recorder: record.NewFakeRecorder(10), VDDKImage: ""}
	if _, err := r.Reconcile(context.Background(), reconcile.Request{
		NamespacedName: types.NamespacedName{Namespace: "tenant-foo", Name: "vcenter"},
	}); err != nil {
		t.Fatalf("reconcile: %v", err)
	}

	got := &migrationv1alpha1.VMImportSource{}
	if err := c.Get(context.Background(), types.NamespacedName{Namespace: "tenant-foo", Name: "vcenter"}, got); err != nil {
		t.Fatalf("get source: %v", err)
	}
	cond := meta.FindStatusCondition(got.Status.Conditions, migrationv1alpha1.SourceReadyCondition)
	if cond == nil {
		t.Fatal("expected a Ready condition")
	}
	if cond.Status != metav1.ConditionFalse {
		t.Errorf("Ready = %s, want False", cond.Status)
	}
	if cond.Reason != migrationv1alpha1.ReasonVDDKNotConfigured {
		t.Errorf("reason = %q, want %q", cond.Reason, migrationv1alpha1.ReasonVDDKNotConfigured)
	}

	provider := newObject(providerGVK)
	err := c.Get(context.Background(), types.NamespacedName{Namespace: "tenant-foo", Name: sourceProviderName("vcenter")}, provider)
	if err == nil {
		t.Error("a Provider was created even though no VDDK image is configured")
	}
}

// TestSourceProjectsCredentialsAndProviders asserts the happy path materializes
// exactly what Forklift needs, and that the credentials never require the
// tenant to have created a Secret.
func TestSourceProjectsCredentialsAndProviders(t *testing.T) {
	s := testScheme(t)
	src := vsphereSource("vcenter", "tenant-foo")
	c := clientfake.NewClientBuilder().WithScheme(s).
		WithObjects(src).WithStatusSubresource(src).Build()

	r := &VMImportSourceReconciler{
		Client: c, Scheme: s, Recorder: record.NewFakeRecorder(10),
		VDDKImage: "registry.example.com/vddk:8.0.3",
	}
	if _, err := r.Reconcile(context.Background(), reconcile.Request{
		NamespacedName: types.NamespacedName{Namespace: "tenant-foo", Name: "vcenter"},
	}); err != nil {
		t.Fatalf("reconcile: %v", err)
	}

	secret := &corev1.Secret{}
	if err := c.Get(context.Background(), types.NamespacedName{
		Namespace: "tenant-foo", Name: credentialsSecretName("vcenter"),
	}, secret); err != nil {
		t.Fatalf("credentials Secret was not projected: %v", err)
	}
	if got := string(secret.Data["user"]); got != "migration@vsphere.local" {
		t.Errorf("user = %q", got)
	}
	if got := string(secret.Data["password"]); got != "hunter2" {
		t.Errorf("password was not projected")
	}
	if secret.Labels[migrationv1alpha1.ManagedByLabel] != migrationv1alpha1.ManagedByValue {
		t.Error("projected Secret is missing the managed-by label that protects it from being adopted by mistake")
	}

	provider := newObject(providerGVK)
	if err := c.Get(context.Background(), types.NamespacedName{
		Namespace: "tenant-foo", Name: sourceProviderName("vcenter"),
	}, provider); err != nil {
		t.Fatalf("source Provider was not created: %v", err)
	}
	image, _, _ := unstructured.NestedString(provider.Object, "spec", "settings", "vddkInitImage")
	if image != "registry.example.com/vddk:8.0.3" {
		t.Errorf("vddkInitImage = %q, want the operator-configured image", image)
	}
	ns, _, _ := unstructured.NestedString(provider.Object, "spec", "secret", "namespace")
	if ns != "tenant-foo" {
		t.Errorf("secret namespace = %q, want the source's own namespace", ns)
	}

	dest := newObject(providerGVK)
	if err := c.Get(context.Background(), types.NamespacedName{
		Namespace: "tenant-foo", Name: destinationProviderName("vcenter"),
	}, dest); err != nil {
		t.Fatalf("destination Provider was not created: %v", err)
	}
}

// TestProjectorRefusesForeignSecret is the guard that keeps a name collision
// from destroying a tenant's own object: a pre-existing Secret without our
// label is reported, never overwritten.
func TestProjectorRefusesForeignSecret(t *testing.T) {
	s := testScheme(t)
	src := vsphereSource("vcenter", "tenant-foo")
	foreign := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      credentialsSecretName("vcenter"),
			Namespace: "tenant-foo",
		},
		Data: map[string][]byte{"something": []byte("precious")},
	}
	c := clientfake.NewClientBuilder().WithScheme(s).
		WithObjects(src, foreign).WithStatusSubresource(src).Build()

	r := &VMImportSourceReconciler{
		Client: c, Scheme: s, Recorder: record.NewFakeRecorder(10),
		VDDKImage: "registry.example.com/vddk:8.0.3",
	}
	if _, err := r.Reconcile(context.Background(), reconcile.Request{
		NamespacedName: types.NamespacedName{Namespace: "tenant-foo", Name: "vcenter"},
	}); err != nil {
		t.Fatalf("reconcile: %v", err)
	}

	kept := &corev1.Secret{}
	if err := c.Get(context.Background(), types.NamespacedName{
		Namespace: "tenant-foo", Name: credentialsSecretName("vcenter"),
	}, kept); err != nil {
		t.Fatalf("get secret: %v", err)
	}
	if string(kept.Data["something"]) != "precious" {
		t.Fatal("a pre-existing Secret was overwritten by the projector")
	}

	got := &migrationv1alpha1.VMImportSource{}
	if err := c.Get(context.Background(), types.NamespacedName{Namespace: "tenant-foo", Name: "vcenter"}, got); err != nil {
		t.Fatalf("get source: %v", err)
	}
	cond := meta.FindStatusCondition(got.Status.Conditions, migrationv1alpha1.SourceReadyCondition)
	if cond == nil || cond.Status != metav1.ConditionFalse {
		t.Fatal("expected the source to report itself not ready")
	}
	if !strings.Contains(cond.Message, "not managed by the migration controller") {
		t.Errorf("message does not explain the collision: %q", cond.Message)
	}
}

// TestSourceMirrorsForkliftVerdict asserts the Source reports Forklift's own
// conclusion rather than assuming a connection nobody tested.
func TestSourceMirrorsForkliftVerdict(t *testing.T) {
	cases := []struct {
		name       string
		conditions []interface{}
		wantReady  metav1.ConditionStatus
		wantInMsg  string
	}{
		{
			name:       "no conditions yet",
			conditions: nil,
			wantReady:  metav1.ConditionFalse,
			wantInMsg:  "waiting for Forklift",
		},
		{
			name: "forklift says ready",
			conditions: []interface{}{
				map[string]interface{}{"type": "Ready", "status": "True"},
			},
			wantReady: metav1.ConditionTrue,
		},
		{
			name: "forklift reports a critical problem",
			conditions: []interface{}{
				map[string]interface{}{
					"type": "ConnectionTestFailed", "status": "True",
					"category": "Critical", "message": "host is unreachable",
				},
			},
			wantReady: metav1.ConditionFalse,
			wantInMsg: "host is unreachable",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := testScheme(t)
			src := vsphereSource("vcenter", "tenant-foo")
			provider := newObject(providerGVK)
			provider.SetName(sourceProviderName("vcenter"))
			provider.SetNamespace("tenant-foo")
			if tc.conditions != nil {
				if err := unstructured.SetNestedSlice(provider.Object, tc.conditions, "status", "conditions"); err != nil {
					t.Fatalf("set conditions: %v", err)
				}
			}
			c := clientfake.NewClientBuilder().WithScheme(s).
				WithObjects(src, provider).WithStatusSubresource(src).Build()

			r := &VMImportSourceReconciler{
				Client: c, Scheme: s, Recorder: record.NewFakeRecorder(10),
				VDDKImage: "registry.example.com/vddk:8.0.3",
			}
			if _, err := r.Reconcile(context.Background(), reconcile.Request{
				NamespacedName: types.NamespacedName{Namespace: "tenant-foo", Name: "vcenter"},
			}); err != nil {
				t.Fatalf("reconcile: %v", err)
			}

			got := &migrationv1alpha1.VMImportSource{}
			if err := c.Get(context.Background(), types.NamespacedName{Namespace: "tenant-foo", Name: "vcenter"}, got); err != nil {
				t.Fatalf("get source: %v", err)
			}
			cond := meta.FindStatusCondition(got.Status.Conditions, migrationv1alpha1.SourceReadyCondition)
			if cond == nil {
				t.Fatal("expected a Ready condition")
			}
			if cond.Status != tc.wantReady {
				t.Errorf("Ready = %s, want %s (message %q)", cond.Status, tc.wantReady, cond.Message)
			}
			if tc.wantInMsg != "" && !strings.Contains(cond.Message, tc.wantInMsg) {
				t.Errorf("message = %q, want it to contain %q", cond.Message, tc.wantInMsg)
			}
		})
	}
}
