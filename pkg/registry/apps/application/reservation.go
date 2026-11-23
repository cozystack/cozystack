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
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
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

	"github.com/cozystack/cozystack/pkg/apis/apps/defaulting"
	appsv1alpha1 "github.com/cozystack/cozystack/pkg/apis/apps/v1alpha1"
	"github.com/cozystack/cozystack/pkg/apis/apps/validation"
	"github.com/cozystack/cozystack/pkg/config"
	"github.com/cozystack/cozystack/pkg/reservation"
)

// ReservedAnnotation carries, on an application's HelmRelease, the
// reservation the gate admitted it with. It sits outside AnnotationPrefix, so
// an Application's own annotations can never write it.
const ReservedAnnotation = "reservation.cozystack.io/reserved"

// maxReservedAnnotationSize bounds ReservedAnnotation well inside the 256KiB
// the API server allows for all of an object's annotations together. A
// reservation is a hash and a handful of quantities; one this large means a
// declaration gone wrong, and it is refused rather than stored.
const maxReservedAnnotationSize = 16 << 10

// tenantNamespacePrefix is the prefix of every tenant namespace and of every
// Tenant HelmRelease, whatever the REST handler's own release prefix is.
const tenantNamespacePrefix = "tenant-"

const (
	defaultSettleTimeout = 5 * time.Second
	settleInterval       = 50 * time.Millisecond
)

// storedReservation is the value of ReservedAnnotation. ValuesHash ties the
// figure to the values it was computed from, so that a figure written before
// an edit made with the gate off is recognised as stale instead of trusted.
type storedReservation struct {
	ValuesHash string                   `json:"valuesHash"`
	Resources  reservation.ResourceList `json:"resources"`
}

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
	// pending holds, per pool root, the writes that were stored but that
	// the cache had not served when their writer gave the lock back.
	pending map[string]map[client.ObjectKey]pendingWrite
}

// pendingWrite is a stored state of a release the cache has not shown yet.
// visible recognises that state or a later one; deleted means the state is
// the release's absence, which only a delete issued here can claim, since a
// missing object otherwise means a create the cache has not seen.
type pendingWrite struct {
	visible func(*helmv2.HelmRelease) bool
	deleted bool
	since   time.Time
}

// pendingDebtLimit bounds how long a write the cache never shows can hold a
// pool. Past it the cache is stuck or the release was changed by a client
// other than this server, and holding the pool would only lock tenants out.
const pendingDebtLimit = 10 * time.Minute

