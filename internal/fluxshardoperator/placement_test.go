package fluxshardoperator

import (
	"fmt"
	"testing"
)

func loadsOf(assign map[string]string, tenants []TenantInfo, k int) []int {
	load := make([]int, k)
	for _, t := range tenants {
		if idx, ok := ParseShardIndex(assign[t.Namespace]); ok && idx < k {
			load[idx] += t.Weight
		}
	}
	return load
}

func TestComputePlacementNOverNIsOnePerShard(t *testing.T) {
	// The N-over-N guarantee from the design plan: uniform weights, N tenants
	// across N shards => exactly 1 per shard. Deterministic, not probabilistic.
	const n = 4
	var tenants []TenantInfo
	for i := 0; i < n; i++ {
		tenants = append(tenants, TenantInfo{Namespace: fmt.Sprintf("tenant-%c", 'a'+i), Weight: 7})
	}
	assign := ComputePlacement(PlacementInput{Tenants: tenants, ShardCount: n})
	used := map[string]bool{}
	for _, s := range assign {
		if used[s] {
			t.Fatalf("shard %s assigned twice: %v", s, assign)
		}
		used[s] = true
	}
	if len(used) != n {
		t.Fatalf("expected %d distinct shards, got %v", n, assign)
	}
}

func TestComputePlacementLegacyBackfillBalances(t *testing.T) {
	// Tenants still on the legacy "tenants" bucket arrive as Current=""
	// (unassigned) and must spread by weight.
	tenants := []TenantInfo{
		{Namespace: "tenant-big", Weight: 80},
		{Namespace: "tenant-a", Weight: 10},
		{Namespace: "tenant-b", Weight: 10},
		{Namespace: "tenant-c", Weight: 10},
		{Namespace: "tenant-d", Weight: 10},
		{Namespace: "tenant-e", Weight: 10},
		{Namespace: "tenant-f", Weight: 10},
		{Namespace: "tenant-g", Weight: 10},
		{Namespace: "tenant-h", Weight: 10},
	}
	assign := ComputePlacement(PlacementInput{Tenants: tenants, ShardCount: 2, RebalanceThreshold: 0.25})
	load := loadsOf(assign, tenants, 2)
	if abs(load[0]-load[1]) > 20 {
		t.Fatalf("unbalanced backfill: loads %v, assign %v", load, assign)
	}
}

func TestComputePlacementKeepsSettledTenantsOnScaleUp(t *testing.T) {
	// Scale-up must not touch tenants already at/under target; rebalance pulls
	// from the most-loaded shard only.
	tenants := []TenantInfo{
		{Namespace: "tenant-a", Weight: 10, Current: "shard0"},
		{Namespace: "tenant-b", Weight: 10, Current: "shard0"},
		{Namespace: "tenant-c", Weight: 10, Current: "shard1"},
	}
	assign := ComputePlacement(PlacementInput{Tenants: tenants, ShardCount: 3, RebalanceThreshold: 0.25})
	if assign["tenant-c"] != "shard1" {
		t.Fatalf("settled tenant on the least-loaded shard moved: %v", assign)
	}
	moved := 0
	for _, tn := range []string{"tenant-a", "tenant-b"} {
		if assign[tn] != "shard0" {
			moved++
			if assign[tn] != "shard2" {
				t.Fatalf("tenant %s moved to %s, expected the new shard2", tn, assign[tn])
			}
		}
	}
	if moved != 1 {
		t.Fatalf("expected exactly 1 tenant pulled to the new shard, got %d: %v", moved, assign)
	}
}

func TestComputePlacementScaleDownMovesOnlyRemovedShards(t *testing.T) {
	tenants := []TenantInfo{
		{Namespace: "tenant-a", Weight: 10, Current: "shard0"},
		{Namespace: "tenant-b", Weight: 10, Current: "shard1"},
		{Namespace: "tenant-c", Weight: 10, Current: "shard2"},
		{Namespace: "tenant-d", Weight: 10, Current: "shard3"},
	}
	assign := ComputePlacement(PlacementInput{Tenants: tenants, ShardCount: 2})
	if assign["tenant-a"] != "shard0" || assign["tenant-b"] != "shard1" {
		t.Fatalf("tenants on surviving shards must stay: %v", assign)
	}
	for _, tn := range []string{"tenant-c", "tenant-d"} {
		if idx, ok := ParseShardIndex(assign[tn]); !ok || idx >= 2 {
			t.Fatalf("tenant %s not redistributed to a surviving shard: %v", tn, assign)
		}
	}
}

