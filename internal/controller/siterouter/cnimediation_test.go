// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Cozystack Authors.

package siterouter

import (
	"context"
	"encoding/json"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
)

// routeSet decodes an ovn.kubernetes.io/routes annotation value into a
// dst→gw map so tests compare route content without depending on entry order.
func routeSet(t *testing.T, encoded string) map[string]string {
	t.Helper()
	var entries []routeEntry
	if err := json.Unmarshal([]byte(encoded), &entries); err != nil {
		t.Fatalf("routes annotation %q is not valid JSON: %v", encoded, err)
	}
	out := make(map[string]string, len(entries))
	for _, e := range entries {
		out[e.Dst] = e.Gw
	}
	return out
}

// TestMergeRoutes_BuildsAnnotationJSON encodes the T12 "routes-annotation JSON
// built correctly" case: one remoteCIDR against an empty namespace annotation
// yields a single {dst,gw} entry pointing at the gateway pod IP.
func TestMergeRoutes_BuildsAnnotationJSON(t *testing.T) {
	got, err := mergeRoutes("", "10.244.0.5", []string{"172.31.0.0/16"})
	if err != nil {
		t.Fatalf("mergeRoutes: %v", err)
	}
	set := routeSet(t, got)
	if len(set) != 1 || set["172.31.0.0/16"] != "10.244.0.5" {
		t.Fatalf("mergeRoutes = %q, want a single 172.31.0.0/16 -> 10.244.0.5 route", got)
	}
}

// TestMergeRoutes_AccumulatesByDst encodes the T07 note "merge, don't replace,
// the routes list if multiple site-routers exist in one namespace (accumulate
// entries, keyed by dst)": merging this instance's route into a namespace that
// already carries a co-tenant instance's route must keep both.
func TestMergeRoutes_AccumulatesByDst(t *testing.T) {
	existing := `[{"dst":"10.10.0.0/16","gw":"10.244.0.9"}]` // another instance's gateway
	got, err := mergeRoutes(existing, "10.244.0.5", []string{"172.31.0.0/16"})
	if err != nil {
		t.Fatalf("mergeRoutes: %v", err)
	}
	set := routeSet(t, got)
	if len(set) != 2 {
		t.Fatalf("mergeRoutes = %q, want both routes preserved (accumulate by dst)", got)
	}
	if set["10.10.0.0/16"] != "10.244.0.9" {
		t.Errorf("co-tenant route 10.10.0.0/16 -> 10.244.0.9 must be preserved, got gw %q", set["10.10.0.0/16"])
	}
	if set["172.31.0.0/16"] != "10.244.0.5" {
		t.Errorf("own route 172.31.0.0/16 -> 10.244.0.5 missing, got gw %q", set["172.31.0.0/16"])
	}
}

// TestMergeRoutes_UpsertsSameDst proves a re-reconcile with a moved gateway IP
// updates the gw in place (keyed by dst) rather than duplicating the dst.
func TestMergeRoutes_UpsertsSameDst(t *testing.T) {
	existing := `[{"dst":"172.31.0.0/16","gw":"10.244.0.5"}]`
	got, err := mergeRoutes(existing, "10.244.0.7", []string{"172.31.0.0/16"})
	if err != nil {
		t.Fatalf("mergeRoutes: %v", err)
	}
	set := routeSet(t, got)
	if len(set) != 1 || set["172.31.0.0/16"] != "10.244.0.7" {
		t.Fatalf("mergeRoutes = %q, want the single dst upserted to gw 10.244.0.7", got)
	}
}

