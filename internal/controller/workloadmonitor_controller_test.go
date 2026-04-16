package controller

import (
	"context"
	"testing"

	cozyv1alpha1 "github.com/cozystack/cozystack/api/v1alpha1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

func TestReconcile_OperationalStatusPersisted(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = cozyv1alpha1.AddToScheme(scheme)
	_ = corev1.AddToScheme(scheme)

	minReplicas := int32(2)
	monitor := &cozyv1alpha1.WorkloadMonitor{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-monitor",
			Namespace: "default",
		},
		Spec: cozyv1alpha1.WorkloadMonitorSpec{
			Selector:    map[string]string{"app": "test"},
			MinReplicas: &minReplicas,
		},
	}

	// Create one pod that is ready — availableReplicas=1 < minReplicas=2, so Operational should be false
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-pod-1",
			Namespace: "default",
			Labels:    map[string]string{"app": "test"},
		},
		Status: corev1.PodStatus{
			Conditions: []corev1.PodCondition{
				{Type: corev1.PodReady, Status: corev1.ConditionTrue},
			},
		},
	}

	fakeClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(monitor, pod).
		WithStatusSubresource(monitor).
		Build()

	reconciler := &WorkloadMonitorReconciler{Client: fakeClient, Scheme: scheme}
	req := reconcile.Request{NamespacedName: types.NamespacedName{Name: "test-monitor", Namespace: "default"}}

	_, err := reconciler.Reconcile(context.TODO(), req)
	if err != nil {
		t.Fatalf("Reconcile returned error: %v", err)
	}

	// Fetch the monitor back from fake client and check Operational is persisted
	updated := &cozyv1alpha1.WorkloadMonitor{}
	if err := fakeClient.Get(context.TODO(), req.NamespacedName, updated); err != nil {
		t.Fatalf("Failed to get updated WorkloadMonitor: %v", err)
	}

	if updated.Status.Operational == nil {
		t.Fatal("Expected Operational to be set, got nil")
	}
	if *updated.Status.Operational {
		t.Error("Expected Operational=false (1 available < 2 minReplicas), got true")
	}
	if updated.Status.ObservedReplicas != 1 {
		t.Errorf("Expected ObservedReplicas=1, got %d", updated.Status.ObservedReplicas)
	}
	if updated.Status.AvailableReplicas != 1 {
		t.Errorf("Expected AvailableReplicas=1, got %d", updated.Status.AvailableReplicas)
	}
}

func TestReconcile_OperationalTrue_WhenEnoughReplicas(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = cozyv1alpha1.AddToScheme(scheme)
	_ = corev1.AddToScheme(scheme)

	minReplicas := int32(1)
	monitor := &cozyv1alpha1.WorkloadMonitor{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-monitor",
			Namespace: "default",
		},
		Spec: cozyv1alpha1.WorkloadMonitorSpec{
			Selector:    map[string]string{"app": "test"},
			MinReplicas: &minReplicas,
		},
	}

	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-pod-1",
			Namespace: "default",
			Labels:    map[string]string{"app": "test"},
		},
		Status: corev1.PodStatus{
			Conditions: []corev1.PodCondition{
				{Type: corev1.PodReady, Status: corev1.ConditionTrue},
			},
		},
	}

	fakeClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(monitor, pod).
		WithStatusSubresource(monitor).
		Build()

	reconciler := &WorkloadMonitorReconciler{Client: fakeClient, Scheme: scheme}
	req := reconcile.Request{NamespacedName: types.NamespacedName{Name: "test-monitor", Namespace: "default"}}

	_, err := reconciler.Reconcile(context.TODO(), req)
	if err != nil {
		t.Fatalf("Reconcile returned error: %v", err)
	}

	updated := &cozyv1alpha1.WorkloadMonitor{}
	if err := fakeClient.Get(context.TODO(), req.NamespacedName, updated); err != nil {
		t.Fatalf("Failed to get updated WorkloadMonitor: %v", err)
	}

	if updated.Status.Operational == nil {
		t.Fatal("Expected Operational to be set, got nil")
	}
	if !*updated.Status.Operational {
		t.Error("Expected Operational=true (1 available >= 1 minReplicas), got false")
	}
}

func TestReconcile_OperationalTrue_WhenNoMinReplicas(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = cozyv1alpha1.AddToScheme(scheme)
	_ = corev1.AddToScheme(scheme)

	monitor := &cozyv1alpha1.WorkloadMonitor{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-monitor",
			Namespace: "default",
		},
		Spec: cozyv1alpha1.WorkloadMonitorSpec{
			Selector: map[string]string{"app": "test"},
			// No MinReplicas — should default to operational=true
		},
	}

	fakeClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(monitor).
		WithStatusSubresource(monitor).
		Build()

	reconciler := &WorkloadMonitorReconciler{Client: fakeClient, Scheme: scheme}
	req := reconcile.Request{NamespacedName: types.NamespacedName{Name: "test-monitor", Namespace: "default"}}

	_, err := reconciler.Reconcile(context.TODO(), req)
	if err != nil {
		t.Fatalf("Reconcile returned error: %v", err)
	}

	updated := &cozyv1alpha1.WorkloadMonitor{}
	if err := fakeClient.Get(context.TODO(), req.NamespacedName, updated); err != nil {
		t.Fatalf("Failed to get updated WorkloadMonitor: %v", err)
	}

	if updated.Status.Operational == nil {
		t.Fatal("Expected Operational to be set, got nil")
	}
	if !*updated.Status.Operational {
		t.Error("Expected Operational=true (no MinReplicas constraint), got false")
	}
}

