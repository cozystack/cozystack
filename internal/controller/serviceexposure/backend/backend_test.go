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

package backend

import (
	"reflect"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	networkv1alpha1 "github.com/cozystack/cozystack/api/network/v1alpha1"
)

func boolPtr(b bool) *bool { return &b }

func testService() *corev1.Service {
	return &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{Name: "root-ingress", Namespace: "tenant-root"},
	}
}

func metallbClass(addrs ...string) *networkv1alpha1.ExposureClass {
	return &networkv1alpha1.ExposureClass{
		ObjectMeta: metav1.ObjectMeta{Name: "default"},
		Spec:       networkv1alpha1.ExposureClassSpec{Backend: networkv1alpha1.BackendMetalLB, Addresses: addrs},
	}
}

func ciliumClass(addrs ...string) *networkv1alpha1.ExposureClass {
	return &networkv1alpha1.ExposureClass{
		ObjectMeta: metav1.ObjectMeta{Name: "default"},
		Spec:       networkv1alpha1.ExposureClassSpec{Backend: networkv1alpha1.BackendCilium, Addresses: addrs},
	}
}

// findObj returns the first object in objs with the given kind, or nil.
func findObj(objs []*unstructured.Unstructured, kind string) *unstructured.Unstructured {
	for _, o := range objs {
		if o.GetKind() == kind {
			return o
		}
	}
	return nil
}

func TestRegistry_AllBackendsRegistered(t *testing.T) {
	for _, name := range []networkv1alpha1.ExposureBackend{
		networkv1alpha1.BackendExternalIPs,
		networkv1alpha1.BackendMetalLB,
		networkv1alpha1.BackendCilium,
		networkv1alpha1.BackendRobotLB,
	} {
		b, err := For(name)
		if err != nil {
			t.Fatalf("For(%q) returned error: %v", name, err)
		}
		if b.Name() != name {
			t.Errorf("For(%q).Name() = %q", name, b.Name())
		}
	}
	if _, err := For("bogus"); err == nil {
		t.Error("For(bogus) expected error, got nil")
	}
}

func TestExternalIPsBackend(t *testing.T) {
	b := externalIPsBackend{}
	objs, err := b.Desired(&networkv1alpha1.ExposureClass{}, []string{"tenant-root"})
	if err != nil {
		t.Fatalf("Desired error: %v", err)
	}
	if len(objs) != 0 {
		t.Errorf("externalIPs backend must render no objects, got %d", len(objs))
	}

	svc := testService()
	svc.Spec.ExternalIPs = []string{"203.0.113.10"}
	ips, ready, _ := b.Observe(svc)
	if !ready || !reflect.DeepEqual(ips, []string{"203.0.113.10"}) {
		t.Errorf("Observe = (%v, %v), want ([203.0.113.10], true)", ips, ready)
	}
	if _, ready, reason := b.Observe(testService()); ready || reason != "NoExternalIPs" {
		t.Errorf("Observe(no IPs) = (%v, %q), want (false, NoExternalIPs)", ready, reason)
	}
}

func TestMetalLBBackend_Desired(t *testing.T) {
	class := metallbClass("192.0.2.0/24", "198.51.100.10-198.51.100.20")
	objs, err := metallbBackend{}.Desired(class, []string{"tenant-root"})
	if err != nil {
		t.Fatalf("Desired error: %v", err)
	}
	if len(objs) != 2 {
		t.Fatalf("want 2 objects (pool + L2Advertisement), got %d", len(objs))
	}

	pool := findObj(objs, "IPAddressPool")
	if pool == nil {
		t.Fatal("no IPAddressPool rendered")
	}
	if pool.GetAPIVersion() != "metallb.io/v1beta1" {
		t.Errorf("pool apiVersion = %q", pool.GetAPIVersion())
	}
	if pool.GetNamespace() != MetalLBNamespace {
		t.Errorf("pool namespace = %q, want %q", pool.GetNamespace(), MetalLBNamespace)
	}
	if pool.GetName() != "cozystack-default" {
		t.Errorf("pool name = %q, want cozystack-default (class-level)", pool.GetName())
	}
	addrs, _, _ := unstructured.NestedStringSlice(pool.Object, "spec", "addresses")
	if !reflect.DeepEqual(addrs, class.Spec.Addresses) {
		t.Errorf("pool addresses = %v, want %v", addrs, class.Spec.Addresses)
	}
	nsAlloc, _, _ := unstructured.NestedStringSlice(pool.Object, "spec", "serviceAllocation", "namespaces")
	if !reflect.DeepEqual(nsAlloc, []string{"tenant-root"}) {
		t.Errorf("serviceAllocation.namespaces = %v, want [tenant-root]", nsAlloc)
	}

	l2 := findObj(objs, "L2Advertisement")
	if l2 == nil {
		t.Fatal("no L2Advertisement rendered")
	}
	pools, _, _ := unstructured.NestedStringSlice(l2.Object, "spec", "ipAddressPools")
	if !reflect.DeepEqual(pools, []string{"cozystack-default"}) {
		t.Errorf("l2 ipAddressPools = %v", pools)
	}
}

