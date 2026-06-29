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

// Package tenantquota implements hierarchical (recursive) resource quotas for
// the Cozystack tenant tree. A tenant's declared quota is the budget for its
// whole sub-tree: a child tenant that declares its own quota carves a fixed
// slice out of its parent's budget, while a child without a quota shares the
// parent's remaining pool.
//
// The model and the "a label-selector spans many namespaces whose usage is
// aggregated for a shared budget" mechanism are adapted from OpenShift's
// ClusterResourceQuota controllers:
//   - github.com/openshift/api (quota/v1)
//   - github.com/openshift/library-go (pkg/quota/clusterquotamapping)
//   - github.com/openshift/cluster-policy-controller (pkg/quota/clusterquotareconciliation)
//   - github.com/openshift/apiserver-library-go (pkg/admission/quota/clusterresourcequota)
//
// Copyright Red Hat, Inc. and the OpenShift Authors; licensed under the Apache
// License, Version 2.0. Like those controllers, the per-namespace quota
// arithmetic is delegated to the upstream Kubernetes quota helpers
// (k8s.io/apiserver/pkg/quota/v1) rather than reimplemented.
package tenantquota

import (
	"sort"
	"strings"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	quota "k8s.io/apiserver/pkg/quota/v1"
)

// rootTenantNamespace is the namespace of the cluster's root tenant, which is
// its own parent in the hierarchy.
const rootTenantNamespace = "tenant-root"

// tenantNamespacePrefix is the common prefix of every tenant namespace.
const tenantNamespacePrefix = "tenant-"

// Tenant is a snapshot of one tenant in the hierarchy: the namespace it owns
// and its declared resourceQuotas. A nil/empty Declared marks an unbounded
// tenant, which draws from the pool of its nearest bounded ancestor.
type Tenant struct {
	Namespace string
	Declared  corev1.ResourceList
}

// parentNamespace returns the namespace owned by the parent of the tenant that
// owns ns, or "" for the root tenant and non-tenant namespaces.
//
// The hierarchy is encoded in the namespace name by the tenant chart: a tenant
// "x" created in namespace P owns namespace computeTenantNamespace(P, x) — i.e.
// P + "-" + x, or "tenant-x" directly under root — so the parent namespace is
// recovered by stripping the trailing "-x" segment.
func parentNamespace(ns string) string {
	if ns == rootTenantNamespace || !strings.HasPrefix(ns, tenantNamespacePrefix) {
		return ""
	}
	segments := strings.Split(ns, "-")
	if len(segments) == 2 {
		return rootTenantNamespace
	}
	return strings.Join(segments[:len(segments)-1], "-")
}

// poolRootOf returns the namespace of the nearest ancestor tenant (inclusive of
// ns itself) that declares a quota — the "pool root" whose budget governs ns.
// It returns "" when no ancestor is bounded, meaning ns is governed by no pool.
func poolRootOf(ns string, declaredByNS map[string]corev1.ResourceList) string {
	for cur := ns; cur != ""; cur = parentNamespace(cur) {
		if len(declaredByNS[cur]) > 0 {
			return cur
		}
	}
	return ""
}

// Pool is a budget shared by a set of namespaces: the pool root's own namespace
// plus every unbounded descendant whose nearest bounded ancestor is the root.
// Bounded sub-tenants are not members — they form their own pools — but their
// declared quota is carved out of this pool's budget.
type Pool struct {
	Root      string
	Budget    corev1.ResourceList // the root tenant's declared quota
	CarvedOut corev1.ResourceList // sum of bounded sub-tenant reservations charged here
	Available corev1.ResourceList // max(Budget-CarvedOut, 0), shared by Members
	Members   []string            // namespaces that draw from Available
}

