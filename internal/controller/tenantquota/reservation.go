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
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"

	helmv2 "github.com/fluxcd/helm-controller/api/v2"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/selection"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	cozyv1alpha1 "github.com/cozystack/cozystack/api/v1alpha1"
	appsv1alpha1 "github.com/cozystack/cozystack/pkg/apis/apps/v1alpha1"
	"github.com/cozystack/cozystack/pkg/reservation"
)

// compiledDefinition is an ApplicationDefinition's evaluator, kept for as
// long as the definition's generation does not move.
type compiledDefinition struct {
	generation int64
	ev         *reservation.Evaluator
	err        error
}

// evaluators caches the evaluator of every kind across sweeps: compiling a
// declaration is far more expensive than evaluating one.
type evaluators struct {
	mu     sync.Mutex
	byName map[string]compiledDefinition
}

// lookup returns the evaluator of every kind that declares its consumption,
// keyed by kind. A declaration that does not compile is kept as an error, so
// that its applications are reported as not evaluated rather than counted as
// free.
func (e *evaluators) lookup(defs []cozyv1alpha1.ApplicationDefinition) map[string]compiledDefinition {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.byName == nil {
		e.byName = map[string]compiledDefinition{}
	}
	byKind := map[string]compiledDefinition{}
	seen := map[string]bool{}
	for i := range defs {
		def := &defs[i]
		if def.Spec.Consumption == nil {
			continue
		}
		seen[def.Name] = true
		c, ok := e.byName[def.Name]
		if !ok || c.generation != def.Generation {
			ev, err := reservation.CompileDefinition(def.Spec.Application.Kind, def.Spec.Consumption, def.Spec.Application.OpenAPISchema)
			c = compiledDefinition{generation: def.Generation, ev: ev, err: err}
			e.byName[def.Name] = c
		}
		byKind[def.Spec.Application.Kind] = c
	}
	for name := range e.byName {
		if !seen[name] {
			delete(e.byName, name)
		}
	}
	return byKind
}

// reservationSnapshot is what one sweep reads with the reservation oracle on.
type reservationSnapshot struct {
	tenants []Tenant
	// tenantReleases maps the namespace a tenant owns to its HelmRelease.
	tenantReleases map[string]*helmv2.HelmRelease
	// usedByNS is what the applications in each namespace reserve.
	usedByNS map[string]corev1.ResourceList
	// incompleteByNS lists, per namespace, the applications whose figure is
	// stale or missing.
	incompleteByNS map[string][]string
}

// snapshotReservations reads every tenant's budget from its own
// resourceQuotas values, and what each namespace reserves from the
// applications in it.
//
// An application is a HelmRelease labelled with an application kind of the
// apps group, whether the apiserver admitted it or a platform chart rendered
// it, so ComputePlane pools and tenant modules are counted too. Its figure is
// the one stored on it while that still matches its values; otherwise it is
// evaluated, and the fresh figure is stored, which is how a release the gate
// never admitted gets one.
func (r *Reconciler) snapshotReservations(ctx context.Context) (*reservationSnapshot, error) {
	logger := log.FromContext(ctx)

	tenantList := &helmv2.HelmReleaseList{}
	if err := r.List(ctx, tenantList, client.MatchingLabels{appsv1alpha1.ApplicationKindLabel: tenantKind}); err != nil {
		return nil, err
	}
	snap := &reservationSnapshot{
		tenantReleases: make(map[string]*helmv2.HelmRelease, len(tenantList.Items)),
		usedByNS:       map[string]corev1.ResourceList{},
		incompleteByNS: map[string][]string{},
	}
	for i := range tenantList.Items {
		hr := &tenantList.Items[i]
		ns := ownedNamespace(hr.Namespace, strings.TrimPrefix(hr.Name, tenantNamespacePrefix))
		budget, err := reservation.TenantBudget(helmReleaseValues(hr))
		if err != nil {
			// The same reading as the gate's: a tenant whose quotas do not
			// parse is unbounded rather than blocking the whole sweep.
			logger.Error(err, "treating a tenant with unparseable resourceQuotas as unbounded", "helmRelease", client.ObjectKeyFromObject(hr))
			budget = nil
		}
		snap.tenants = append(snap.tenants, Tenant{Namespace: ns, Declared: toCore(budget)})
		snap.tenantReleases[ns] = hr
	}

	defs := &cozyv1alpha1.ApplicationDefinitionList{}
	if err := r.List(ctx, defs); err != nil {
		return nil, err
	}
	byKind := r.evaluators.lookup(defs.Items)

	kindReq, err := labels.NewRequirement(appsv1alpha1.ApplicationKindLabel, selection.Exists, nil)
	if err != nil {
		return nil, err
	}
	groupReq, err := labels.NewRequirement(appsv1alpha1.ApplicationGroupLabel, selection.Equals, []string{appsv1alpha1.GroupName})
	if err != nil {
		return nil, err
	}
	apps := &helmv2.HelmReleaseList{}
	if err := r.List(ctx, apps, client.MatchingLabelsSelector{Selector: labels.NewSelector().Add(*kindReq, *groupReq)}); err != nil {
		return nil, err
	}
	for i := range apps.Items {
		hr := &apps.Items[i]
		kind := hr.Labels[appsv1alpha1.ApplicationKindLabel]
		c, declared := byKind[kind]
		if !declared {
			// Neither gated nor counted until every kind declares its
			// consumption, as in the gate.
			continue
		}
		name := fmt.Sprintf("%s %s/%s", kind, hr.Namespace, hr.Name)
		if c.err != nil {
			snap.incompleteByNS[hr.Namespace] = append(snap.incompleteByNS[hr.Namespace], fmt.Sprintf("%s: %v", name, c.err))
			continue
		}
		prev, err := reservation.ReadStored(hr.Annotations)
		if err != nil {
			logger.Error(err, "ignoring an unreadable stored reservation", "helmRelease", client.ObjectKeyFromObject(hr))
			prev = nil
		}
		values := helmReleaseValues(hr)
		rl, src, err := reservation.Resolve(c.ev, values, prev, r.Env)
		switch {
		case err != nil:
			snap.incompleteByNS[hr.Namespace] = append(snap.incompleteByNS[hr.Namespace], fmt.Sprintf("%s: %v", name, err))
			continue
		case src == reservation.StaleStored:
			snap.incompleteByNS[hr.Namespace] = append(snap.incompleteByNS[hr.Namespace], name+": no longer evaluates, counted at its last stored reservation")
		case src == reservation.Evaluated:
			if err := r.storeReservation(ctx, hr, values, rl); err != nil {
				// The figure still counts this sweep; storing it is retried
				// on the next one.
				logger.Error(err, "failed to store an application's reservation", "helmRelease", client.ObjectKeyFromObject(hr))
			}
		}
		snap.usedByNS[hr.Namespace] = addResourceList(snap.usedByNS[hr.Namespace], toCore(rl))
	}
	return snap, nil
}

