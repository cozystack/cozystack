// SPDX-License-Identifier: Apache-2.0
package lineagecontrollerwebhook

import (
	"context"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	"k8s.io/client-go/kubernetes/scheme"
	clientfake "sigs.k8s.io/controller-runtime/pkg/client/fake"

	schedulerapi "github.com/cozystack/cozystack-scheduler/pkg/apis/v1alpha1"
)

const testSchedulingClass = "co-region-test"

// newSchedulingClassCR builds an unstructured SchedulingClass CR the fake
// dynamic client can serve.
func newSchedulingClassCR(name string) *unstructured.Unstructured {
	u := &unstructured.Unstructured{}
	u.SetGroupVersionKind(schema.GroupVersionKind{
		Group:   schedulerapi.Group,
		Version: schedulerapi.Version,
		Kind:    "SchedulingClass",
	})
	u.SetName(name)
	return u
}

// newWebhookTestEnv builds a LineageControllerWebhook wired with fakes,
// pre-populated with a Namespace carrying the scheduling-class label and a
// SchedulingClass CR by that name.
func newWebhookTestEnv(t *testing.T, namespace string, classOnNamespace string, classExists bool) *LineageControllerWebhook {
	t.Helper()

	testScheme := runtime.NewScheme()
	_ = scheme.AddToScheme(testScheme)

	ns := &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{
			Name: namespace,
		},
	}
	if classOnNamespace != "" {
		ns.Labels = map[string]string{
			schedulerapi.SchedulingClassLabel: classOnNamespace,
		}
	}

	c := clientfake.NewClientBuilder().
		WithScheme(testScheme).
		WithObjects(ns).
		Build()

	gvr := schema.GroupVersionResource{
		Group:    schedulerapi.Group,
		Version:  schedulerapi.Version,
		Resource: schedulerapi.Resource,
	}

	dynObjs := []runtime.Object{}
	if classExists {
		dynObjs = append(dynObjs, newSchedulingClassCR(classOnNamespace))
	}
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(
		testScheme,
		map[schema.GroupVersionResource]string{gvr: "SchedulingClassList"},
		dynObjs...,
	)

	return &LineageControllerWebhook{
		Client:    c,
		Scheme:    testScheme,
		dynClient: dyn,
	}
}

// newPod returns a minimal unstructured Pod for mutation tests.
func newPod(namespace, name string) *unstructured.Unstructured {
	u := &unstructured.Unstructured{}
	u.SetGroupVersionKind(schema.GroupVersionKind{Version: "v1", Kind: "Pod"})
	u.SetNamespace(namespace)
	u.SetName(name)
	return u
}

func TestApplySchedulingClass_SetsLabelAnnotationAndSchedulerName(t *testing.T) {
	ctx := context.Background()
	h := newWebhookTestEnv(t, "tenant-acme", testSchedulingClass, true)
	pod := newPod("tenant-acme", "db-0")

	if err := h.applySchedulingClass(ctx, pod, nil, "tenant-acme"); err != nil {
		t.Fatalf("applySchedulingClass returned error: %v", err)
	}

	gotLabel := pod.GetLabels()[schedulerapi.SchedulingClassLabel]
	if gotLabel != testSchedulingClass {
		t.Errorf("expected pod label %s=%s, got %q", schedulerapi.SchedulingClassLabel, testSchedulingClass, gotLabel)
	}

	gotAnnotation := pod.GetAnnotations()[schedulerapi.SchedulingClassAnnotation]
	if gotAnnotation != testSchedulingClass {
		t.Errorf("expected pod annotation %s=%s, got %q", schedulerapi.SchedulingClassAnnotation, testSchedulingClass, gotAnnotation)
	}

	gotSched, _, _ := unstructured.NestedString(pod.Object, "spec", "schedulerName")
	if gotSched != schedulerapi.SchedulerName {
		t.Errorf("expected pod spec.schedulerName=%s, got %q", schedulerapi.SchedulerName, gotSched)
	}
}