func TestComputePlacementPinnedTenant(t *testing.T) {
	tenants := []TenantInfo{
		{Namespace: "tenant-whale", Weight: 100, Current: "shard0"},
		{Namespace: "tenant-a", Weight: 1},
	}
	assign := ComputePlacement(PlacementInput{
		Tenants:    tenants,
		ShardCount: 4,
		Pinned:     map[string]string{"tenant-whale": "shard3"},
	})
	if assign["tenant-whale"] != "shard3" {
		t.Fatalf("pinned tenant not on its shard: %v", assign)
	}

	// A pin to a non-existent shard is ignored, the current assignment wins.
	assign = ComputePlacement(PlacementInput{
		Tenants:    tenants,
		ShardCount: 2,
		Pinned:     map[string]string{"tenant-whale": "shard3"},
	})
	if assign["tenant-whale"] != "shard0" {
		t.Fatalf("invalid pin must fall back to current assignment: %v", assign)
	}
}

func TestComputePlacementRebalanceIgnoresInvalidPins(t *testing.T) {
	tenants := []TenantInfo{
		{Namespace: "tenant-anchor", Weight: 25, Current: "shard0"},
		{Namespace: "tenant-pinned", Weight: 15, Current: "shard0"},
		{Namespace: "tenant-c", Weight: 10, Current: "shard1"},
	}
	assign := ComputePlacement(PlacementInput{
		Tenants:            tenants,
		ShardCount:         2,
		RebalanceThreshold: 0.25,
		// The pin points at a shard that does not exist at this K; initial
		// placement already ignores it, so rebalance must not treat the
		// tenant as sticky either.
		Pinned: map[string]string{"tenant-pinned": "shard5"},
		// Veto the unpinned candidate so the invalidly pinned tenant is the
		// only move that can fix the spread.
		CanRebalance: func(ns string) bool { return ns == "tenant-pinned" },
	})
	if assign["tenant-pinned"] != "shard1" {
		t.Fatalf("invalidly pinned tenant must participate in rebalance: %v", assign)
	}
}

func TestComputePlacementDeletingTenants(t *testing.T) {
	// A deleting tenant on a removed shard stays put (the provisioner waits
	// for the drain); an unassigned deleting tenant still gets an owner so its
	// teardown can finish.
	tenants := []TenantInfo{
		{Namespace: "tenant-gone", Weight: 5, Current: "shard3", Deleting: true},
		{Namespace: "tenant-dying", Weight: 5, Deleting: true},
		{Namespace: "tenant-a", Weight: 5, Current: "shard0"},
	}
	assign := ComputePlacement(PlacementInput{Tenants: tenants, ShardCount: 2})
	if assign["tenant-gone"] != "shard3" {
		t.Fatalf("deleting tenant must never move: %v", assign)
	}
	if idx, ok := ParseShardIndex(assign["tenant-dying"]); !ok || idx >= 2 {
		t.Fatalf("unassigned deleting tenant must still get an owning shard: %v", assign)
	}
}

func TestComputePlacementRebalanceRespectsCooldownAndThreshold(t *testing.T) {
	tenants := []TenantInfo{
		{Namespace: "tenant-a", Weight: 30, Current: "shard0"},
		{Namespace: "tenant-b", Weight: 30, Current: "shard0"},
		{Namespace: "tenant-c", Weight: 10, Current: "shard1"},
	}

	// Below threshold: nothing moves.
	assign := ComputePlacement(PlacementInput{Tenants: tenants, ShardCount: 2, RebalanceThreshold: 2.0})
	for _, tn := range []string{"tenant-a", "tenant-b", "tenant-c"} {
		if assign[tn] != map[string]string{"tenant-a": "shard0", "tenant-b": "shard0", "tenant-c": "shard1"}[tn] {
			t.Fatalf("no move expected below threshold: %v", assign)
		}
	}

	// Above threshold: one 30-weight tenant moves from shard0 to shard1,
	// loads become [30, 40] and the spread drops within reach of the average.
	assign = ComputePlacement(PlacementInput{Tenants: tenants, ShardCount: 2, RebalanceThreshold: 0.25})
	load := loadsOf(assign, tenants, 2)
	if load[0] != 30 || load[1] != 40 {
		t.Fatalf("expected one 30-weight tenant to move, got loads %v: %v", load, assign)
	}

	// Cooldown vetoes every candidate: nothing moves.
	assign = ComputePlacement(PlacementInput{
		Tenants:            tenants,
		ShardCount:         2,
		RebalanceThreshold: 0.25,
		CanRebalance:       func(string) bool { return false },
	})
	if assign["tenant-a"] != "shard0" || assign["tenant-b"] != "shard0" {
		t.Fatalf("cooldown must block rebalance moves: %v", assign)
	}
}