// storeReservation writes rl, evaluated from values, onto hr. The write is
// conditional on hr's resourceVersion, so that it can never pin a figure
// onto values written since it was read.
func (r *Reconciler) storeReservation(ctx context.Context, hr *helmv2.HelmRelease, values []byte, rl reservation.ResourceList) error {
	hash, err := reservation.ValuesHash(values)
	if err != nil {
		return err
	}
	raw, err := (&reservation.Stored{ValuesHash: hash, Resources: rl}).Encode()
	if err != nil {
		return err
	}
	return r.patchAnnotation(ctx, hr, reservation.ReservedAnnotation, &raw)
}

// patchAnnotation sets key on hr to *value, or removes it when value is nil.
func (r *Reconciler) patchAnnotation(ctx context.Context, hr *helmv2.HelmRelease, key string, value *string) error {
	base := hr.DeepCopy()
	if value == nil {
		if _, ok := hr.Annotations[key]; !ok {
			return nil
		}
		delete(hr.Annotations, key)
	} else {
		if cur, ok := hr.Annotations[key]; ok && cur == *value {
			return nil
		}
		if hr.Annotations == nil {
			hr.Annotations = map[string]string{}
		}
		hr.Annotations[key] = *value
	}
	return r.Patch(ctx, hr, client.MergeFromWithOptions(base, client.MergeFromWithOptimisticLock{}))
}

// reconcileReservations is the sweep with the reservation oracle on. The
// controller only observes: it publishes each pool's figures on its root
// tenant's HelmRelease, and enforces nothing, since the apiserver gate
// already checks every write into a pool against its budget. Any
// tenant-quota-allocated ResourceQuota and LimitRange copy the legacy path
// wrote are garbage-collected: clamping pods to a figure counted in
// reservations would compare two vocabularies.
func (r *Reconciler) reconcileReservations(ctx context.Context) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	snap, err := r.snapshotReservations(ctx)
	if err != nil {
		return ctrl.Result{}, err
	}
	pools := ComputePools(snap.tenants)

	for ns, hr := range snap.tenantReleases {
		p, isRoot := pools[ns]
		if !isRoot {
			if err := r.patchAnnotation(ctx, hr, reservation.PoolAnnotation, nil); err != nil {
				logger.Error(err, "failed to clear a pool status", "helmRelease", client.ObjectKeyFromObject(hr))
			}
			continue
		}
		status := r.poolStatus(hr, p, snap)
		if over := overcommitted(status); len(over) > 0 {
			logger.Info("tenant quota pool is overcommitted", "pool", p.Root, "overcommit", over)
			r.recordOvercommit(ctx, p.Root, fmt.Sprintf(
				"what this tenant's pool reserves and its sub-tenants carved out exceed its budget by %s; nothing is evicted, but no write that grows the pool is admitted until it fits",
				resourceListString(toCore(over))))
		}
		raw, err := status.Encode()
		if err != nil {
			return ctrl.Result{}, err
		}
		if err := r.patchAnnotation(ctx, hr, reservation.PoolAnnotation, &raw); err != nil {
			logger.Error(err, "failed to publish a pool status", "pool", p.Root)
		}
	}

	if err := r.gcAllocatedQuotas(ctx, nil); err != nil {
		logger.Error(err, "failed to garbage-collect allocated quotas")
	}
	if err := r.gcAllocatedLimitRanges(ctx, nil); err != nil {
		logger.Error(err, "failed to garbage-collect allocated limit ranges")
	}
	return ctrl.Result{RequeueAfter: resyncInterval}, nil
}