// TestMergeRoutes_Idempotent proves the canonical encoding is stable: merging the
// same desired state twice yields byte-identical JSON (so a config-hash / SSA
// no-op guard does not see spurious drift).
func TestMergeRoutes_Idempotent(t *testing.T) {
	first, err := mergeRoutes("", "10.244.0.5", []string{"172.31.0.0/16", "10.10.0.0/16"})
	if err != nil {
		t.Fatalf("mergeRoutes first: %v", err)
	}
	second, err := mergeRoutes(first, "10.244.0.5", []string{"172.31.0.0/16", "10.10.0.0/16"})
	if err != nil {
		t.Fatalf("mergeRoutes second: %v", err)
	}
	if first != second {
		t.Fatalf("mergeRoutes not idempotent: first=%q second=%q", first, second)
	}
}

// TestRemoveRoutes_RemovesOnlyOwnEntries encodes the finalizer contract: removing
// this instance's remoteCIDRs from a namespace shared with a co-tenant instance
// drops only its own dst entries and leaves the co-tenant's intact.
func TestRemoveRoutes_RemovesOnlyOwnEntries(t *testing.T) {
	existing := `[{"dst":"172.31.0.0/16","gw":"10.244.0.5"},{"dst":"10.10.0.0/16","gw":"10.244.0.9"}]`
	got, err := removeRoutes(existing, "10.244.0.5", []string{"172.31.0.0/16"})
	if err != nil {
		t.Fatalf("removeRoutes: %v", err)
	}
	set := routeSet(t, got)
	if _, ok := set["172.31.0.0/16"]; ok {
		t.Errorf("own route 172.31.0.0/16 should have been removed, got %q", got)
	}
	if set["10.10.0.0/16"] != "10.244.0.9" {
		t.Errorf("co-tenant route 10.10.0.0/16 must survive removal, got %q", got)
	}
}

// TestRemoveRoutes_RemovesAllOwnEntriesByGatewayIP proves the finalizer path
// withdraws every entry this gateway owns even when remoteCIDRs has since shrunk
// (so the stale dst is no longer in the declared set), while preserving a
// co-tenant entry.
func TestRemoveRoutes_RemovesAllOwnEntriesByGatewayIP(t *testing.T) {
	existing := `[{"dst":"172.31.0.0/16","gw":"10.244.0.5"},{"dst":"10.10.0.0/16","gw":"10.244.0.5"},{"dst":"192.0.2.0/24","gw":"10.244.0.9"}]`
	// remoteCIDRs was shrunk to just 172.31 before delete; 10.10 is stale but
	// still owned by this gateway and must be reclaimed.
	got, err := removeRoutes(existing, "10.244.0.5", []string{"172.31.0.0/16"})
	if err != nil {
		t.Fatalf("removeRoutes: %v", err)
	}
	set := routeSet(t, got)
	if _, ok := set["172.31.0.0/16"]; ok {
		t.Errorf("own route 172.31.0.0/16 should have been removed, got %q", got)
	}
	if _, ok := set["10.10.0.0/16"]; ok {
		t.Errorf("stale own route 10.10.0.0/16 should have been removed by gateway-IP ownership, got %q", got)
	}
	if set["192.0.2.0/24"] != "10.244.0.9" {
		t.Errorf("co-tenant route 192.0.2.0/24 must survive removal, got %q", got)
	}
}

// TestMergeRoutes_PrunesOwnStaleOnShrink encodes the R3 fix: when remoteCIDRs
// shrinks ([A,B] -> [A]) the entry this instance left behind (B, own gw) is
// withdrawn on the next programming, while the kept entry (A) and a co-tenant
// entry (different gw) survive.
func TestMergeRoutes_PrunesOwnStaleOnShrink(t *testing.T) {
	// This instance owns A (10.10) and B (172.31) at gw 10.244.0.5; a co-tenant
	// owns C (192.0.2) at gw 10.244.0.9.
	existing := `[{"dst":"10.10.0.0/16","gw":"10.244.0.5"},{"dst":"172.31.0.0/16","gw":"10.244.0.5"},{"dst":"192.0.2.0/24","gw":"10.244.0.9"}]`
	got, err := mergeRoutes(existing, "10.244.0.5", []string{"10.10.0.0/16"}) // shrink to A only
	if err != nil {
		t.Fatalf("mergeRoutes: %v", err)
	}
	set := routeSet(t, got)
	if _, ok := set["172.31.0.0/16"]; ok {
		t.Errorf("stale own route 172.31.0.0/16 (B) must be pruned on shrink, got %q", got)
	}
	if set["10.10.0.0/16"] != "10.244.0.5" {
		t.Errorf("kept own route 10.10.0.0/16 (A) missing, got %q", got)
	}
	if set["192.0.2.0/24"] != "10.244.0.9" {
		t.Errorf("co-tenant route 192.0.2.0/24 (C) must be preserved, got %q", got)
	}
}