func TestParseShardIndex(t *testing.T) {
	cases := []struct {
		in  string
		idx int
		ok  bool
	}{
		{"shard0", 0, true},
		{"shard12", 12, true},
		{"shard", 0, false},
		{"shard-1", 0, false},
		{"shard01", 0, false},
		{"tenants", 0, false},
		{"", 0, false},
	}
	for _, c := range cases {
		idx, ok := ParseShardIndex(c.in)
		if ok != c.ok || (ok && idx != c.idx) {
			t.Errorf("ParseShardIndex(%q) = %d,%v, want %d,%v", c.in, idx, ok, c.idx, c.ok)
		}
	}
}

func TestRecommendedShardCount(t *testing.T) {
	cases := []struct {
		hrs, tenants, want int
	}{
		{15, 3, 1},     // small cluster
		{81, 13, 1},    // C5 anchor: comfortable on one shard
		{946, 128, 10}, // C1 anchor: ceil(946/100) = 10
		{5000, 128, 16},
		{500, 2, 2}, // capped by tenant count
		{0, 0, 1},
	}
	for _, c := range cases {
		if got := RecommendedShardCount(c.hrs, c.tenants); got != c.want {
			t.Errorf("RecommendedShardCount(%d, %d) = %d, want %d", c.hrs, c.tenants, got, c.want)
		}
	}
}

func TestEffectiveShardCount(t *testing.T) {
	explicit := &Config{ShardCount: 7}
	if got := explicit.EffectiveShardCount(5000, 128, 2); got != 7 {
		t.Fatalf("explicit shard count must win, got %d", got)
	}

	auto := &Config{AutoShardCount: true}
	cases := []struct {
		desc                  string
		hrs, tenants, current int
		want                  int
	}{
		{"fresh install goes straight to the recommendation", 250, 10, 0, 3},
		{"stable at the recommendation", 150, 10, 2, 2},
		{"holds below the scale-up threshold", 110, 10, 1, 1}, // 110/1 <= 120
		{"scales up eagerly past the threshold", 130, 10, 1, 2},
		{"holds above the scale-down threshold", 110, 10, 2, 2},          // 55*2: desired 2 == current
		{"holds while shards are moderately underloaded", 130, 10, 2, 2}, // desired 2 == current
		{"scales down lazily when well under target", 90, 10, 2, 1},      // 45/shard < 60
		{"keeps current when underloaded but above floor", 99, 10, 1, 1},
	}
	for _, c := range cases {
		if got := auto.EffectiveShardCount(c.hrs, c.tenants, c.current); got != c.want {
			t.Errorf("%s: EffectiveShardCount(%d, %d, %d) = %d, want %d",
				c.desc, c.hrs, c.tenants, c.current, got, c.want)
		}
	}
}

func TestParseShardCount(t *testing.T) {
	if _, auto, err := ParseShardCount("auto"); err != nil || !auto {
		t.Fatalf("auto must parse: auto=%v err=%v", auto, err)
	}
	if n, auto, err := ParseShardCount("7"); err != nil || auto || n != 7 {
		t.Fatalf("explicit count must parse: n=%d auto=%v err=%v", n, auto, err)
	}
	for _, bad := range []string{"", "0", "-1", "many"} {
		if _, _, err := ParseShardCount(bad); err == nil {
			t.Errorf("ParseShardCount(%q) must fail", bad)
		}
	}
}

func TestParsePinnedTenants(t *testing.T) {
	pinned, err := ParsePinnedTenants("tenant-a=shard1, tenant-b=shard0")
	if err != nil {
		t.Fatal(err)
	}
	if pinned["tenant-a"] != "shard1" || pinned["tenant-b"] != "shard0" {
		t.Fatalf("unexpected parse result: %v", pinned)
	}
	if _, err := ParsePinnedTenants("tenant-a=tenants"); err == nil {
		t.Fatal("non-canonical shard value must be rejected")
	}
	if _, err := ParsePinnedTenants("tenant-a"); err == nil {
		t.Fatal("missing shard value must be rejected")
	}
}