// poolStatus computes the figures and conditions of pool p, whose root
// tenant's release is hr. A condition keeps its transition time while its
// status does not change.
func (r *Reconciler) poolStatus(hr *helmv2.HelmRelease, p *Pool, snap *reservationSnapshot) *reservation.PoolStatus {
	reserved := corev1.ResourceList{}
	var incomplete []string
	for _, m := range p.Members {
		reserved = addResourceList(reserved, snap.usedByNS[m])
		incomplete = append(incomplete, snap.incompleteByNS[m]...)
	}
	status := &reservation.PoolStatus{
		Reserved:  fromCore(reserved),
		CarvedOut: fromCore(p.CarvedOut),
		Budget:    fromCore(p.Budget),
	}
	if prev, err := reservation.ReadPoolStatus(hr.Annotations); err == nil && prev != nil {
		status.Conditions = prev.Conditions
	}

	over := overcommitted(status)
	cond := metav1.Condition{Type: reservation.ConditionOvercommitted, Status: metav1.ConditionFalse, Reason: "WithinBudget", Message: "the pool fits its budget"}
	if len(over) > 0 {
		cond.Status = metav1.ConditionTrue
		cond.Reason = "BudgetExceeded"
		cond.Message = "reserved and carved out exceed the budget by " + summarize(over)
	}
	meta.SetStatusCondition(&status.Conditions, cond)

	cond = metav1.Condition{Type: reservation.ConditionReservationIncomplete, Status: metav1.ConditionFalse, Reason: "AllEvaluated", Message: "every application in the pool evaluates"}
	if len(incomplete) > 0 {
		sort.Strings(incomplete)
		cond.Status = metav1.ConditionTrue
		cond.Reason = "NotEvaluated"
		cond.Message = strings.Join(incomplete, "; ")
	}
	meta.SetStatusCondition(&status.Conditions, cond)
	return status
}

// overcommitted returns, for every key the budget bounds, how far what the
// pool reserves plus what its sub-tenants carved out exceeds it. A pool can
// get there although the gate checks every write: a parent lowers its quota
// below its sub-tenants' slices, or a re-evaluation raises what applications
// reserve.
func overcommitted(s *reservation.PoolStatus) reservation.ResourceList {
	over := reservation.ResourceList{}
	for k, budget := range s.Budget {
		total := s.Reserved[k].DeepCopy()
		total.Add(s.CarvedOut[k])
		total.Sub(budget)
		if total.Sign() > 0 {
			over[k] = total
		}
	}
	return over
}

// clearPoolStatuses removes the pool status from every tenant release that
// carries one, so that turning the oracle off does not leave a figure
// frozen in Tenant.status. It writes nothing on a cluster that never had the
// oracle on.
func (r *Reconciler) clearPoolStatuses(ctx context.Context) error {
	list := &helmv2.HelmReleaseList{}
	if err := r.List(ctx, list, client.MatchingLabels{appsv1alpha1.ApplicationKindLabel: tenantKind}); err != nil {
		return err
	}
	for i := range list.Items {
		if err := r.patchAnnotation(ctx, &list.Items[i], reservation.PoolAnnotation, nil); err != nil {
			return err
		}
	}
	return nil
}

func helmReleaseValues(hr *helmv2.HelmRelease) []byte {
	if hr.Spec.Values == nil {
		return nil
	}
	return hr.Spec.Values.Raw
}

func toCore(rl reservation.ResourceList) corev1.ResourceList {
	if len(rl) == 0 {
		return nil
	}
	out := make(corev1.ResourceList, len(rl))
	for k, v := range rl {
		out[corev1.ResourceName(k)] = v.DeepCopy()
	}
	return out
}

func fromCore(rl corev1.ResourceList) reservation.ResourceList {
	out := make(reservation.ResourceList, len(rl))
	for k, v := range rl {
		out[string(k)] = v.DeepCopy()
	}
	return out
}

// addResourceList returns the per-resource sum of a and b.
func addResourceList(a, b corev1.ResourceList) corev1.ResourceList {
	out := make(corev1.ResourceList, len(a)+len(b))
	for k, v := range a {
		out[k] = v.DeepCopy()
	}
	for k, v := range b {
		cur, ok := out[k]
		if !ok {
			cur = resource.Quantity{Format: v.Format}
		}
		cur.Add(v)
		out[k] = cur
	}
	return out
}

func summarize(rl reservation.ResourceList) string {
	parts := make([]string, 0, len(rl))
	for _, k := range rl.Keys() {
		q := rl[k]
		parts = append(parts, q.String()+" "+k)
	}
	return strings.Join(parts, ", ")
}
