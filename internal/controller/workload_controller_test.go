package controller

import (
	"context"
	"testing"

	cozyv1alpha1 "github.com/cozystack/cozystack/api/v1alpha1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

func TestUnprefixedMonitoredObjectReturnsNil(t *testing.T) {
	w := &cozyv1alpha1.Workload{}
	w.Name = "unprefixed-name"
	obj := getMonitoredObject(w)
	if obj != nil {
		t.Errorf(`getMonitoredObject(&Workload{Name: "%s"}) == %v, want nil`, w.Name, obj)
	}
}

func TestPodMonitoredObject(t *testing.T) {
	w := &cozyv1alpha1.Workload{}
	w.Name = "pod-mypod"
	obj := getMonitoredObject(w)
	if pod, ok := obj.(*corev1.Pod); !ok || pod.Name != "mypod" {
		t.Errorf(`getMonitoredObject(&Workload{Name: "%s"}) == %v, want &Pod{Name: "mypod"}`, w.Name, obj)
	}
}

func TestWorkloadReconciler_DeletesOnMissingMonitor(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = cozyv1alpha1.AddToScheme(scheme)
	_ = corev1.AddToScheme(scheme)

	// Workload with a non-existent monitor
	w := &cozyv1alpha1.Workload{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "pod-foo",
			Namespace: "default",
			Labels: map[string]string{
				"workloads.cozystack.io/monitor": "missing-monitor",
			},
		},
	}

	fakeClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(w).
		Build()
	reconciler := &WorkloadReconciler{Client: fakeClient}
	req := reconcile.Request{NamespacedName: types.NamespacedName{Name: "pod-foo", Namespace: "default"}}

	if _, err := reconciler.Reconcile(context.TODO(), req); err != nil {
		t.Fatalf("Reconcile returned error: %v", err)
	}

	// Expect workload to be deleted
	err := fakeClient.Get(context.TODO(), req.NamespacedName, &cozyv1alpha1.Workload{})
	if !apierrors.IsNotFound(err) {
		t.Errorf("expected workload to be deleted, got: %v", err)
	}
}

func TestWorkloadReconciler_KeepsWhenAllExist(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = cozyv1alpha1.AddToScheme(scheme)
	_ = corev1.AddToScheme(scheme)

	// Create a monitor and its backing Pod
	monitor := &cozyv1alpha1.WorkloadMonitor{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "mon",
			Namespace: "default",
		},
	}
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "foo",
			Namespace: "default",
		},
	}
	w := &cozyv1alpha1.Workload{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "pod-foo",
			Namespace: "default",
			Labels: map[string]string{
				"workloads.cozystack.io/monitor": "mon",
			},
		},
	}

	fakeClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(monitor, pod, w).
		Build()
	reconciler := &WorkloadReconciler{Client: fakeClient}
	req := reconcile.Request{NamespacedName: types.NamespacedName{Name: "pod-foo", Namespace: "default"}}

	if _, err := reconciler.Reconcile(context.TODO(), req); err != nil {
		t.Fatalf("Reconcile returned error: %v", err)
	}

	// Expect workload to remain
	err := fakeClient.Get(context.TODO(), req.NamespacedName, &cozyv1alpha1.Workload{})
	if err != nil {
		t.Errorf("expected workload to persist, got error: %v", err)
	}
}

// A GPU usage record has no monitored object and sits under a monitor whose
// selector matches nothing: it stays exactly while its monitor exists,
// whatever the selector does or does not match.
func TestWorkloadReconciler_KeepsARecordWhoseMonitorSelectsNothing(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = cozyv1alpha1.AddToScheme(scheme)
	_ = corev1.AddToScheme(scheme)

	monitor := &cozyv1alpha1.WorkloadMonitor{
		ObjectMeta: metav1.ObjectMeta{Name: "gpu-devices", Namespace: "default"},
		Spec:       cozyv1alpha1.WorkloadMonitorSpec{Selector: map[string]string{"gpu.cozystack.io/usage-monitor": "true"}},
	}
	record := func() *cozyv1alpha1.Workload {
		return &cozyv1alpha1.Workload{
			ObjectMeta: metav1.ObjectMeta{Name: "gpu-train-0", Namespace: "default",
				Labels: map[string]string{"workloads.cozystack.io/monitor": "gpu-devices"}},
			Status: cozyv1alpha1.WorkloadStatus{Kind: "gpu-device"},
		}
	}
	req := reconcile.Request{NamespacedName: types.NamespacedName{Name: "gpu-train-0", Namespace: "default"}}

	kept := fake.NewClientBuilder().WithScheme(scheme).WithObjects(monitor, record()).Build()
	if _, err := (&WorkloadReconciler{Client: kept}).Reconcile(context.TODO(), req); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if err := kept.Get(context.TODO(), req.NamespacedName, &cozyv1alpha1.Workload{}); err != nil {
		t.Errorf("record under an existing monitor was deleted: %v", err)
	}

	orphaned := fake.NewClientBuilder().WithScheme(scheme).WithObjects(record()).Build()
	if _, err := (&WorkloadReconciler{Client: orphaned}).Reconcile(context.TODO(), req); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if err := orphaned.Get(context.TODO(), req.NamespacedName, &cozyv1alpha1.Workload{}); !apierrors.IsNotFound(err) {
		t.Errorf("record whose monitor is gone was kept: %v", err)
	}
}
