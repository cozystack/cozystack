// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Cozystack Authors.

package siterouter

import (
	"context"
	"fmt"
	"sort"
	"strings"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// reasonPendingRoutes is the stable, machine-readable reason the controller
// records when tenant workload pods have not yet inherited the site-router return
// route. It is part of the D4 machine-readable contract cozyportal consumes at
// runtime — do not rename without updating the consumers.
const reasonPendingRoutes = "PendingRoutes"

// updateStatus surfaces the instance's status. In Phase 1 the runtime readiness
// (tunnel up, source filter active) rides the gateway WorkloadMonitor (rendered by
// T10) plus the HelmRelease Ready condition, NOT a status subresource on the app
// CR — convertHelmReleaseToApplication is deliberately not extended (D9). The one
// signal this step surfaces directly is the set of tenant workload pods still
// missing the return route: kube-ovn only stamps ovn.kubernetes.io/routes onto a
// pod at CREATE, so pods that predate the route keep lagging until they restart.
// The controller reports them (count + names) via a recorded Event and MUTATES
// NOTHING — rolling a pod is the tenant's decision, never the controller's.
func (r *SiteRouterReconciler) updateStatus(ctx context.Context, inst *instance) error {
	return r.surfacePendingRoutePods(ctx, inst)
}

// surfacePendingRoutePods records an Event naming the tenant workload pods in the
// instance namespace that have not yet inherited every route entry the namespace
// carries. It is read-only: it never patches, deletes or restarts a pod. Gateway
// pods (this instance's or a co-tenant site-router's) are excluded — they are the
// next hop, not workloads that need the return route — as are pods already being
// torn down. When no route is programmed yet, or every workload is up to date,
// nothing is recorded.
func (r *SiteRouterReconciler) surfacePendingRoutePods(ctx context.Context, inst *instance) error {
	// A route is only pending once this instance actually declares remoteCIDRs.
	if len(stringSlice(inst.values[remoteCIDRsValueKey])) == 0 {
		return nil
	}

	ns := &corev1.Namespace{}
	if err := r.reader().Get(ctx, types.NamespacedName{Name: inst.namespace}, ns); err != nil {
		return fmt.Errorf("get namespace %s for pending-route surfacing: %w", inst.namespace, err)
	}
	wantDsts, err := routeDsts(ns.Annotations[routesAnnotation])
	if err != nil {
		return fmt.Errorf("decode namespace %s routes annotation: %w", inst.namespace, err)
	}
	if len(wantDsts) == 0 {
		return nil // nothing programmed yet; nothing can be pending
	}

	pods := &corev1.PodList{}
	if err := r.List(ctx, pods, client.InNamespace(inst.namespace)); err != nil {
		return fmt.Errorf("list pods in namespace %s: %w", inst.namespace, err)
	}

	var pending []string
	for i := range pods.Items {
		p := &pods.Items[i]
		// A gateway pod (this or a co-tenant site-router) is the route's next hop,
		// not a workload that needs the return route — skip it.
		if p.Labels[appKindLabelKey] == siteRouterKind {
			continue
		}
		// A pod on its way out will be replaced with the annotation inherited.
		if p.DeletionTimestamp != nil {
			continue
		}
		haveDsts, err := routeDsts(p.Annotations[routesAnnotation])
		if err != nil {
			// A pod carrying an unparseable annotation is not the controller's to
			// interpret; leave it out rather than guess it is pending.
			continue
		}
		if !coversDsts(haveDsts, wantDsts) {
			pending = append(pending, p.Name)
		}
	}
	if len(pending) == 0 {
		return nil
	}

	sort.Strings(pending)
	if r.Recorder != nil {
		r.Recorder.Eventf(inst.hr, corev1.EventTypeNormal, reasonPendingRoutes,
			"%d tenant pod(s) have not yet inherited the site-router return route and will reach the remote site only after they restart: %s",
			len(pending), strings.Join(pending, ", "))
	}
	return nil
}

// routeDsts decodes an ovn.kubernetes.io/routes annotation value into the set of
// its destination CIDRs, reusing the same decoder the mediation path uses. An
// empty value yields an empty set (not an error).
func routeDsts(annotation string) (map[string]struct{}, error) {
	entries, err := decodeRoutes(annotation)
	if err != nil {
		return nil, err
	}
	out := make(map[string]struct{}, len(entries))
	for _, e := range entries {
		out[e.Dst] = struct{}{}
	}
	return out, nil
}

// coversDsts reports whether have contains every destination in want — i.e. the
// pod already carries every route the namespace does (co-tenant extras on the pod
// are fine). An empty want is trivially covered.
func coversDsts(have, want map[string]struct{}) bool {
	for d := range want {
		if _, ok := have[d]; !ok {
			return false
		}
	}
	return true
}
