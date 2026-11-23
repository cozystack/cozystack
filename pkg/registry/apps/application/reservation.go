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

package application

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	helmv2 "github.com/fluxcd/helm-controller/api/v2"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/selection"
	"k8s.io/apimachinery/pkg/util/validation/field"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/klog/v2"
	"sigs.k8s.io/controller-runtime/pkg/client"

	appsv1alpha1 "github.com/cozystack/cozystack/pkg/apis/apps/v1alpha1"
	"github.com/cozystack/cozystack/pkg/apis/apps/validation"
	"github.com/cozystack/cozystack/pkg/config"
	"github.com/cozystack/cozystack/pkg/reservation"
)

// ReservedAnnotation carries, on an application's HelmRelease, the
// reservation the gate admitted it with. It sits outside AnnotationPrefix, so
// an Application's own annotations can never write it.
const ReservedAnnotation = reservation.ReservedAnnotation

// tenantNamespacePrefix is the prefix of every tenant namespace and of every
// Tenant HelmRelease, whatever the REST handler's own release prefix is.
const tenantNamespacePrefix = "tenant-"

const (
	defaultSettleTimeout = 5 * time.Second
	settleInterval       = 50 * time.Millisecond
)

type kindEvaluator struct {
	ev  *reservation.Evaluator
	err error
}

// Reservation is the tenant-quota reservation gate. One instance is shared by
// the REST handlers of every kind, since a pool's figure sums applications of
// all kinds and its lock has to be the same whichever kind is written.
type Reservation struct {
	evaluators    map[string]kindEvaluator
	env           reservation.Environment
	settleTimeout time.Duration

	mu    sync.Mutex
	locks map[string]*sync.Mutex
}

// NewReservation compiles the consumption declaration of every resource. A
// declaration that does not compile is kept as an error, so that its kind is
// refused rather than counted as free.
func NewReservation(resources []config.Resource, env reservation.Environment) *Reservation {
	g := &Reservation{
		evaluators:    map[string]kindEvaluator{},
		env:           env,
		settleTimeout: defaultSettleTimeout,
		locks:         map[string]*sync.Mutex{},
	}
	for _, res := range resources {
		decl := res.Application.Consumption
		if decl == nil {
			continue
		}
		kind := res.Application.Kind
		ev, err := reservation.CompileDefinition(kind, decl, res.Application.OpenAPISchema)
		if err != nil {
			klog.Error(err)
			g.evaluators[kind] = kindEvaluator{err: err}
			continue
		}
		g.evaluators[kind] = kindEvaluator{ev: ev}
	}
	return g
}

// evaluator returns the evaluator of a kind, or nil for a kind that declares
// no consumption. Such a kind is neither gated nor counted until every kind
// declares one; refusing it before then would refuse every catalog kind that
// has not been given its declaration yet.
func (g *Reservation) evaluator(kind, group string) (*reservation.Evaluator, error) {
	if group != appsv1alpha1.GroupName {
		return nil, nil
	}
	k, ok := g.evaluators[kind]
	if !ok {
		return nil, nil
	}
	return k.ev, k.err
}

// lock serialises the gate per pool root within this process, from the check
// to the moment the informer cache shows the write, so that a client's
// sequential writes into one pool each see the previous one.
func (g *Reservation) lock(root string) func() {
	g.mu.Lock()
	m, ok := g.locks[root]
	if !ok {
		m = &sync.Mutex{}
		g.locks[root] = m
	}
	g.mu.Unlock()
	m.Lock()
	return m.Unlock
}

func helmReleaseValues(hr *helmv2.HelmRelease) []byte {
	if hr.Spec.Values == nil {
		return nil
	}
	return hr.Spec.Values.Raw
}

// stored reads ReservedAnnotation, or nil when it is absent or unreadable.
func stored(hr *helmv2.HelmRelease) *reservation.Stored {
	s, err := reservation.ReadStored(hr.Annotations)
	if err != nil {
		klog.Warningf("ignoring unreadable annotation on HelmRelease %s/%s: %v", hr.Namespace, hr.Name, err)
		return nil
	}
	return s
}

