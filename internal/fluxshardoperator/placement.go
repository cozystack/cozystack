package fluxshardoperator

import (
	"sort"
)

// TenantInfo is the placement view of one tenant.
type TenantInfo struct {
	// Namespace is the tenant namespace ("tenant-<id>") and the placement key.
	Namespace string
	// Weight is the tenant's HelmRelease count (parent + children). Shards are
	// balanced by weight, not by raw tenant count.
	Weight int
	// Current is the tenant's current assignment: a canonical "shard<i>" value
	// or "" when unassigned (never assigned, or still on the legacy "tenants"
	// bucket).
	Current string
	// Deleting tenants (namespace deletionTimestamp set) are never moved; an
	// unassigned deleting tenant is still assigned once so an owning shard can
	// finish its teardown and remove finalizers.
	Deleting bool
}

// PlacementInput is everything ComputePlacement needs to produce a desired
// assignment.
type PlacementInput struct {
	Tenants    []TenantInfo
	ShardCount int
	// Pinned maps tenant namespace -> shard name; pinned tenants bypass
	// least-loaded assignment and are excluded from rebalancing. Pins to
	// non-existent shards (index >= ShardCount) are ignored.
	Pinned map[string]string
	// RebalanceThreshold is the (maxLoad-minLoad)/avgLoad ratio above which
	// rebalance moves are produced. <= 0 disables rebalancing.
	RebalanceThreshold float64
	// CanRebalance gates rebalance moves per tenant (move cooldown). nil means
	// no gate. It is consulted for rebalance moves only — first assignments and
	// forced moves (scale-down, pin) always go through.
	CanRebalance func(tenantNamespace string) bool
}

// ComputePlacement returns the desired tenant->shard assignment.
//
// Strategy is greedy least-loaded by weight (deterministic: with uniform
// weights, N tenants over N shards land exactly 1 per shard), with minimal
// movement on rescale:
//   - tenants whose current shard still exists keep it (scale-up never touches
//     settled tenants; scale-down only moves tenants of removed shards);
//   - unassigned tenants (and tenants of removed shards) are placed
//     least-loaded-first, heaviest tenants first;
//   - a bounded rebalance pass then moves tenants from the most- to the
//     least-loaded shard while it strictly reduces the spread and the spread
//     exceeds RebalanceThreshold, preferring the smallest tenant that achieves
//     the reduction (cheaper handoff).
func ComputePlacement(in PlacementInput) map[string]string {
	k := in.ShardCount
	if k < 1 {
		k = 1
	}

	assign := make(map[string]string, len(in.Tenants))
	load := make([]int, k)
	members := make([][]TenantInfo, k)

	place := func(t TenantInfo, idx int) {
		assign[t.Namespace] = ShardName(idx)
		load[idx] += t.Weight
		members[idx] = append(members[idx], t)
	}

	// Deterministic processing order.
	tenants := make([]TenantInfo, len(in.Tenants))
	copy(tenants, in.Tenants)
	sort.Slice(tenants, func(i, j int) bool { return tenants[i].Namespace < tenants[j].Namespace })

	var unassigned []TenantInfo
	for _, t := range tenants {
		if pin, ok := in.Pinned[t.Namespace]; ok && !t.Deleting {
			if idx, ok := ParseShardIndex(pin); ok && idx < k {
				place(t, idx)
				continue
			}
		}
		if idx, ok := ParseShardIndex(t.Current); ok {
			if idx < k {
				place(t, idx)
				continue
			}
			if t.Deleting {
				// Never move a deleting tenant, even off a removed shard: the
				// provisioner keeps the shard Deployment until it drains.
				assign[t.Namespace] = t.Current
				continue
			}
		}
		unassigned = append(unassigned, t)
	}

	// Heaviest first (LPT) gives the better balance when backfilling many
	// tenants at once; name tie-break keeps it deterministic.
	sort.Slice(unassigned, func(i, j int) bool {
		if unassigned[i].Weight != unassigned[j].Weight {
			return unassigned[i].Weight > unassigned[j].Weight
		}
		return unassigned[i].Namespace < unassigned[j].Namespace
	})
	for _, t := range unassigned {
		place(t, argminLoad(load))
	}

	rebalance(in, k, assign, load, members)
	return assign
}

