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

// Package tenantlogrouting keeps the node fluent-bit's tenant log routes in
// step with the namespaces that declare them.
//
// The monitoring-agents chart builds its routes with a Helm lookup of every
// Namespace's namespace.cozystack.io/monitoring label (see
// packages/system/monitoring-agents/templates/_tenant-log-routes.tpl).
// helm-controller only upgrades a release when its chart or values digest
// changes, and lookup results are part of neither, so a tenant that turns
// monitoring on would get no route, and a removed one would keep its route,
// until some unrelated change upgraded the release. This reconciler closes
// that gap: it digests the label across all namespaces and, when the digest
// moves, requests a forced upgrade of the release, which re-runs the lookup.
//
// The digest covers every namespace carrying the label, whatever its value,
// rather than repeating the chart's selection (non-empty and not
// global.target). A superset can only cost an upgrade that renders the same
// config, while a copy of the rule could drift from the chart and miss one.
// For the same reason a release that carries no digest yet is forced once on
// first sight: that also covers a namespace labelled between the install
// render and the first sweep.
package tenantlogrouting

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"sort"
	"strings"

	helmv2 "github.com/fluxcd/helm-controller/api/v2"
	fluxmeta "github.com/fluxcd/pkg/apis/meta"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

const (
	// MonitoringLabel is written by the apps/tenant chart and names the tenant
	// whose monitoring stack a namespace reports to.
	MonitoringLabel = "namespace.cozystack.io/monitoring"

	// DigestAnnotation records, on the HelmRelease, the digest the last forced
	// upgrade was requested for. It is compared instead of requestedAt, which
	// anyone running `flux reconcile` overwrites, so that a manual reconcile
	// does not make the next sweep force one more upgrade.
	DigestAnnotation = "monitoring.cozystack.io/log-routing-digest"
)

// Release is the node-agent HelmRelease whose fluent-bit config carries the
// routes (packages/core/platform/sources/monitoring-agents.yaml).
var Release = types.NamespacedName{Namespace: "cozy-monitoring", Name: "monitoring-agents"}

// Reconciler requests a forced upgrade of Release whenever the set of
// namespace monitoring labels changes.
type Reconciler struct {
	client.Client
}

// +kubebuilder:rbac:groups="",resources=namespaces,verbs=get;list;watch
// +kubebuilder:rbac:groups=helm.toolkit.fluxcd.io,resources=helmreleases,verbs=get;list;watch;patch

func (r *Reconciler) Reconcile(ctx context.Context, _ ctrl.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	hr := &helmv2.HelmRelease{}
	if err := r.Get(ctx, Release, hr); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	namespaces := &corev1.NamespaceList{}
	if err := r.List(ctx, namespaces); err != nil {
		return ctrl.Result{}, err
	}
	digest := Digest(namespaces.Items)
	if hr.GetAnnotations()[DigestAnnotation] == digest {
		return ctrl.Result{}, nil
	}

	patch := client.MergeFrom(hr.DeepCopy())
	annotations := hr.GetAnnotations()
	if annotations == nil {
		annotations = map[string]string{}
	}
	// helm-controller runs a forced upgrade when forceAt equals requestedAt and
	// differs from the last one it handled; the digest serves as both tokens.
	annotations[DigestAnnotation] = digest
	annotations[fluxmeta.ReconcileRequestAnnotation] = digest
	annotations[fluxmeta.ForceRequestAnnotation] = digest
	hr.SetAnnotations(annotations)
	if err := r.Patch(ctx, hr, patch); err != nil {
		return ctrl.Result{}, err
	}
	logger.Info("namespace monitoring labels changed, forcing an upgrade of the node agents", "release", Release.String(), "digest", digest)
	return ctrl.Result{}, nil
}

// Digest hashes the monitoring label of every namespace that carries it, in
// namespace order, so it moves exactly when a namespace gains the label,
// loses it, changes its value, or is deleted.
func Digest(namespaces []corev1.Namespace) string {
	pairs := make([]string, 0, len(namespaces))
	for i := range namespaces {
		if target, ok := namespaces[i].Labels[MonitoringLabel]; ok {
			pairs = append(pairs, namespaces[i].Name+"="+target)
		}
	}
	sort.Strings(pairs)
	sum := sha256.Sum256([]byte(strings.Join(pairs, "\n")))
	return hex.EncodeToString(sum[:8])
}

// sweep coalesces every event into one reconcile: the digest spans all
// namespaces, so there is nothing per-object to reconcile.
var sweep = reconcile.Request{NamespacedName: Release}

func (r *Reconciler) SetupWithManager(mgr ctrl.Manager) error {
	toSweep := handler.EnqueueRequestsFromMapFunc(func(context.Context, client.Object) []reconcile.Request {
		return []reconcile.Request{sweep}
	})
	labelled := func(o client.Object) bool {
		_, ok := o.GetLabels()[MonitoringLabel]
		return ok
	}
	monitoringLabelChanged := predicate.Funcs{
		CreateFunc: func(e event.CreateEvent) bool { return labelled(e.Object) },
		DeleteFunc: func(e event.DeleteEvent) bool { return labelled(e.Object) },
		UpdateFunc: func(e event.UpdateEvent) bool {
			oldValue, oldOK := e.ObjectOld.GetLabels()[MonitoringLabel]
			newValue, newOK := e.ObjectNew.GetLabels()[MonitoringLabel]
			return oldOK != newOK || oldValue != newValue
		},
		GenericFunc: func(event.GenericEvent) bool { return false },
	}
	isRelease := predicate.NewPredicateFuncs(func(o client.Object) bool {
		return o.GetNamespace() == Release.Namespace && o.GetName() == Release.Name
	})
	return ctrl.NewControllerManagedBy(mgr).
		Named("tenantlogrouting-controller").
		Watches(&corev1.Namespace{}, toSweep, builder.WithPredicates(monitoringLabelChanged)).
		Watches(&helmv2.HelmRelease{}, toSweep, builder.WithPredicates(isRelease)).
		Complete(r)
}