func TestMetalLBBackend_NamespaceUnion(t *testing.T) {
	objs, err := metallbBackend{}.Desired(metallbClass("192.0.2.0/24"), []string{"tenant-a", "tenant-b"})
	if err != nil {
		t.Fatalf("Desired error: %v", err)
	}
	pool := findObj(objs, "IPAddressPool")
	nsAlloc, _, _ := unstructured.NestedStringSlice(pool.Object, "spec", "serviceAllocation", "namespaces")
	if !reflect.DeepEqual(nsAlloc, []string{"tenant-a", "tenant-b"}) {
		t.Errorf("serviceAllocation.namespaces = %v, want [tenant-a tenant-b]", nsAlloc)
	}
}

func TestMetalLBBackend_NoL2(t *testing.T) {
	class := metallbClass("192.0.2.0/24")
	class.Spec.L2 = boolPtr(false)
	objs, err := metallbBackend{}.Desired(class, []string{"tenant-root"})
	if err != nil {
		t.Fatalf("Desired error: %v", err)
	}
	if len(objs) != 1 || findObj(objs, "IPAddressPool") == nil {
		t.Errorf("L2 disabled must render only the pool, got %d objects", len(objs))
	}
}

func TestMetalLBBackend_NoNamespacesRendersNothing(t *testing.T) {
	objs, err := metallbBackend{}.Desired(metallbClass("192.0.2.0/24"), nil)
	if err != nil {
		t.Fatalf("Desired error: %v", err)
	}
	if len(objs) != 0 {
		t.Errorf("no namespaces must render nothing, got %d objects", len(objs))
	}
}

func TestMetalLBBackend_EmptyAddressesErrors(t *testing.T) {
	class := &networkv1alpha1.ExposureClass{
		ObjectMeta: metav1.ObjectMeta{Name: "default"},
		Spec:       networkv1alpha1.ExposureClassSpec{Backend: networkv1alpha1.BackendMetalLB},
	}
	if _, err := (metallbBackend{}).Desired(class, []string{"tenant-root"}); err == nil {
		t.Error("metallb with no addresses must error")
	}
}

func TestCiliumBackend_Desired(t *testing.T) {
	class := ciliumClass("192.0.2.0/24", "198.51.100.10-198.51.100.20")
	class.Spec.Interfaces = []string{"eth0"}
	objs, err := ciliumBackend{}.Desired(class, []string{"tenant-root"})
	if err != nil {
		t.Fatalf("Desired error: %v", err)
	}
	if len(objs) != 2 {
		t.Fatalf("want 2 objects, got %d", len(objs))
	}

	pool := findObj(objs, "CiliumLoadBalancerIPPool")
	if pool == nil {
		t.Fatal("no CiliumLoadBalancerIPPool rendered")
	}
	if pool.GetAPIVersion() != "cilium.io/v2" {
		t.Errorf("pool apiVersion = %q, want cilium.io/v2", pool.GetAPIVersion())
	}
	if pool.GetNamespace() != "" {
		t.Errorf("CiliumLoadBalancerIPPool must be cluster-scoped, got namespace %q", pool.GetNamespace())
	}
	if pool.GetName() != "cozystack-default" {
		t.Errorf("pool name = %q, want cozystack-default", pool.GetName())
	}
	blocks, _, _ := unstructured.NestedSlice(pool.Object, "spec", "blocks")
	if len(blocks) != 2 {
		t.Fatalf("want 2 blocks, got %d", len(blocks))
	}
	cidrBlock, _ := blocks[0].(map[string]interface{})
	if cidrBlock["cidr"] != "192.0.2.0/24" {
		t.Errorf("block[0].cidr = %v", cidrBlock["cidr"])
	}
	rangeBlock, _ := blocks[1].(map[string]interface{})
	if rangeBlock["start"] != "198.51.100.10" || rangeBlock["stop"] != "198.51.100.20" {
		t.Errorf("block[1] range = %v", rangeBlock)
	}
	if !ciliumSelectorHasNamespaces(t, pool, []string{"tenant-root"}) {
		t.Errorf("pool serviceSelector did not scope to [tenant-root]")
	}

	l2 := findObj(objs, "CiliumL2AnnouncementPolicy")
	if l2 == nil {
		t.Fatal("no CiliumL2AnnouncementPolicy rendered")
	}
	if l2.GetAPIVersion() != "cilium.io/v2alpha1" {
		t.Errorf("l2 apiVersion = %q, want cilium.io/v2alpha1", l2.GetAPIVersion())
	}
	ifaces, _, _ := unstructured.NestedStringSlice(l2.Object, "spec", "interfaces")
	if !reflect.DeepEqual(ifaces, []string{"eth0"}) {
		t.Errorf("l2 interfaces = %v", ifaces)
	}
}