// rebalance moves tenants from the most-loaded to the least-loaded shard while
// the spread exceeds the threshold and a strictly improving move exists.
func rebalance(in PlacementInput, k int, assign map[string]string, load []int, members [][]TenantInfo) {
	if k < 2 || in.RebalanceThreshold <= 0 {
		return
	}
	total := 0
	for _, l := range load {
		total += l
	}
	if total == 0 {
		return
	}
	avg := float64(total) / float64(k)

	for iter := 0; iter < len(in.Tenants); iter++ {
		maxIdx, minIdx := argmaxLoad(load), argminLoad(load)
		spread := load[maxIdx] - load[minIdx]
		if float64(spread)/avg <= in.RebalanceThreshold {
			return
		}

		// Pick the move that most reduces the spread between the two shards;
		// among equal reductions prefer the smaller tenant (cheaper handoff).
		best := -1
		bestSpread, bestWeight := spread, 0
		for i, t := range members[maxIdx] {
			if t.Deleting || t.Weight == 0 {
				continue
			}
			if _, pinned := in.Pinned[t.Namespace]; pinned {
				continue
			}
			if in.CanRebalance != nil && !in.CanRebalance(t.Namespace) {
				continue
			}
			newSpread := abs((load[maxIdx] - t.Weight) - (load[minIdx] + t.Weight))
			if newSpread < bestSpread || (newSpread == bestSpread && best >= 0 && t.Weight < bestWeight) {
				best, bestSpread, bestWeight = i, newSpread, t.Weight
			}
		}
		if best < 0 || bestSpread >= spread {
			return
		}

		t := members[maxIdx][best]
		members[maxIdx] = append(members[maxIdx][:best:best], members[maxIdx][best+1:]...)
		members[minIdx] = append(members[minIdx], t)
		load[maxIdx] -= t.Weight
		load[minIdx] += t.Weight
		assign[t.Namespace] = ShardName(minIdx)
	}
}

// argminLoad returns the least-loaded shard index, tie-break lowest index.
func argminLoad(load []int) int {
	idx := 0
	for i, l := range load {
		if l < load[idx] {
			idx = i
		}
	}
	return idx
}

// argmaxLoad returns the most-loaded shard index, tie-break lowest index.
func argmaxLoad(load []int) int {
	idx := 0
	for i, l := range load {
		if l > load[idx] {
			idx = i
		}
	}
	return idx
}

func abs(v int) int {
	if v < 0 {
		return -v
	}
	return v
}

// Autosizing parameters: shards are sized by HelmRelease count, capped by
// tenant count (more shards than tenants is useless). The 100 HR/shard target
// leaves headroom for tenants growing their HelmRelease count without an
// immediate reshard. Hysteresis keeps K stable around the boundaries: scale up
// eagerly once a shard runs 20% over target, scale down lazily only when
// shards run at well under half the target.
const (
	hrTargetPerShard  = 100
	maxAutoShards     = 16
	scaleUpPerShard   = hrTargetPerShard * 1.2
	scaleDownPerShard = hrTargetPerShard * 0.6
)

// RecommendedShardCount is the raw autosizing formula:
// K = clamp(ceil(H / hrTargetPerShard), 1, min(maxAutoShards, T)).
func RecommendedShardCount(helmReleases, tenants int) int {
	k := (helmReleases + hrTargetPerShard - 1) / hrTargetPerShard
	if k < 1 {
		k = 1
	}
	limit := maxAutoShards
	if tenants > 0 && tenants < limit {
		limit = tenants
	}
	if k > limit {
		k = limit
	}
	return k
}

// EffectiveShardCount resolves the shard count for this sync. Explicit
// configuration wins; in auto mode the recommendation is applied with
// hysteresis anchored on the currently provisioned shard count, so K does not
// flap when the HelmRelease count hovers around a sizing boundary. A
// structurally over-provisioned tail (more shards than tenants after tenant
// deletions) is left alone until the load condition triggers — idle shards
// watch zero objects and cost next to nothing.
func (c *Config) EffectiveShardCount(helmReleases, tenants, currentShards int) int {
	if !c.AutoShardCount {
		if c.ShardCount < 1 {
			return 1
		}
		return c.ShardCount
	}
	desired := RecommendedShardCount(helmReleases, tenants)
	if currentShards < 1 || desired == currentShards {
		return max(desired, currentShards)
	}
	perShard := float64(helmReleases) / float64(currentShards)
	if desired > currentShards && perShard > scaleUpPerShard {
		return desired
	}
	if desired < currentShards && perShard < scaleDownPerShard {
		return desired
	}
	return currentShards
}