// writtenState recognises, in the cache, the release as just written or as
// changed again since. The resourceVersion is compared for equality only. A
// status write by Flux right after it changes the resourceVersion and keeps
// the spec and annotations, so the figure or the values written are matched
// too.
func writtenState(hr *helmv2.HelmRelease) pendingWrite {
	rv := hr.ResourceVersion
	annotation := hr.Annotations[ReservedAnnotation]
	hash, _ := valuesHash(helmReleaseValues(hr))
	return pendingWrite{since: time.Now(), visible: func(cached *helmv2.HelmRelease) bool {
		if rv != "" && cached.ResourceVersion == rv {
			return true
		}
		if annotation != "" {
			return cached.Annotations[ReservedAnnotation] == annotation
		}
		h, err := valuesHash(helmReleaseValues(cached))
		return err == nil && h == hash
	}}
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
		pending:       map[string]map[client.ObjectKey]pendingWrite{},
	}
	for _, res := range resources {
		decl := res.Application.Consumption
		if decl == nil {
			continue
		}
		kind := res.Application.Kind
		schema, err := defaulting.BuildSchema(res.Application.OpenAPISchema)
		if err != nil {
			err = fmt.Errorf("the ApplicationDefinition of kind %s: schema: %w", kind, err)
			klog.Error(err)
			g.evaluators[kind] = kindEvaluator{err: err}
			continue
		}
		ev, err := reservation.Compile(decl, schema)
		if err != nil {
			err = fmt.Errorf("the ApplicationDefinition of kind %s: consumption: %w", kind, err)
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

type heldPoolKey struct{}

// withHeldPool marks root as locked by this request, so that a second
// acquire of the same pool further down the same request does not deadlock.
func withHeldPool(ctx context.Context, root string) context.Context {
	return context.WithValue(ctx, heldPoolKey{}, root)
}

// acquire serialises the gate per pool root within this process, from the
// check to the moment the informer cache shows the write, so that a client's
// sequential writes into one pool each see the previous one.
//
// A write whose writer gave the lock back before the cache showed it is
// still owed to the pool. Until the cache shows it, the pool's figure read
// from the cache is short by that write, so no growth is admitted and the
// caller is asked to retry. The returned unlock is nil when ctx already
// holds root.
func (g *Reservation) acquire(ctx context.Context, c client.Client, root string) (func(), error) {
	if held, _ := ctx.Value(heldPoolKey{}).(string); held == root {
		return nil, nil
	}
	g.mu.Lock()
	m, ok := g.locks[root]
	if !ok {
		m = &sync.Mutex{}
		g.locks[root] = m
	}
	g.mu.Unlock()
	m.Lock()

	g.mu.Lock()
	owed := make(map[client.ObjectKey]pendingWrite, len(g.pending[root]))
	for key, pw := range g.pending[root] {
		owed[key] = pw
	}
	g.mu.Unlock()
	for key, pw := range owed {
		if time.Since(pw.since) > pendingDebtLimit {
			klog.Warningf("HelmRelease %s was still not in the cache after %s; pool %q stops waiting for it", key, pendingDebtLimit, root)
		} else if err := g.waitVisible(c, key, pw); err != nil {
			m.Unlock()
			return nil, apierrors.NewTooManyRequests(fmt.Sprintf("an earlier write into tenant pool %q is not visible yet; retry", root), 1)
		}
		g.clear(root, key)
	}
	return m.Unlock, nil
}

func (g *Reservation) clear(root string, key client.ObjectKey) {
	g.mu.Lock()
	defer g.mu.Unlock()
	delete(g.pending[root], key)
	if len(g.pending[root]) == 0 {
		delete(g.pending, root)
	}
}

func (g *Reservation) owe(root string, key client.ObjectKey, pw pendingWrite) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.pending[root] == nil {
		g.pending[root] = map[client.ObjectKey]pendingWrite{}
	}
	g.pending[root][key] = pw
}

// supersede records that this server stored a later state of key, through
// a write or a delete that may not have taken the pool lock (a shrink, a
// Tenant losing its quota). A debt owed for key then waits for that state
// instead, which the cache may show even though it will never show the
// earlier one. The debt itself still clears only from the cache.
func (g *Reservation) supersede(key client.ObjectKey, pw pendingWrite) {
	if g == nil {
		return
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	for _, owed := range g.pending {
		if prev, ok := owed[key]; ok {
			pw.since = prev.since
			owed[key] = pw
		}
	}
}

// waitVisible polls the cache until it serves the write. It is detached from
// the request: a client that gives up must not shorten the wait the next
// writer depends on.
func (g *Reservation) waitVisible(c client.Client, key client.ObjectKey, pw pendingWrite) error {
	ctx, cancel := context.WithTimeout(context.Background(), g.settleTimeout)
	defer cancel()
	return wait.PollUntilContextCancel(ctx, settleInterval, true, func(ctx context.Context) (bool, error) {
		hr := &helmv2.HelmRelease{}
		if err := c.Get(ctx, key, hr); err != nil {
			if apierrors.IsNotFound(err) {
				return pw.deleted, nil
			}
			return false, err
		}
		return !pw.deleted && pw.visible(hr), nil
	})
}

// valuesHash hashes values in a canonical form: the apiserver may hand back
// a stored blob with its keys in another order than the one written.
// Numbers keep their literal, since through a float64 two integers above
// 2^53 would hash alike.
func valuesHash(raw []byte) (string, error) {
	var v any
	if len(raw) > 0 {
		dec := json.NewDecoder(bytes.NewReader(raw))
		dec.UseNumber()
		if err := dec.Decode(&v); err != nil {
			return "", err
		}
		if dec.More() {
			return "", fmt.Errorf("values carry more than one JSON document")
		}
	}
	canonical, err := json.Marshal(v)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(canonical)
	return hex.EncodeToString(sum[:]), nil
}

func helmReleaseValues(hr *helmv2.HelmRelease) []byte {
	if hr.Spec.Values == nil {
		return nil
	}
	return hr.Spec.Values.Raw
}

// stored reads ReservedAnnotation, or nil when it is absent or unreadable.
func stored(hr *helmv2.HelmRelease) *storedReservation {
	raw, ok := hr.Annotations[ReservedAnnotation]
	if !ok {
		return nil
	}
	var s storedReservation
	if err := json.Unmarshal([]byte(raw), &s); err != nil {
		klog.Warningf("ignoring unreadable %s on HelmRelease %s/%s: %v", ReservedAnnotation, hr.Namespace, hr.Name, err)
		return nil
	}
	return &s
}

// reservedBy returns what a stored application reserves.
//
// The figure the gate stored is used while it still matches the values. An
// application that no longer evaluates keeps the last figure it was admitted
// with, reported as stale, rather than dropping to zero and handing its share
// of the pool to the next writer. With no stored figure either, the error is
// returned and the application is left out of the sum.
func (g *Reservation) reservedBy(hr *helmv2.HelmRelease) (rl reservation.ResourceList, stale bool, err error) {
	prev := stored(hr)
	ev, err := g.evaluator(hr.Labels[ApplicationKindLabel], hr.Labels[ApplicationGroupLabel])
	if err != nil {
		// The kind's declaration no longer compiles. Its new writes are
		// refused, but what it was admitted with stays charged.
		if prev != nil {
			return prev.Resources, true, nil
		}
		return nil, false, err
	}
	if ev == nil {
		return nil, false, nil
	}
	values := helmReleaseValues(hr)
	if prev != nil {
		if hash, hashErr := valuesHash(values); hashErr == nil && hash == prev.ValuesHash {
			return prev.Resources, false, nil
		}
	}
	rl, err = ev.EvaluateJSON(values, g.env)
	if err == nil {
		return rl, false, nil
	}
	if prev != nil {
		return prev.Resources, true, nil
	}
	return nil, false, err
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

// budgetKey maps a resourceQuotas key onto the reservation vocabulary. An
// operator may write requests.storage where a declaration reserves storage;
// both name the same budget.
func budgetKey(k string) string {
	if rest, ok := strings.CutPrefix(k, "requests."); ok {
		return rest
	}
	if rest, ok := strings.CutPrefix(k, "limits."); ok {
		return rest
	}
	return k
}

func budgetOf(declared map[string]resource.Quantity) reservation.ResourceList {
	out := reservation.ResourceList{}
	for k, v := range declared {
		key := budgetKey(k)
		if cur, ok := out[key]; ok && cur.Cmp(v) <= 0 {
			continue
		}
		out[key] = v.DeepCopy()
	}
	return out
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
	p := &pool{root: root, budget: budgetOf(tree[root]), carved: reservation.ResourceList{}}
	for owned, declared := range tree {
		if rootOf(owned) == root {
			p.members = append(p.members, owned)
		}
		if owned != root && len(declared) > 0 && rootOf(tenantParentNamespace(owned)) == root {
			p.carved.Add(budgetOf(declared))
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

// reservationAdmission is the outcome of the gate for one write: the figure
// to store on the release, the pool lock to give back once the write is
// visible, and the stored values the decision was made against.
type reservationAdmission struct {
	gate     *Reservation
	root     string
	stored   *storedReservation
	unlock   func()
	baseHash string
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
	if a == nil {
		return nil
	}
	if a.stored == nil {
		return nil
	}
	raw, err := json.Marshal(a.stored)
	if err != nil {
		return err
	}
	if len(raw) > maxReservedAnnotationSize {
		return fmt.Errorf("the reservation takes %d bytes, more than the %d an annotation is allowed here", len(raw), maxReservedAnnotationSize)
	}
	if hr.Annotations == nil {
		hr.Annotations = map[string]string{}
	}
	hr.Annotations[ReservedAnnotation] = string(raw)
	return nil
}

// stale reports whether the stored release changed since the decision was
// made, so that a conflict retry carrying the admitted figure onto a
// different object would skip the check the change calls for.
func (a *reservationAdmission) stale(cur *helmv2.HelmRelease) bool {
	if a == nil || a.baseHash == "" {
		return false
	}
	h, err := valuesHash(helmReleaseValues(cur))
	return err != nil || h != a.baseHash
}

// base records the stored release a decision taken before it was read was
// made against, for stale to compare a conflict retry with.
func (a *reservationAdmission) base(cur *helmv2.HelmRelease) error {
	if a == nil {
		return nil
	}
	h, err := valuesHash(helmReleaseValues(cur))
	if err != nil {
		return err
	}
	a.baseHash = h
	return nil
}

// finalUpdateError carries an error out of a conflict retry loop without
// being retried as one: it deliberately does not unwrap to its APIStatus.
type finalUpdateError struct{ err error }

func (e finalUpdateError) Error() string { return e.err.Error() }

// settle waits until the cache serves the release as written, so that the
// next writer into the pool, which reads the cache, counts it. If it does
// not in time, the write is recorded as owed to the pool, and the pool
// admits no growth until the cache shows it or a later state of it.
func (a *reservationAdmission) settle(c client.Client, hr *helmv2.HelmRelease) {
	if a == nil || a.unlock == nil {
		return
	}
	key := client.ObjectKeyFromObject(hr)
	pw := writtenState(hr)
	if err := a.gate.waitVisible(c, key, pw); err != nil {
		klog.Warningf("HelmRelease %s is not in the cache after %s; pool %q admits no growth until it is: %v", key, a.gate.settleTimeout, a.root, err)
		a.gate.owe(a.root, key, pw)
	}
}

// lockTenantPool holds the pool a Tenant's quota is carved out of, from the
// carve-out check to the moment the cache shows the Tenant, so that an
// application admitted into the same pool and the carve-out cannot both
// count the same headroom. It returns a nil admission when the gate is off
// or the Tenant declares no quota, and ctx marked with the held pool.
func (r *REST) lockTenantPool(ctx context.Context, app *appsv1alpha1.Application) (context.Context, *reservationAdmission, error) {
	if r.reservation == nil || r.kindName != validation.TenantKind {
		return ctx, nil, nil
	}
	var raw []byte
	if app.Spec != nil {
		raw = app.Spec.Raw
	}
	quotas, err := parseDeclaredQuotas(raw)
	if err != nil || len(quotas) == 0 {
		return ctx, nil, nil
	}
	tree, err := r.tenantTree(ctx)
	if err != nil {
		return ctx, nil, err
	}
	p := poolOf(tree, app.Namespace)
	if p == nil {
		return ctx, nil, nil
	}
	unlock, err := r.reservation.acquire(ctx, r.c, p.root)
	if err != nil {
		return ctx, nil, err
	}
	return withHeldPool(ctx, p.root), &reservationAdmission{gate: r.reservation, root: p.root, unlock: unlock}, nil
}

// admitReservation is the reservation gate for a write of app, whose stored
// release is old (nil on Create). It returns a nil reservationAdmission when the gate
// is off or the kind declares nothing. Otherwise the caller holds the pool
// lock until it calls release, and must annotate the release it writes and
// settle after writing.
//
// Only the delta counts: an edit that reserves nothing more is accepted even
// in a pool that is already over budget, and shrinking is always accepted.
func (r *REST) admitReservation(ctx context.Context, app *appsv1alpha1.Application, old *helmv2.HelmRelease) (*reservationAdmission, error) {
	adm, errs, err := r.admit(ctx, app, old)
	if err != nil {
		return nil, err
	}
	if len(errs) > 0 {
		return nil, apierrors.NewInvalid(r.gvk.GroupKind(), app.Name, errs)
	}
	return adm, nil
}

func (r *REST) admit(ctx context.Context, app *appsv1alpha1.Application, old *helmv2.HelmRelease) (*reservationAdmission, field.ErrorList, error) {
	g := r.reservation
	if g == nil {
		return nil, nil, nil
	}
	specPath := field.NewPath("spec")
	ev, err := g.evaluator(r.kindName, r.gvk.Group)
	if err != nil {
		return nil, field.ErrorList{field.Forbidden(specPath, fmt.Sprintf("%s cannot be admitted: %v", r.kindName, err))}, nil
	}
	if ev == nil {
		return nil, nil, nil
	}

	var raw []byte
	if app.Spec != nil {
		raw = app.Spec.Raw
	}
	want, err := ev.EvaluateJSON(raw, g.env)
	if err != nil {
		return nil, field.ErrorList{field.Invalid(specPath, "", fmt.Sprintf("cannot compute what this %s reserves from its tenant's quota: %v", r.kindName, err))}, nil
	}
	hash, err := valuesHash(raw)
	if err != nil {
		return nil, field.ErrorList{field.Invalid(specPath, "", err.Error())}, nil
	}
	adm := &reservationAdmission{gate: g, stored: &storedReservation{ValuesHash: hash, Resources: want}}
	if old != nil {
		if adm.baseHash, err = valuesHash(helmReleaseValues(old)); err != nil {
			return nil, field.ErrorList{field.Invalid(specPath, "", err.Error())}, nil
		}
	}

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
		return adm, nil, nil
	}

	tree, err := r.tenantTree(ctx)
	if err != nil {
		return nil, field.ErrorList{field.InternalError(specPath, err)}, nil
	}
	p := poolOf(tree, app.Namespace)
	if p == nil {
		return adm, nil, nil
	}
	adm.root = p.root
	if adm.unlock, err = g.acquire(ctx, r.c, p.root); err != nil {
		return nil, nil, err
	}
	// The pool read above only chose the lock. Budget, carve-outs and
	// members are read again under it, since a Tenant write holding it may
	// have just changed them.
	if tree, err = r.tenantTree(ctx); err != nil {
		adm.release()
		return nil, field.ErrorList{field.InternalError(specPath, err)}, nil
	}
	if p = poolOf(tree, app.Namespace); p == nil || p.root != adm.root {
		adm.release()
		return nil, nil, apierrors.NewConflict(r.gvr.GroupResource(), app.Name,
			fmt.Errorf("the tenant pool of namespace %s changed while this write was checked; retry it", app.Namespace))
	}

	// The object being updated stays in the figure at its old reservation,
	// so that what is checked against the budget is the delta.
	fig, err := r.reservedIn(ctx, p.members)
	if err != nil {
		adm.release()
		return nil, field.ErrorList{field.InternalError(specPath, err)}, nil
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
		return nil, errs, nil
	}
	return adm, nil, nil
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