func TestApplySchedulingClass_PreservesExistingLabels(t *testing.T) {
	ctx := context.Background()
	h := newWebhookTestEnv(t, "tenant-acme", testSchedulingClass, true)
	pod := newPod("tenant-acme", "db-0")
	pod.SetLabels(map[string]string{
		"app.kubernetes.io/name": "postgres",
		"role":                   "primary",
	})

	if err := h.applySchedulingClass(ctx, pod, nil, "tenant-acme"); err != nil {
		t.Fatalf("applySchedulingClass returned error: %v", err)
	}

	labels := pod.GetLabels()
	if labels[schedulerapi.SchedulingClassLabel] != testSchedulingClass {
		t.Errorf("scheduling-class label not set")
	}
	if labels["app.kubernetes.io/name"] != "postgres" {
		t.Errorf("existing label app.kubernetes.io/name was clobbered: %q", labels["app.kubernetes.io/name"])
	}
	if labels["role"] != "primary" {
		t.Errorf("existing label role was clobbered: %q", labels["role"])
	}
}

func TestApplySchedulingClass_NonPodIsNoOp(t *testing.T) {
	ctx := context.Background()
	h := newWebhookTestEnv(t, "tenant-acme", testSchedulingClass, true)

	svc := &unstructured.Unstructured{}
	svc.SetGroupVersionKind(schema.GroupVersionKind{Version: "v1", Kind: "Service"})
	svc.SetNamespace("tenant-acme")
	svc.SetName("svc-0")

	if err := h.applySchedulingClass(ctx, svc, nil, "tenant-acme"); err != nil {
		t.Fatalf("applySchedulingClass returned error: %v", err)
	}

	if _, ok := svc.GetLabels()[schedulerapi.SchedulingClassLabel]; ok {
		t.Errorf("non-Pod object should not be mutated")
	}
	if _, ok := svc.GetAnnotations()[schedulerapi.SchedulingClassAnnotation]; ok {
		t.Errorf("non-Pod object should not be mutated")
	}
}

func TestApplySchedulingClass_NoClassOnNamespaceIsNoOp(t *testing.T) {
	ctx := context.Background()
	h := newWebhookTestEnv(t, "tenant-acme", "", false)
	pod := newPod("tenant-acme", "db-0")

	if err := h.applySchedulingClass(ctx, pod, nil, "tenant-acme"); err != nil {
		t.Fatalf("applySchedulingClass returned error: %v", err)
	}

	if _, ok := pod.GetLabels()[schedulerapi.SchedulingClassLabel]; ok {
		t.Errorf("pod label should not be set when namespace has no scheduling-class label")
	}
	if _, ok := pod.GetAnnotations()[schedulerapi.SchedulingClassAnnotation]; ok {
		t.Errorf("pod annotation should not be set when namespace has no scheduling-class label")
	}
	if sched, _, _ := unstructured.NestedString(pod.Object, "spec", "schedulerName"); sched != "" {
		t.Errorf("pod spec.schedulerName should not be set when namespace has no scheduling-class label, got %q", sched)
	}
}

func TestApplySchedulingClass_MissingClassCRSkipsInjection(t *testing.T) {
	ctx := context.Background()
	// Namespace carries the label, but the SchedulingClass CR is NOT created.
	h := newWebhookTestEnv(t, "tenant-acme", testSchedulingClass, false)
	pod := newPod("tenant-acme", "db-0")

	if err := h.applySchedulingClass(ctx, pod, nil, "tenant-acme"); err != nil {
		t.Fatalf("applySchedulingClass returned error: %v", err)
	}

	// Defensive behavior: pod must not be left referencing a non-existent
	// scheduler, otherwise it would stay Pending forever.
	if _, ok := pod.GetLabels()[schedulerapi.SchedulingClassLabel]; ok {
		t.Errorf("pod label should not be set when SchedulingClass CR is missing")
	}
	if _, ok := pod.GetAnnotations()[schedulerapi.SchedulingClassAnnotation]; ok {
		t.Errorf("pod annotation should not be set when SchedulingClass CR is missing")
	}
	if sched, _, _ := unstructured.NestedString(pod.Object, "spec", "schedulerName"); sched != "" {
		t.Errorf("pod spec.schedulerName should not be set when SchedulingClass CR is missing, got %q", sched)
	}
}