// ComputePools derives the pool structure for a snapshot of tenants. Each
// bounded tenant becomes a pool root; every namespace is assigned to its
// nearest bounded ancestor's pool; each bounded sub-tenant's declared quota is
// charged as a carve-out against its parent pool.
func ComputePools(tenants []Tenant) map[string]*Pool {
	declaredByNS := make(map[string]corev1.ResourceList, len(tenants))
	allNS := make([]string, 0, len(tenants))
	for _, t := range tenants {
		allNS = append(allNS, t.Namespace)
		if len(t.Declared) > 0 {
			declaredByNS[t.Namespace] = t.Declared
		}
	}
	sort.Strings(allNS)

	pools := make(map[string]*Pool, len(declaredByNS))
	for ns, declared := range declaredByNS {
		pools[ns] = &Pool{
			Root:      ns,
			Budget:    declared.DeepCopy(),
			CarvedOut: corev1.ResourceList{},
		}
	}

	// Membership: every namespace draws from its nearest bounded ancestor's
	// pool (a bounded tenant's own namespace draws from its own pool).
	for _, ns := range allNS {
		if root := poolRootOf(ns, declaredByNS); root != "" {
			pools[root].Members = append(pools[root].Members, ns)
		}
	}

	// Carve-outs: a bounded sub-tenant reserves its declared budget out of the
	// pool of its parent's nearest bounded ancestor.
	for ns, declared := range declaredByNS {
		if parentPool := poolRootOf(parentNamespace(ns), declaredByNS); parentPool != "" {
			pools[parentPool].CarvedOut = quota.Add(pools[parentPool].CarvedOut, declared)
		}
	}

	for _, p := range pools {
		p.Available = quota.SubtractWithNonNegativeResult(p.Budget, p.CarvedOut)
		sort.Strings(p.Members)
	}
	return pools
}

// Overcommitted reports the resources whose carve-outs exceed the pool budget,
// i.e. where sub-tenants have collectively been promised more than the root
// tenant owns. This should be prevented at admission, but a parent quota that
// is lowered after children exist can still produce it, so the controller
// surfaces it.
//
// Only resources the budget actually constrains are considered: a child that
// bounds a resource its parent leaves unbounded is allowed at admission, and an
// unbounded budget can never be overcommitted, so such resources must not be
// reported here.
func (p *Pool) Overcommitted() corev1.ResourceList {
	over := quota.Subtract(p.CarvedOut, p.Budget)
	result := corev1.ResourceList{}
	for name, qty := range over {
		if _, bounded := p.Budget[name]; !bounded {
			continue
		}
		if qty.Sign() > 0 {
			result[name] = qty
		}
	}
	return result
}

// EnforcedHard computes the per-namespace ResourceQuota hard limit that keeps
// member namespace ns within its pool: the pool's available budget minus what
// every other member is currently using. Restricted to the resources the pool
// budget actually constrains.
//
// This realises the shared pool at runtime with plain per-namespace
// ResourceQuotas. It is eventually consistent: concurrent admission across
// members can briefly overshoot before the controller re-clamps — the trade-off
// OpenShift's reconciler has and closes with its admission plugin.
func (p *Pool) EnforcedHard(ns string, usedByNS map[string]corev1.ResourceList) corev1.ResourceList {
	othersUsed := corev1.ResourceList{}
	for _, member := range p.Members {
		if member == ns {
			continue
		}
		othersUsed = quota.Add(othersUsed, usedByNS[member])
	}
	hard := quota.SubtractWithNonNegativeResult(p.Available, othersUsed)
	return quota.Mask(hard, quota.ResourceNames(p.Available))
}

// ScaleResourceList returns rl scaled by percent (e.g. percent=120 grows every
// quantity by 20%). It is used to apply the temporary upgrade buffer that keeps
// pre-existing workloads admissible while operators right-size their quotas
// after this feature is rolled out.
func ScaleResourceList(rl corev1.ResourceList, percent int64) corev1.ResourceList {
	if percent == 100 || len(rl) == 0 {
		return rl.DeepCopy()
	}
	out := make(corev1.ResourceList, len(rl))
	for name, qty := range rl {
		// MilliValue keeps sub-unit precision (e.g. CPU millicores) intact.
		milli := qty.MilliValue() * percent / 100
		out[name] = *resource.NewMilliQuantity(milli, qty.Format)
	}
	return out
}