func TestCiliumBackend_NamespaceUnion(t *testing.T) {
	objs, err := ciliumBackend{}.Desired(ciliumClass("192.0.2.0/24"), []string{"tenant-a", "tenant-b"})
	if err != nil {
		t.Fatalf("Desired error: %v", err)
	}
	pool := findObj(objs, "CiliumLoadBalancerIPPool")
	if !ciliumSelectorHasNamespaces(t, pool, []string{"tenant-a", "tenant-b"}) {
		t.Errorf("pool serviceSelector did not scope to [tenant-a tenant-b]")
	}
}

// ciliumSelectorHasNamespaces asserts the pool's serviceSelector contains
// a matchExpressions entry on io.kubernetes.service.namespace In want.
func ciliumSelectorHasNamespaces(t *testing.T, obj *unstructured.Unstructured, want []string) bool {
	t.Helper()
	exprs, _, _ := unstructured.NestedSlice(obj.Object, "spec", "serviceSelector", "matchExpressions")
	for _, e := range exprs {
		m, _ := e.(map[string]interface{})
		if m["key"] != "io.kubernetes.service.namespace" || m["operator"] != "In" {
			continue
		}
		vals, _, _ := unstructured.NestedStringSlice(m, "values")
		if reflect.DeepEqual(vals, want) {
			return true
		}
	}
	return false
}

func TestCiliumBackend_NoL2(t *testing.T) {
	class := ciliumClass("192.0.2.0/24")
	class.Spec.L2 = boolPtr(false)
	objs, err := ciliumBackend{}.Desired(class, []string{"tenant-root"})
	if err != nil {
		t.Fatalf("Desired error: %v", err)
	}
	if len(objs) != 1 || findObj(objs, "CiliumLoadBalancerIPPool") == nil {
		t.Errorf("L2 disabled must render only the pool, got %d objects", len(objs))
	}
}

func TestCiliumBackend_NoNamespacesRendersNothing(t *testing.T) {
	objs, err := ciliumBackend{}.Desired(ciliumClass("192.0.2.0/24"), nil)
	if err != nil {
		t.Fatalf("Desired error: %v", err)
	}
	if len(objs) != 0 {
		t.Errorf("no namespaces must render nothing, got %d objects", len(objs))
	}
}

func TestCiliumBackend_EmptyAddressesErrors(t *testing.T) {
	class := &networkv1alpha1.ExposureClass{
		ObjectMeta: metav1.ObjectMeta{Name: "default"},
		Spec:       networkv1alpha1.ExposureClassSpec{Backend: networkv1alpha1.BackendCilium},
	}
	if _, err := (ciliumBackend{}).Desired(class, []string{"tenant-root"}); err == nil {
		t.Error("cilium with no addresses must error")
	}
}

func TestRobotLBBackend(t *testing.T) {
	objs, err := robotlbBackend{}.Desired(&networkv1alpha1.ExposureClass{}, []string{"tenant-root"})
	if err != nil {
		t.Fatalf("Desired error: %v", err)
	}
	if len(objs) != 0 {
		t.Errorf("robotlb backend must render no objects, got %d", len(objs))
	}
	svc := testService()
	svc.Status.LoadBalancer.Ingress = []corev1.LoadBalancerIngress{{IP: "203.0.113.55"}}
	ips, ready, _ := robotlbBackend{}.Observe(svc)
	if !ready || !reflect.DeepEqual(ips, []string{"203.0.113.55"}) {
		t.Errorf("Observe = (%v, %v)", ips, ready)
	}
}