// TestMergeRoutes_PrunesAllOwnOnEmptyShrink proves shrinking remoteCIDRs to []
// withdraws every entry this instance owns while leaving a co-tenant entry.
func TestMergeRoutes_PrunesAllOwnOnEmptyShrink(t *testing.T) {
	existing := `[{"dst":"172.31.0.0/16","gw":"10.244.0.5"},{"dst":"192.0.2.0/24","gw":"10.244.0.9"}]`
	got, err := mergeRoutes(existing, "10.244.0.5", nil)
	if err != nil {
		t.Fatalf("mergeRoutes: %v", err)
	}
	set := routeSet(t, got)
	if _, ok := set["172.31.0.0/16"]; ok {
		t.Errorf("own route 172.31.0.0/16 must be pruned when remoteCIDRs empties, got %q", got)
	}
	if set["192.0.2.0/24"] != "10.244.0.9" {
		t.Errorf("co-tenant route 192.0.2.0/24 must be preserved, got %q", got)
	}
}

// gwPod builds a gateway virt-launcher pod carrying the lineage labels the
// controller discovers it by, with a pod IP.
func gwPod(name, instance, podIP string) *corev1.Pod {
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: "tenant-test",
			Labels: map[string]string{
				appKindLabelKey: siteRouterKind,
				appNameLabelKey: instance,
			},
		},
		Status: corev1.PodStatus{Phase: corev1.PodRunning, PodIP: podIP},
	}
}

// TestRelaxGatewayPortSecurity_TargetsOnlyGatewayPod encodes the T07 Acceptance
// "the gateway VM pod has port_security=false; no other pod does": the
// relaxation patch lands on the resolved gateway pod and nothing else in the
// namespace.
func TestRelaxGatewayPortSecurity_TargetsOnlyGatewayPod(t *testing.T) {
	gateway := gwPod("virt-launcher-site-router-demo-abcde", "demo", "10.244.0.5")
	bystander := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "tenant-workload-0", Namespace: "tenant-test"}}
	r := newTestReconciler(t, gateway, bystander)

	inst := &instance{
		name:       "demo",
		namespace:  "tenant-test",
		gatewayPod: gateway,
	}
	if err := r.relaxGatewayPortSecurity(context.Background(), inst); err != nil {
		t.Fatalf("relaxGatewayPortSecurity: %v", err)
	}

	got := &corev1.Pod{}
	if err := r.Get(context.Background(), types.NamespacedName{Namespace: "tenant-test", Name: gateway.Name}, got); err != nil {
		t.Fatalf("get gateway pod: %v", err)
	}
	if got.Annotations[portSecurityAnnotation] != portSecurityRelaxed {
		t.Errorf("gateway pod %s = %q, want %s=%q", gateway.Name, got.Annotations[portSecurityAnnotation], portSecurityAnnotation, portSecurityRelaxed)
	}

	other := &corev1.Pod{}
	if err := r.Get(context.Background(), types.NamespacedName{Namespace: "tenant-test", Name: bystander.Name}, other); err != nil {
		t.Fatalf("get bystander pod: %v", err)
	}
	if _, set := other.Annotations[portSecurityAnnotation]; set {
		t.Errorf("bystander pod must not carry %s, got %q", portSecurityAnnotation, other.Annotations[portSecurityAnnotation])
	}
}
