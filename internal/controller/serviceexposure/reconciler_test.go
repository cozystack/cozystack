/*
Copyright 2026 The Cozystack Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package serviceexposure

import (
	"context"
	"testing"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	networkv1alpha1 "github.com/cozystack/cozystack/api/network/v1alpha1"
	"github.com/cozystack/cozystack/internal/controller/serviceexposure/backend"
)

func newScheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	s := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(s); err != nil {
		t.Fatal(err)
	}
	if err := networkv1alpha1.AddToScheme(s); err != nil {
		t.Fatal(err)
	}
	for _, gvk := range backend.ManagedGVKs() {
		s.AddKnownTypeWithName(gvk, &unstructured.Unstructured{})
		listGVK := gvk
		listGVK.Kind += "List"
		s.AddKnownTypeWithName(listGVK, &unstructured.UnstructuredList{})
	}
	return s
}

func defaultClass(backendName networkv1alpha1.ExposureBackend, addrs ...string) *networkv1alpha1.ExposureClass {
	return &networkv1alpha1.ExposureClass{
		ObjectMeta: metav1.ObjectMeta{
			Name:        "default",
			Annotations: map[string]string{networkv1alpha1.IsDefaultExposureClassAnnotation: "true"},
		},
		Spec: networkv1alpha1.ExposureClassSpec{Backend: backendName, Addresses: addrs},
	}
}

func ingressService() *corev1.Service {
	return &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{Name: "root-ingress", Namespace: "tenant-root"},
		Spec:       corev1.ServiceSpec{Type: corev1.ServiceTypeLoadBalancer},
	}
}

func exposure(className string) *networkv1alpha1.ServiceExposure {
	return &networkv1alpha1.ServiceExposure{
		ObjectMeta: metav1.ObjectMeta{Name: "root-ingress", Namespace: "tenant-root"},
		Spec: networkv1alpha1.ServiceExposureSpec{
			ServiceRef:        corev1.LocalObjectReference{Name: "root-ingress"},
			ExposureClassName: className,
		},
	}
}

// reconcileOnce drives a single Reconcile (which adds the finalizer and
// runs the desired-state work in one pass) and returns its error. Negative
// paths legitimately return an error to trigger requeue while still
// writing status, so callers assert on status rather than on the error.
func reconcileOnce(r *Reconciler, exp *networkv1alpha1.ServiceExposure) error {
	key := types.NamespacedName{Namespace: exp.Namespace, Name: exp.Name}
	_, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: key})
	return err
}

func getExposure(t *testing.T, c client.Client, ns, name string) *networkv1alpha1.ServiceExposure {
	t.Helper()
	exp := &networkv1alpha1.ServiceExposure{}
	if err := c.Get(context.Background(), types.NamespacedName{Namespace: ns, Name: name}, exp); err != nil {
		t.Fatalf("get exposure: %v", err)
	}
	return exp
}

func readyCondition(exp *networkv1alpha1.ServiceExposure) *metav1.Condition {
	return apimeta.FindStatusCondition(exp.Status.Conditions, "Ready")
}

func getUnstructured(t *testing.T, c client.Client, gvk, name, namespace string) (*unstructured.Unstructured, error) {
	t.Helper()
	obj := &unstructured.Unstructured{}
	parts := splitGVK(gvk)
	obj.SetGroupVersionKind(parts)
	err := c.Get(context.Background(), types.NamespacedName{Namespace: namespace, Name: name}, obj)
	return obj, err
}

// splitGVK maps a test shorthand to a GroupVersionKind.
func splitGVK(s string) schema.GroupVersionKind {
	switch s {
	case "metallb/IPAddressPool":
		return schema.GroupVersionKind{Group: "metallb.io", Version: "v1beta1", Kind: "IPAddressPool"}
	case "metallb/L2Advertisement":
		return schema.GroupVersionKind{Group: "metallb.io", Version: "v1beta1", Kind: "L2Advertisement"}
	case "cilium/Pool":
		return schema.GroupVersionKind{Group: "cilium.io", Version: "v2", Kind: "CiliumLoadBalancerIPPool"}
	case "cilium/L2":
		return schema.GroupVersionKind{Group: "cilium.io", Version: "v2alpha1", Kind: "CiliumL2AnnouncementPolicy"}
	}
	return schema.GroupVersionKind{}
}

func TestReconcile_ClassNotFound(t *testing.T) {
	s := newScheme(t)
	exp := exposure("") // no class, no default exists
	c := fake.NewClientBuilder().WithScheme(s).
		WithObjects(exp, ingressService()).
		WithStatusSubresource(&networkv1alpha1.ServiceExposure{}).
		Build()
	r := &Reconciler{Client: c, Scheme: s}
	_ = reconcileOnce(r, exp)

	got := getExposure(t, c, "tenant-root", "root-ingress")
	cond := readyCondition(got)
	if cond == nil || cond.Status != metav1.ConditionFalse || cond.Reason != "ClassNotFound" {
		t.Fatalf("want Ready=False/ClassNotFound, got %+v", cond)
	}
}

func TestReconcile_ServiceNotFound(t *testing.T) {
	s := newScheme(t)
	exp := exposure("")
	c := fake.NewClientBuilder().WithScheme(s).
		WithObjects(exp, defaultClass(networkv1alpha1.BackendMetalLB, "192.0.2.0/24")).
		WithStatusSubresource(&networkv1alpha1.ServiceExposure{}).
		Build()
	r := &Reconciler{Client: c, Scheme: s}
	_ = reconcileOnce(r, exp)

	cond := readyCondition(getExposure(t, c, "tenant-root", "root-ingress"))
	if cond == nil || cond.Reason != "ServiceNotFound" {
		t.Fatalf("want Ready=False/ServiceNotFound, got %+v", cond)
	}
}

func TestReconcile_MetalLBHappyPath(t *testing.T) {
	s := newScheme(t)
	exp := exposure("")
	c := fake.NewClientBuilder().WithScheme(s).
		WithObjects(exp, ingressService(), defaultClass(networkv1alpha1.BackendMetalLB, "192.0.2.0/24")).
		WithStatusSubresource(&networkv1alpha1.ServiceExposure{}).
		Build()
	r := &Reconciler{Client: c, Scheme: s}
	_ = reconcileOnce(r, exp)

	pool, err := getUnstructured(t, c, "metallb/IPAddressPool", "cozystack-default", "cozy-metallb")
	if err != nil {
		t.Fatalf("IPAddressPool not created: %v", err)
	}
	if pool.GetLabels()[managedByLabel] != managedByValue {
		t.Errorf("pool missing managed-by label")
	}
	if pool.GetAnnotations()[ownerClassAnnotation] != "default" {
		t.Errorf("pool missing owner-class annotation")
	}
	if _, err := getUnstructured(t, c, "metallb/L2Advertisement", "cozystack-default", "cozy-metallb"); err != nil {
		t.Fatalf("L2Advertisement not created: %v", err)
	}

	got := getExposure(t, c, "tenant-root", "root-ingress")
	if got.Status.ResolvedBackend != "metallb" {
		t.Errorf("resolvedBackend = %q", got.Status.ResolvedBackend)
	}
	cond := readyCondition(got)
	if cond == nil || cond.Status != metav1.ConditionFalse || cond.Reason != "AwaitingAllocation" {
		t.Fatalf("want Ready=False/AwaitingAllocation before IP lands, got %+v", cond)
	}

	// IP lands → Ready=True, AssignedIPs populated.
	svc := ingressService()
	if err := c.Get(context.Background(), types.NamespacedName{Namespace: "tenant-root", Name: "root-ingress"}, svc); err != nil {
		t.Fatal(err)
	}
	svc.Status.LoadBalancer.Ingress = []corev1.LoadBalancerIngress{{IP: "192.0.2.5"}}
	if err := c.Status().Update(context.Background(), svc); err != nil {
		t.Fatal(err)
	}
	_ = reconcileOnce(r, exp)
	got = getExposure(t, c, "tenant-root", "root-ingress")
	cond = readyCondition(got)
	if cond == nil || cond.Status != metav1.ConditionTrue {
		t.Fatalf("want Ready=True after IP lands, got %+v", cond)
	}
	if len(got.Status.AssignedIPs) != 1 || got.Status.AssignedIPs[0] != "192.0.2.5" {
		t.Errorf("assignedIPs = %v", got.Status.AssignedIPs)
	}
}

func TestReconcile_ExternalIPsRendersNoObjects(t *testing.T) {
	s := newScheme(t)
	exp := exposure("")
	svc := ingressService()
	svc.Spec.Type = corev1.ServiceTypeClusterIP
	svc.Spec.ExternalIPs = []string{"203.0.113.10"}
	c := fake.NewClientBuilder().WithScheme(s).
		WithObjects(exp, svc, defaultClass(networkv1alpha1.BackendExternalIPs)).
		WithStatusSubresource(&networkv1alpha1.ServiceExposure{}).
		Build()
	r := &Reconciler{Client: c, Scheme: s}
	_ = reconcileOnce(r, exp)

	if _, err := getUnstructured(t, c, "metallb/IPAddressPool", "cozystack-default", "cozy-metallb"); !apierrors.IsNotFound(err) {
		t.Errorf("externalIPs backend must render no IPAddressPool, got err=%v", err)
	}
	cond := readyCondition(getExposure(t, c, "tenant-root", "root-ingress"))
	if cond == nil || cond.Status != metav1.ConditionTrue {
		t.Fatalf("externalIPs with pinned IPs should be Ready=True, got %+v", cond)
	}
}

func TestReconcile_BackendSwitchGCsOldCRs(t *testing.T) {
	s := newScheme(t)
	exp := exposure("")
	class := defaultClass(networkv1alpha1.BackendMetalLB, "192.0.2.0/24")
	c := fake.NewClientBuilder().WithScheme(s).
		WithObjects(exp, ingressService(), class).
		WithStatusSubresource(&networkv1alpha1.ServiceExposure{}).
		Build()
	r := &Reconciler{Client: c, Scheme: s}
	_ = reconcileOnce(r, exp)
	if _, err := getUnstructured(t, c, "metallb/IPAddressPool", "cozystack-default", "cozy-metallb"); err != nil {
		t.Fatalf("metallb pool should exist: %v", err)
	}

	// Flip the class to cilium and reconcile.
	cur := &networkv1alpha1.ExposureClass{}
	if err := c.Get(context.Background(), types.NamespacedName{Name: "default"}, cur); err != nil {
		t.Fatal(err)
	}
	cur.Spec.Backend = networkv1alpha1.BackendCilium
	cur.Spec.Addresses = []string{"192.0.2.0/24"}
	if err := c.Update(context.Background(), cur); err != nil {
		t.Fatal(err)
	}
	_ = reconcileOnce(r, exp)

	if _, err := getUnstructured(t, c, "metallb/IPAddressPool", "cozystack-default", "cozy-metallb"); !apierrors.IsNotFound(err) {
		t.Errorf("metallb pool should be GC'd after switch, got err=%v", err)
	}
	if _, err := getUnstructured(t, c, "cilium/Pool", "cozystack-default", ""); err != nil {
		t.Errorf("cilium pool should exist after switch: %v", err)
	}
}

func TestReconcile_RefusesTakeoverOfUnmanagedCR(t *testing.T) {
	s := newScheme(t)
	exp := exposure("")
	// Pre-existing, operator-owned IPAddressPool with the same name.
	foreign := &unstructured.Unstructured{}
	foreign.SetGroupVersionKind(splitGVK("metallb/IPAddressPool"))
	foreign.SetNamespace("cozy-metallb")
	foreign.SetName("cozystack-default")
	foreign.Object["spec"] = map[string]interface{}{"addresses": []interface{}{"10.0.0.0/24"}}

	c := fake.NewClientBuilder().WithScheme(s).
		WithObjects(exp, ingressService(), defaultClass(networkv1alpha1.BackendMetalLB, "192.0.2.0/24"), foreign).
		WithStatusSubresource(&networkv1alpha1.ServiceExposure{}).
		Build()
	r := &Reconciler{Client: c, Scheme: s}

	if err := reconcileOnce(r, exp); err == nil {
		t.Fatal("expected reconcile error on take-over refusal")
	}

	got, err := getUnstructured(t, c, "metallb/IPAddressPool", "cozystack-default", "cozy-metallb")
	if err != nil {
		t.Fatal(err)
	}
	addrs, _, _ := unstructured.NestedStringSlice(got.Object, "spec", "addresses")
	if len(addrs) != 1 || addrs[0] != "10.0.0.0/24" {
		t.Errorf("foreign pool must be left untouched, got addresses %v", addrs)
	}
	cond := readyCondition(getExposure(t, c, "tenant-root", "root-ingress"))
	if cond == nil || cond.Reason != "ResourceConflict" {
		t.Fatalf("want Ready=False/ResourceConflict, got %+v", cond)
	}
}

func TestReconcile_SecondExposureSharesOnePool(t *testing.T) {
	// Two ServiceExposures in different namespaces resolving to one
	// metallb class must converge on a SINGLE pool (no overlapping pools),
	// scoped to BOTH namespaces. This is the multi-consumer case the
	// class-level model exists to make correct.
	s := newScheme(t)
	class := defaultClass(networkv1alpha1.BackendMetalLB, "192.0.2.0/24")
	expA := exposure("")
	svcA := ingressService()
	expB := &networkv1alpha1.ServiceExposure{
		ObjectMeta: metav1.ObjectMeta{Name: "other-ingress", Namespace: "tenant-other"},
		Spec:       networkv1alpha1.ServiceExposureSpec{ServiceRef: corev1.LocalObjectReference{Name: "other-ingress"}},
	}
	svcB := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{Name: "other-ingress", Namespace: "tenant-other"},
		Spec:       corev1.ServiceSpec{Type: corev1.ServiceTypeLoadBalancer},
	}
	c := fake.NewClientBuilder().WithScheme(s).
		WithObjects(class, expA, svcA, expB, svcB).
		WithStatusSubresource(&networkv1alpha1.ServiceExposure{}).
		Build()
	r := &Reconciler{Client: c, Scheme: s}
	_ = reconcileOnce(r, expA)
	_ = reconcileOnce(r, expB)

	pools := &unstructured.UnstructuredList{}
	pools.SetGroupVersionKind(schema.GroupVersionKind{Group: "metallb.io", Version: "v1beta1", Kind: "IPAddressPoolList"})
	if err := c.List(context.Background(), pools); err != nil {
		t.Fatal(err)
	}
	if len(pools.Items) != 1 {
		t.Fatalf("want exactly 1 shared pool, got %d", len(pools.Items))
	}
	nsAlloc, _, _ := unstructured.NestedStringSlice(pools.Items[0].Object, "spec", "serviceAllocation", "namespaces")
	if len(nsAlloc) != 2 || nsAlloc[0] != "tenant-other" || nsAlloc[1] != "tenant-root" {
		t.Errorf("shared pool must be scoped to both namespaces (sorted), got %v", nsAlloc)
	}
}

func TestReconcile_PreservesServerDefaultsAndIsIdempotent(t *testing.T) {
	// The apiserver adds CRD-defaulted keys the backend never sets
	// (metallb IPAddressPool autoAssign/avoidBuggyIPs). A whole-spec
	// compare would rewrite the pool every reconcile and strip them; the
	// subset merge must leave them intact and issue no Update.
	s := newScheme(t)
	exp := exposure("")
	c := fake.NewClientBuilder().WithScheme(s).
		WithObjects(exp, ingressService(), defaultClass(networkv1alpha1.BackendMetalLB, "192.0.2.0/24")).
		WithStatusSubresource(&networkv1alpha1.ServiceExposure{}).
		Build()
	r := &Reconciler{Client: c, Scheme: s}
	_ = reconcileOnce(r, exp)

	// Simulate the apiserver writing a defaulted key onto the pool.
	pool, err := getUnstructured(t, c, "metallb/IPAddressPool", "cozystack-default", "cozy-metallb")
	if err != nil {
		t.Fatal(err)
	}
	_ = unstructured.SetNestedField(pool.Object, true, "spec", "autoAssign")
	if err := c.Update(context.Background(), pool); err != nil {
		t.Fatal(err)
	}
	pool, _ = getUnstructured(t, c, "metallb/IPAddressPool", "cozystack-default", "cozy-metallb")
	rvBefore := pool.GetResourceVersion()

	// Reconcile again: managed keys are unchanged, so no Update must fire
	// and the server default must survive.
	_ = reconcileOnce(r, exp)
	pool, err = getUnstructured(t, c, "metallb/IPAddressPool", "cozystack-default", "cozy-metallb")
	if err != nil {
		t.Fatal(err)
	}
	if pool.GetResourceVersion() != rvBefore {
		t.Errorf("pool was rewritten on a no-op reconcile (rv %s → %s)", rvBefore, pool.GetResourceVersion())
	}
	if auto, _, _ := unstructured.NestedBool(pool.Object, "spec", "autoAssign"); !auto {
		t.Error("subset merge stripped the server-defaulted spec.autoAssign")
	}
}

func TestReconcile_ClearsRemovedManagedKeyButKeepsServerDefaults(t *testing.T) {
	// Clearing class.spec.interfaces must remove interfaces from the
	// L2Advertisement (a managed key absent from the fresh render), while
	// the apiserver-defaulted pool key (autoAssign) must survive.
	s := newScheme(t)
	class := &networkv1alpha1.ExposureClass{
		ObjectMeta: metav1.ObjectMeta{
			Name:        "default",
			Annotations: map[string]string{networkv1alpha1.IsDefaultExposureClassAnnotation: "true"},
		},
		Spec: networkv1alpha1.ExposureClassSpec{
			Backend:    networkv1alpha1.BackendMetalLB,
			Addresses:  []string{"192.0.2.0/24"},
			Interfaces: []string{"eth0"},
		},
	}
	exp := exposure("")
	c := fake.NewClientBuilder().WithScheme(s).
		WithObjects(class, exp, ingressService()).
		WithStatusSubresource(&networkv1alpha1.ServiceExposure{}).
		Build()
	r := &Reconciler{Client: c, Scheme: s}
	_ = reconcileOnce(r, exp)

	l2, err := getUnstructured(t, c, "metallb/L2Advertisement", "cozystack-default", "cozy-metallb")
	if err != nil {
		t.Fatal(err)
	}
	if ifaces, _, _ := unstructured.NestedStringSlice(l2.Object, "spec", "interfaces"); len(ifaces) != 1 {
		t.Fatalf("precondition: L2Advertisement should have interfaces, got %v", ifaces)
	}
	// Server defaults a key on the pool.
	pool, _ := getUnstructured(t, c, "metallb/IPAddressPool", "cozystack-default", "cozy-metallb")
	_ = unstructured.SetNestedField(pool.Object, true, "spec", "autoAssign")
	if err := c.Update(context.Background(), pool); err != nil {
		t.Fatal(err)
	}

	// Operator clears interfaces.
	cur := &networkv1alpha1.ExposureClass{}
	if err := c.Get(context.Background(), types.NamespacedName{Name: "default"}, cur); err != nil {
		t.Fatal(err)
	}
	cur.Spec.Interfaces = nil
	if err := c.Update(context.Background(), cur); err != nil {
		t.Fatal(err)
	}
	_ = reconcileOnce(r, exp)

	l2, err = getUnstructured(t, c, "metallb/L2Advertisement", "cozystack-default", "cozy-metallb")
	if err != nil {
		t.Fatal(err)
	}
	if _, found, _ := unstructured.NestedSlice(l2.Object, "spec", "interfaces"); found {
		t.Error("cleared class.spec.interfaces did not propagate; L2Advertisement still has interfaces")
	}
	pool, err = getUnstructured(t, c, "metallb/IPAddressPool", "cozystack-default", "cozy-metallb")
	if err != nil {
		t.Fatal(err)
	}
	if auto, _, _ := unstructured.NestedBool(pool.Object, "spec", "autoAssign"); !auto {
		t.Error("server-defaulted spec.autoAssign was stripped while removing a managed key")
	}
}

func TestClassReconciler_DeletionGCsBackendCRs(t *testing.T) {
	s := newScheme(t)
	class := defaultClass(networkv1alpha1.BackendMetalLB, "192.0.2.0/24")
	exp := exposure("")
	c := fake.NewClientBuilder().WithScheme(s).
		WithObjects(class, exp, ingressService()).
		WithStatusSubresource(&networkv1alpha1.ServiceExposure{}).
		Build()
	r := &Reconciler{Client: c, Scheme: s}
	cr := &ClassReconciler{Client: c, Scheme: s}
	classKey := types.NamespacedName{Name: "default"}

	// Class controller stamps its finalizer; exposure controller renders.
	if _, err := cr.Reconcile(context.Background(), ctrl.Request{NamespacedName: classKey}); err != nil {
		t.Fatal(err)
	}
	_ = reconcileOnce(r, exp)
	if _, err := getUnstructured(t, c, "metallb/IPAddressPool", "cozystack-default", "cozy-metallb"); err != nil {
		t.Fatalf("pool should exist: %v", err)
	}

	// Delete the ExposureClass — its finalizer keeps it until the class
	// controller reclaims the orphan-prone pool.
	cur := &networkv1alpha1.ExposureClass{}
	if err := c.Get(context.Background(), classKey, cur); err != nil {
		t.Fatal(err)
	}
	if err := c.Delete(context.Background(), cur); err != nil {
		t.Fatal(err)
	}
	if _, err := cr.Reconcile(context.Background(), ctrl.Request{NamespacedName: classKey}); err != nil {
		t.Fatal(err)
	}
	if _, err := getUnstructured(t, c, "metallb/IPAddressPool", "cozystack-default", "cozy-metallb"); !apierrors.IsNotFound(err) {
		t.Errorf("pool must be GC'd on class deletion, got err=%v", err)
	}
}

func TestReconcile_DeletionKeepsFinalizerOnTransientClassError(t *testing.T) {
	// A transient (non-NotFound) error resolving the class during deletion
	// must NOT drop the finalizer — otherwise the exposure vanishes without
	// recomputing the shared pool, leaking it.
	s := newScheme(t)
	exp := exposure("gold")
	exp.Finalizers = []string{exposureFinalizer}
	c := fake.NewClientBuilder().WithScheme(s).
		WithObjects(exp, ingressService()).
		WithStatusSubresource(&networkv1alpha1.ServiceExposure{}).
		WithInterceptorFuncs(interceptor.Funcs{
			Get: func(ctx context.Context, cl client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
				if _, ok := obj.(*networkv1alpha1.ExposureClass); ok {
					return apierrors.NewServiceUnavailable("apiserver hiccup")
				}
				return cl.Get(ctx, key, obj, opts...)
			},
		}).
		Build()
	r := &Reconciler{Client: c, Scheme: s}

	// Put the exposure into deletion.
	cur := getExposure(t, c, "tenant-root", "root-ingress")
	if err := c.Delete(context.Background(), cur); err != nil {
		t.Fatal(err)
	}
	if err := reconcileOnce(r, exp); err == nil {
		t.Fatal("expected requeue error on transient class-resolve failure during deletion")
	}
	// The exposure must still exist with its finalizer intact.
	got := &networkv1alpha1.ServiceExposure{}
	if err := c.Get(context.Background(), types.NamespacedName{Namespace: "tenant-root", Name: "root-ingress"}, got); err != nil {
		t.Fatalf("exposure must still exist (finalizer retained): %v", err)
	}
	if !controllerutil.ContainsFinalizer(got, exposureFinalizer) {
		t.Error("finalizer was dropped on a transient error")
	}
}

func TestReconcile_DeletionUnwedgesMisconfiguredClass(t *testing.T) {
	// A pool backend with no addresses makes Desired error. Deleting the
	// last exposure of such a class must still drop the finalizer (empty
	// namespaces ⇒ nothing to render), not hang in Terminating forever.
	for _, be := range []networkv1alpha1.ExposureBackend{networkv1alpha1.BackendMetalLB, networkv1alpha1.BackendCilium} {
		s := newScheme(t)
		class := defaultClass(be) // no addresses → Desired errors when namespaces non-empty
		exp := exposure("")
		exp.Finalizers = []string{exposureFinalizer}
		c := fake.NewClientBuilder().WithScheme(s).
			WithObjects(class, exp, ingressService()).
			WithStatusSubresource(&networkv1alpha1.ServiceExposure{}).
			Build()
		r := &Reconciler{Client: c, Scheme: s}

		cur := getExposure(t, c, "tenant-root", "root-ingress")
		if err := c.Delete(context.Background(), cur); err != nil {
			t.Fatal(err)
		}
		if err := reconcileOnce(r, exp); err != nil {
			t.Fatalf("backend %s: deletion must not wedge, got %v", be, err)
		}
		if err := c.Get(context.Background(), types.NamespacedName{Namespace: "tenant-root", Name: "root-ingress"}, &networkv1alpha1.ServiceExposure{}); !apierrors.IsNotFound(err) {
			t.Errorf("backend %s: exposure should be gone after delete, got err=%v", be, err)
		}
	}
}

func TestReconcile_DeletionUnwedgesWhenOtherExposuresKeepMisconfiguredClass(t *testing.T) {
	// Deleting a non-last exposure of a misconfigured (address-less) class:
	// other exposures keep the namespace set non-empty, so Desired still
	// errors. The deleting exposure must NOT wedge — no pool could have
	// been created in this state, so removing it is safe.
	s := newScheme(t)
	class := defaultClass(networkv1alpha1.BackendMetalLB) // no addresses
	expA := exposure("")
	expA.Finalizers = []string{exposureFinalizer}
	expB := &networkv1alpha1.ServiceExposure{
		ObjectMeta: metav1.ObjectMeta{Name: "other-ingress", Namespace: "tenant-other", Finalizers: []string{exposureFinalizer}},
		Spec:       networkv1alpha1.ServiceExposureSpec{ServiceRef: corev1.LocalObjectReference{Name: "other-ingress"}},
	}
	c := fake.NewClientBuilder().WithScheme(s).
		WithObjects(class, expA, ingressService(), expB).
		WithStatusSubresource(&networkv1alpha1.ServiceExposure{}).
		Build()
	r := &Reconciler{Client: c, Scheme: s}

	cur := getExposure(t, c, "tenant-root", "root-ingress")
	if err := c.Delete(context.Background(), cur); err != nil {
		t.Fatal(err)
	}
	if err := reconcileOnce(r, expA); err != nil {
		t.Fatalf("deletion must not wedge on a misconfigured class with peers, got %v", err)
	}
	if err := c.Get(context.Background(), types.NamespacedName{Namespace: "tenant-root", Name: "root-ingress"}, &networkv1alpha1.ServiceExposure{}); !apierrors.IsNotFound(err) {
		t.Errorf("deleted exposure should be gone, got err=%v", err)
	}
	// The surviving exposure is untouched.
	if err := c.Get(context.Background(), types.NamespacedName{Namespace: "tenant-other", Name: "other-ingress"}, &networkv1alpha1.ServiceExposure{}); err != nil {
		t.Errorf("surviving exposure must remain, got %v", err)
	}
}

func TestReconcile_DeletionReclaimsStalePoolWhenClassMisconfigured(t *testing.T) {
	// A class that WAS valid (pool created, scoped to both namespaces) then
	// had its addresses cleared while exposures are live. Deleting one
	// exposure must not leave the pool frozen with the deleted namespace
	// still in its scope (isolation leak) — the controller reclaims the
	// now-invalid pool entirely.
	s := newScheme(t)
	class := defaultClass(networkv1alpha1.BackendMetalLB, "192.0.2.0/24")
	expA := exposure("")
	expA.Finalizers = []string{exposureFinalizer}
	expB := &networkv1alpha1.ServiceExposure{
		ObjectMeta: metav1.ObjectMeta{Name: "other-ingress", Namespace: "tenant-other", Finalizers: []string{exposureFinalizer}},
		Spec:       networkv1alpha1.ServiceExposureSpec{ServiceRef: corev1.LocalObjectReference{Name: "other-ingress"}},
	}
	svcB := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{Name: "other-ingress", Namespace: "tenant-other"},
		Spec:       corev1.ServiceSpec{Type: corev1.ServiceTypeLoadBalancer},
	}
	c := fake.NewClientBuilder().WithScheme(s).
		WithObjects(class, expA, ingressService(), expB, svcB).
		WithStatusSubresource(&networkv1alpha1.ServiceExposure{}).
		Build()
	r := &Reconciler{Client: c, Scheme: s}
	_ = reconcileOnce(r, expA)
	_ = reconcileOnce(r, expB)
	if _, err := getUnstructured(t, c, "metallb/IPAddressPool", "cozystack-default", "cozy-metallb"); err != nil {
		t.Fatalf("precondition: pool should exist while class is valid: %v", err)
	}

	// Operator clears addresses → class can no longer render.
	cur := &networkv1alpha1.ExposureClass{}
	if err := c.Get(context.Background(), types.NamespacedName{Name: "default"}, cur); err != nil {
		t.Fatal(err)
	}
	cur.Spec.Addresses = nil
	if err := c.Update(context.Background(), cur); err != nil {
		t.Fatal(err)
	}

	// Delete the tenant-root exposure.
	del := getExposure(t, c, "tenant-root", "root-ingress")
	if err := c.Delete(context.Background(), del); err != nil {
		t.Fatal(err)
	}
	if err := reconcileOnce(r, expA); err != nil {
		t.Fatalf("deletion must not wedge, got %v", err)
	}

	// The stale pool must be gone (no leftover scope on tenant-root), and
	// the exposure removed.
	if _, err := getUnstructured(t, c, "metallb/IPAddressPool", "cozystack-default", "cozy-metallb"); !apierrors.IsNotFound(err) {
		t.Errorf("stale pool must be reclaimed, got err=%v", err)
	}
	if err := c.Get(context.Background(), types.NamespacedName{Namespace: "tenant-root", Name: "root-ingress"}, &networkv1alpha1.ServiceExposure{}); !apierrors.IsNotFound(err) {
		t.Errorf("deleted exposure should be gone, got err=%v", err)
	}
}

func TestReconcile_DeletionWithMissingClassDropsFinalizer(t *testing.T) {
	// Deleting an exposure whose class no longer exists must not hang: the
	// class is genuinely gone (ClassNotFound), nothing to recompute.
	s := newScheme(t)
	exp := exposure("ghost") // no such class
	exp.Finalizers = []string{exposureFinalizer}
	c := fake.NewClientBuilder().WithScheme(s).
		WithObjects(exp, ingressService()).
		WithStatusSubresource(&networkv1alpha1.ServiceExposure{}).
		Build()
	r := &Reconciler{Client: c, Scheme: s}

	cur := getExposure(t, c, "tenant-root", "root-ingress")
	if err := c.Delete(context.Background(), cur); err != nil {
		t.Fatal(err)
	}
	if err := reconcileOnce(r, exp); err != nil {
		t.Fatalf("deletion with a missing class must not error, got %v", err)
	}
	if err := c.Get(context.Background(), types.NamespacedName{Namespace: "tenant-root", Name: "root-ingress"}, &networkv1alpha1.ServiceExposure{}); !apierrors.IsNotFound(err) {
		t.Errorf("exposure should be gone after delete, got err=%v", err)
	}
}

func TestReconcile_BackendCRDMissing(t *testing.T) {
	// Simulate a cluster where the Cilium CRDs are not installed: the
	// RESTMapper cannot map the GVK, so a Get/Create against it returns a
	// NoKindMatchError. The controller must surface BackendUnavailable
	// rather than a generic failure. The fake client tolerates unknown
	// GVKs, so inject the mapper miss via an interceptor.
	s := newScheme(t)
	noCilium := func(gvk schema.GroupVersionKind) error {
		if gvk.Group == "cilium.io" {
			return &apimeta.NoKindMatchError{GroupKind: gvk.GroupKind(), SearchedVersions: []string{gvk.Version}}
		}
		return nil
	}
	exp := exposure("")
	c := fake.NewClientBuilder().WithScheme(s).
		WithObjects(exp, ingressService(), defaultClass(networkv1alpha1.BackendCilium, "192.0.2.0/24")).
		WithStatusSubresource(&networkv1alpha1.ServiceExposure{}).
		WithInterceptorFuncs(interceptor.Funcs{
			Get: func(ctx context.Context, cl client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
				if err := noCilium(obj.GetObjectKind().GroupVersionKind()); err != nil {
					return err
				}
				return cl.Get(ctx, key, obj, opts...)
			},
		}).
		Build()
	r := &Reconciler{Client: c, Scheme: s}
	_ = reconcileOnce(r, exp)

	cond := readyCondition(getExposure(t, c, "tenant-root", "root-ingress"))
	if cond == nil || cond.Status != metav1.ConditionFalse || cond.Reason != "BackendUnavailable" {
		t.Fatalf("want Ready=False/BackendUnavailable when the Cilium CRD is absent, got %+v", cond)
	}
}

func TestReconcile_DeletionCleansUpOwnedCRs(t *testing.T) {
	s := newScheme(t)
	exp := exposure("")
	c := fake.NewClientBuilder().WithScheme(s).
		WithObjects(exp, ingressService(), defaultClass(networkv1alpha1.BackendMetalLB, "192.0.2.0/24")).
		WithStatusSubresource(&networkv1alpha1.ServiceExposure{}).
		Build()
	r := &Reconciler{Client: c, Scheme: s}
	_ = reconcileOnce(r, exp)
	if _, err := getUnstructured(t, c, "metallb/IPAddressPool", "cozystack-default", "cozy-metallb"); err != nil {
		t.Fatalf("pool should exist before delete: %v", err)
	}

	cur := getExposure(t, c, "tenant-root", "root-ingress")
	if err := c.Delete(context.Background(), cur); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: types.NamespacedName{Namespace: "tenant-root", Name: "root-ingress"}}); err != nil {
		t.Fatalf("deletion reconcile: %v", err)
	}

	if _, err := getUnstructured(t, c, "metallb/IPAddressPool", "cozystack-default", "cozy-metallb"); !apierrors.IsNotFound(err) {
		t.Errorf("pool should be cleaned up on delete, got err=%v", err)
	}
	// Finalizer removed ⇒ exposure gone.
	if err := c.Get(context.Background(), types.NamespacedName{Namespace: "tenant-root", Name: "root-ingress"}, &networkv1alpha1.ServiceExposure{}); !apierrors.IsNotFound(err) {
		t.Errorf("exposure should be gone after finalizer removal, got err=%v", err)
	}
}