// reservedBy returns what a stored application reserves, by the rule of
// reservation.Resolve.
func (g *Reservation) reservedBy(hr *helmv2.HelmRelease) (rl reservation.ResourceList, stale bool, err error) {
	ev, err := g.evaluator(hr.Labels[ApplicationKindLabel], hr.Labels[ApplicationGroupLabel])
	if err != nil {
		return nil, false, err
	}
	if ev == nil {
		return nil, false, nil
	}
	rl, src, err := reservation.Resolve(ev, helmReleaseValues(hr), stored(hr), g.env)
	return rl, src == reservation.StaleStored, err
}

// tenantOwnedNamespace is the namespace a Tenant HelmRelease owns. It is
// computeTenantNamespace for callers that are not the Tenant handler, whose
// release prefix is not the tenant prefix.
func tenantOwnedNamespace(hrNamespace, hrName string) string {
	switch {
	case hrNamespace == rootTenantNamespace && hrName == rootTenantNamespace:
		return rootTenantNamespace
	case hrNamespace == rootTenantNamespace:
		return hrName
	default:
		return hrNamespace + "-" + strings.TrimPrefix(hrName, tenantNamespacePrefix)
	}
}

// tenantParentNamespace is parentNamespaceOf for the tenant prefix.
func tenantParentNamespace(ns string) string {
	if ns == rootTenantNamespace || !strings.HasPrefix(ns, tenantNamespacePrefix) {
		return ""
	}
	segments := strings.Split(ns, "-")
	if len(segments) == 2 {
		return rootTenantNamespace
	}
	return strings.Join(segments[:len(segments)-1], "-")
}

// pool is the budget a namespace draws from: its nearest bounded tenant,
// that tenant's unbounded descendants, and the slices its bounded
// sub-tenants carved out of it. It is the admission-side counterpart of
// tenantquota.Pool.
type pool struct {
	root    string
	budget  reservation.ResourceList
	carved  reservation.ResourceList
	members []string
}

// tenantTree reads the declared quota of every tenant, keyed by the
// namespace it owns.
func (r *REST) tenantTree(ctx context.Context) (map[string]map[string]resource.Quantity, error) {
	list := &helmv2.HelmReleaseList{}
	selector := labels.SelectorFromSet(labels.Set{
		ApplicationKindLabel:  validation.TenantKind,
		ApplicationGroupLabel: appsv1alpha1.GroupName,
	})
	if err := r.c.List(ctx, list, &client.ListOptions{LabelSelector: selector}); err != nil {
		return nil, err
	}
	tree := make(map[string]map[string]resource.Quantity, len(list.Items))
	for i := range list.Items {
		hr := &list.Items[i]
		ns := tenantOwnedNamespace(hr.Namespace, hr.Name)
		quotas, err := declaredQuotasFromHelmRelease(hr)
		if err != nil {
			klog.Warningf("treating tenant %s/%s with unparseable quotas as unbounded: %v", hr.Namespace, hr.Name, err)
			quotas = nil
		}
		tree[ns] = quotas
	}
	return tree, nil
}

// poolOf returns the pool governing ns, or nil when no tenant above it
// declares a quota.
func poolOf(tree map[string]map[string]resource.Quantity, ns string) *pool {
	rootOf := func(ns string) string {
		for cur := ns; cur != ""; cur = tenantParentNamespace(cur) {
			if len(tree[cur]) > 0 {
				return cur
			}
		}
		return ""
	}
	root := rootOf(ns)
	if root == "" {
		return nil
	}
	p := &pool{root: root, budget: reservation.Budget(tree[root]), carved: reservation.ResourceList{}}
	for owned, declared := range tree {
		if rootOf(owned) == root {
			p.members = append(p.members, owned)
		}
		if owned != root && len(declared) > 0 && rootOf(tenantParentNamespace(owned)) == root {
			p.carved.Add(reservation.Budget(declared))
		}
	}
	if _, ok := tree[root]; ok && !containsString(p.members, root) {
		p.members = append(p.members, root)
	}
	return p
}

