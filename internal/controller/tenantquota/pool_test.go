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

package tenantquota

import (
	"testing"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
)

func rl(pairs map[string]string) corev1.ResourceList {
	if pairs == nil {
		return nil
	}
	out := corev1.ResourceList{}
	for k, v := range pairs {
		out[corev1.ResourceName(k)] = resource.MustParse(v)
	}
	return out
}

func quantityEqual(t *testing.T, got corev1.ResourceList, name, want string) {
	t.Helper()
	q := got[corev1.ResourceName(name)]
	w := resource.MustParse(want)
	if q.Cmp(w) != 0 {
		t.Fatalf("%s = %s, want %s", name, q.String(), w.String())
	}
}

func TestParentNamespace(t *testing.T) {
	cases := map[string]string{
		"tenant-root":        "",
		"tenant-foo":         "tenant-root",
		"tenant-foo-bar":     "tenant-foo",
		"tenant-foo-bar-baz": "tenant-foo-bar",
		"not-a-tenant":       "",
	}
	for in, want := range cases {
		if got := parentNamespace(in); got != want {
			t.Errorf("parentNamespace(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestComputePools_CarveOut(t *testing.T) {
	// tenant-foo (cpu 10) with a bounded child bar (cpu 4) and an unbounded
	// child qux. foo's pool: budget 10, carved 4, available 6, members
	// {tenant-foo, tenant-foo-qux}. bar's pool: budget 4, available 4,
	// members {tenant-foo-bar}.
	pools := ComputePools([]Tenant{
		{Namespace: "tenant-foo", Declared: rl(map[string]string{"cpu": "10"})},
		{Namespace: "tenant-foo-bar", Declared: rl(map[string]string{"cpu": "4"})},
		{Namespace: "tenant-foo-qux", Declared: nil},
	})

	foo, ok := pools["tenant-foo"]
	if !ok {
		t.Fatal("expected a pool rooted at tenant-foo")
	}
	quantityEqual(t, foo.Budget, "cpu", "10")
	quantityEqual(t, foo.CarvedOut, "cpu", "4")
	quantityEqual(t, foo.Available, "cpu", "6")
	if len(foo.Members) != 2 || foo.Members[0] != "tenant-foo" || foo.Members[1] != "tenant-foo-qux" {
		t.Fatalf("foo.Members = %v, want [tenant-foo tenant-foo-qux]", foo.Members)
	}

	bar, ok := pools["tenant-foo-bar"]
	if !ok {
		t.Fatal("expected a pool rooted at tenant-foo-bar")
	}
	quantityEqual(t, bar.Available, "cpu", "4")
	if len(bar.Members) != 1 || bar.Members[0] != "tenant-foo-bar" {
		t.Fatalf("bar.Members = %v, want [tenant-foo-bar]", bar.Members)
	}
}

func TestComputePools_SharedPoolNoChildQuota(t *testing.T) {
	// foo (cpu 10) with an unbounded child bar => they share the budget of 10.
	pools := ComputePools([]Tenant{
		{Namespace: "tenant-foo", Declared: rl(map[string]string{"cpu": "10"})},
		{Namespace: "tenant-foo-bar", Declared: nil},
	})
	foo := pools["tenant-foo"]
	if foo == nil {
		t.Fatal("expected pool at tenant-foo")
	}
	quantityEqual(t, foo.Available, "cpu", "10")
	if len(foo.Members) != 2 {
		t.Fatalf("foo.Members = %v, want both foo and foo-bar", foo.Members)
	}
	if _, ok := pools["tenant-foo-bar"]; ok {
		t.Fatalf("unbounded child must not form its own pool")
	}
}

func TestComputePools_NestedUnboundedIntermediary(t *testing.T) {
	// foo (cpu 10) -> bar (unbounded) -> baz (cpu 4, bounded).
	// baz forms its own pool and carves 4 out of foo's pool (its nearest
	// bounded ancestor is foo, through the unbounded bar).
	pools := ComputePools([]Tenant{
		{Namespace: "tenant-foo", Declared: rl(map[string]string{"cpu": "10"})},
		{Namespace: "tenant-foo-bar", Declared: nil},
		{Namespace: "tenant-foo-bar-baz", Declared: rl(map[string]string{"cpu": "4"})},
	})
	foo := pools["tenant-foo"]
	quantityEqual(t, foo.CarvedOut, "cpu", "4")
	quantityEqual(t, foo.Available, "cpu", "6")
	// foo's pool members: foo itself and the unbounded bar (baz is its own pool).
	if len(foo.Members) != 2 {
		t.Fatalf("foo.Members = %v, want foo and foo-bar", foo.Members)
	}
	baz := pools["tenant-foo-bar-baz"]
	if baz == nil {
		t.Fatal("expected pool at tenant-foo-bar-baz")
	}
	quantityEqual(t, baz.Available, "cpu", "4")
}

// TestComputePools_CarveOutUnboundedResourceDoesNotClamp: a child carving a
// resource its parent does not bound must not make that resource appear in the
// parent pool's Available/EnforcedHard — otherwise the parent would be clamped
// to zero of a resource it never limited.
func TestComputePools_CarveOutUnboundedResourceDoesNotClamp(t *testing.T) {
	pools := ComputePools([]Tenant{
		{Namespace: "tenant-foo", Declared: rl(map[string]string{"cpu": "4"})},
		{Namespace: "tenant-foo-bar", Declared: rl(map[string]string{"memory": "1Gi"})},
	})
	foo := pools["tenant-foo"]
	if _, ok := foo.Available[corev1.ResourceName("memory")]; ok {
		t.Fatalf("memory must not appear in Available (parent does not bound it), got %v", foo.Available)
	}
	quantityEqual(t, foo.Available, "cpu", "4")
	hard := foo.EnforcedHard("tenant-foo", nil)
	if _, ok := hard[corev1.ResourceName("memory")]; ok {
		t.Fatalf("EnforcedHard must not clamp a resource the parent does not bound, got %v", hard)
	}
}

func TestPoolEnforcedHard(t *testing.T) {
	pools := ComputePools([]Tenant{
		{Namespace: "tenant-foo", Declared: rl(map[string]string{"cpu": "10"})},
		{Namespace: "tenant-foo-bar", Declared: nil},
	})
	foo := pools["tenant-foo"]
	used := map[string]corev1.ResourceList{
		"tenant-foo":     rl(map[string]string{"cpu": "3"}),
		"tenant-foo-bar": rl(map[string]string{"cpu": "2"}),
	}
	// foo's own namespace may use available(10) - others(bar=2) = 8.
	gotFoo := foo.EnforcedHard("tenant-foo", used)
	quantityEqual(t, gotFoo, "cpu", "8")
	// bar may use 10 - foo's 3 = 7.
	gotBar := foo.EnforcedHard("tenant-foo-bar", used)
	quantityEqual(t, gotBar, "cpu", "7")
}

func TestPoolEnforcedHard_ClampsAtZero(t *testing.T) {
	pools := ComputePools([]Tenant{
		{Namespace: "tenant-foo", Declared: rl(map[string]string{"cpu": "10"})},
		{Namespace: "tenant-foo-bar", Declared: nil},
	})
	foo := pools["tenant-foo"]
	used := map[string]corev1.ResourceList{
		"tenant-foo-bar": rl(map[string]string{"cpu": "100"}), // overshoot
	}
	got := foo.EnforcedHard("tenant-foo", used)
	quantityEqual(t, got, "cpu", "0")
}

func TestPoolOvercommitted(t *testing.T) {
	// foo (cpu 10) with two bounded children summing to 11 => overcommitted by 1.
	pools := ComputePools([]Tenant{
		{Namespace: "tenant-foo", Declared: rl(map[string]string{"cpu": "10"})},
		{Namespace: "tenant-foo-bar", Declared: rl(map[string]string{"cpu": "6"})},
		{Namespace: "tenant-foo-baz", Declared: rl(map[string]string{"cpu": "5"})},
	})
	over := pools["tenant-foo"].Overcommitted()
	quantityEqual(t, over, "cpu", "1")
	// A within-budget pool reports nothing.
	pools2 := ComputePools([]Tenant{
		{Namespace: "tenant-foo", Declared: rl(map[string]string{"cpu": "10"})},
		{Namespace: "tenant-foo-bar", Declared: rl(map[string]string{"cpu": "4"})},
	})
	if over2 := pools2["tenant-foo"].Overcommitted(); len(over2) != 0 {
		t.Fatalf("expected no overcommit, got %v", over2)
	}
}

// TestPoolOvercommitted_IgnoresUnboundedResource: a child that bounds a resource
// its parent leaves unbounded is allowed at admission, so the parent pool must
// not be reported as overcommitted on that resource (an unbounded budget cannot
// be overcommitted).
func TestPoolOvercommitted_IgnoresUnboundedResource(t *testing.T) {
	pools := ComputePools([]Tenant{
		{Namespace: "tenant-foo", Declared: rl(map[string]string{"cpu": "10"})},
		{Namespace: "tenant-foo-bar", Declared: rl(map[string]string{"memory": "8Gi"})},
	})
	if over := pools["tenant-foo"].Overcommitted(); len(over) != 0 {
		t.Fatalf("expected no overcommit for a resource the parent does not bound, got %v", over)
	}
}

func TestScaleResourceList(t *testing.T) {
	got := ScaleResourceList(rl(map[string]string{"cpu": "10", "memory": "20Gi"}), 120)
	quantityEqual(t, got, "cpu", "12")
	quantityEqual(t, got, "memory", "24Gi")
	// 100% is a no-op copy.
	same := ScaleResourceList(rl(map[string]string{"cpu": "10"}), 100)
	quantityEqual(t, same, "cpu", "10")
}