func containsString(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

// poolFigure is what a pool's members reserve.
type poolFigure struct {
	reserved     reservation.ResourceList
	applications int
}

// reservedIn sums the reservations of the applications in namespaces. An
// application that cannot be evaluated is logged and left out; it must not
// block an unrelated write.
func (r *REST) reservedIn(ctx context.Context, namespaces []string) (poolFigure, error) {
	fig := poolFigure{reserved: reservation.ResourceList{}}
	kindReq, err := labels.NewRequirement(ApplicationKindLabel, selection.Exists, nil)
	if err != nil {
		return fig, err
	}
	groupReq, err := labels.NewRequirement(ApplicationGroupLabel, selection.Equals, []string{appsv1alpha1.GroupName})
	if err != nil {
		return fig, err
	}
	selector := labels.NewSelector().Add(*kindReq, *groupReq)
	for _, ns := range namespaces {
		list := &helmv2.HelmReleaseList{}
		if err := r.c.List(ctx, list, &client.ListOptions{Namespace: ns, LabelSelector: selector}); err != nil {
			return fig, err
		}
		for i := range list.Items {
			hr := &list.Items[i]
			rl, stale, err := r.reservation.reservedBy(hr)
			if err != nil {
				klog.Warningf("leaving %s %s/%s out of its pool's reservation: %v", hr.Labels[ApplicationKindLabel], hr.Namespace, hr.Name, err)
				continue
			}
			if stale {
				klog.Warningf("%s %s/%s no longer evaluates; counting the reservation it was admitted with", hr.Labels[ApplicationKindLabel], hr.Namespace, hr.Name)
			}
			if len(rl) == 0 {
				continue
			}
			fig.reserved.Add(rl)
			fig.applications++
		}
	}
	return fig, nil
}

// reservationAdmission is the outcome of the gate for one write: the figure to store on
// the release and the lock to release once the write is visible.
type reservationAdmission struct {
	gate   *Reservation
	stored *reservation.Stored
	unlock func()
}

// release drops the pool lock. Safe on a nil reservationAdmission.
func (a *reservationAdmission) release() {
	if a != nil && a.unlock != nil {
		a.unlock()
		a.unlock = nil
	}
}

// annotate writes the admitted figure onto the release about to be stored.
func (a *reservationAdmission) annotate(hr *helmv2.HelmRelease) error {
	if a == nil || a.stored == nil {
		return nil
	}
	raw, err := a.stored.Encode()
	if err != nil {
		return err
	}
	if hr.Annotations == nil {
		hr.Annotations = map[string]string{}
	}
	hr.Annotations[ReservedAnnotation] = raw
	return nil
}

// settle waits until the cache serves the release as written, so that the
// next writer into the pool, which reads the cache, counts it. A timeout is
// logged and not fatal: the write has happened.
func (a *reservationAdmission) settle(ctx context.Context, c client.Client, key client.ObjectKey) {
	if a == nil || a.stored == nil || a.unlock == nil {
		return
	}
	want := a.stored.ValuesHash
	err := wait.PollUntilContextTimeout(ctx, settleInterval, a.gate.settleTimeout, true, func(ctx context.Context) (bool, error) {
		hr := &helmv2.HelmRelease{}
		if err := c.Get(ctx, key, hr); err != nil {
			if apierrors.IsNotFound(err) {
				return false, nil
			}
			return false, err
		}
		s := stored(hr)
		return s != nil && s.ValuesHash == want, nil
	})
	if err != nil {
		klog.Warningf("HelmRelease %s is not in the cache yet after %s; releasing its pool lock anyway: %v", key, a.gate.settleTimeout, err)
	}
}

// admitReservation is the reservation gate for a write of app, whose stored
// release is old (nil on Create). It returns a nil reservationAdmission when the gate
// is off or the kind declares nothing. Otherwise the caller holds the pool
// lock until it calls release, and must annotate the release it writes and
// settle after writing.
//
// Only the delta counts: an edit that reserves nothing more is accepted even
// in a pool that is already over budget, and shrinking is always accepted.
func (r *REST) admitReservation(ctx context.Context, app *appsv1alpha1.Application, old *helmv2.HelmRelease) (*reservationAdmission, field.ErrorList) {
	g := r.reservation
	if g == nil {
		return nil, nil
	}
	specPath := field.NewPath("spec")
	ev, err := g.evaluator(r.kindName, r.gvk.Group)
	if err != nil {
		return nil, field.ErrorList{field.Forbidden(specPath, fmt.Sprintf("%s cannot be admitted: %v", r.kindName, err))}
	}
	if ev == nil {
		return nil, nil
	}

	var raw []byte
	if app.Spec != nil {
		raw = app.Spec.Raw
	}
	want, err := ev.EvaluateJSON(raw, g.env)
	if err != nil {
		return nil, field.ErrorList{field.Invalid(specPath, "", fmt.Sprintf("cannot compute what this %s reserves from its tenant's quota: %v", r.kindName, err))}
	}
	hash, err := reservation.ValuesHash(raw)
	if err != nil {
		return nil, field.ErrorList{field.Invalid(specPath, "", err.Error())}
	}
	adm := &reservationAdmission{gate: g, stored: &reservation.Stored{ValuesHash: hash, Resources: want}}

	delta := want.DeepCopy()
	if old != nil {
		prev, _, err := g.reservedBy(old)
		if err != nil {
			// The stored object no longer evaluates and carries no figure:
			// count the whole new reservation, the conservative reading.
			klog.Warningf("%s %s/%s: no previous reservation to subtract: %v", r.kindName, old.Namespace, old.Name, err)
		} else {
			delta.Sub(prev)
		}
	}
	growing := false
	for _, k := range delta.Keys() {
		if q := delta[k]; q.Sign() > 0 {
			growing = true
			break
		}
	}
	if !growing {
		return adm, nil
	}

	tree, err := r.tenantTree(ctx)
	if err != nil {
		return nil, field.ErrorList{field.InternalError(specPath, err)}
	}
	p := poolOf(tree, app.Namespace)
	if p == nil {
		return adm, nil
	}
	adm.unlock = g.lock(p.root)

	// The object being updated stays in the figure at its old reservation,
	// so that what is checked against the budget is the delta.
	fig, err := r.reservedIn(ctx, p.members)
	if err != nil {
		adm.release()
		return nil, field.ErrorList{field.InternalError(specPath, err)}
	}

	var errs field.ErrorList
	for _, k := range delta.Keys() {
		requested := delta[k]
		limit, bounded := p.budget[k]
		if !bounded || requested.Sign() <= 0 {
			continue
		}
		reserved := fig.reserved[k]
		carved := p.carved[k]
		remaining := limit.DeepCopy()
		remaining.Sub(reserved)
		remaining.Sub(carved)
		if requested.Cmp(remaining) <= 0 {
			continue
		}
		msg := fmt.Sprintf("reserving %s would exceed the remaining %q budget of tenant pool %q: %s allowed, %s already reserved by %d applications",
			summarize(want), k, p.root, limit.String(), reserved.String(), fig.applications)
		if !carved.IsZero() {
			msg += fmt.Sprintf(", %s carved out by sub-tenants", carved.String())
		}
		msg += fmt.Sprintf(", %s requested", requested.String())
		errs = append(errs, field.Forbidden(specPath, msg))
	}
	if len(errs) > 0 {
		adm.release()
		return nil, errs
	}
	return adm, nil
}

// summarize renders a reservation for an error message.
func summarize(rl reservation.ResourceList) string {
	parts := make([]string, 0, len(rl))
	for _, k := range rl.Keys() {
		q := rl[k]
		if q.IsZero() {
			continue
		}
		parts = append(parts, fmt.Sprintf("%s %s", q.String(), k))
	}
	if len(parts) == 0 {
		return "nothing"
	}
	return strings.Join(parts, ", ")
}
