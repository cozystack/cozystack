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
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"

	helmv2 "github.com/fluxcd/helm-controller/api/v2"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/selection"
	"k8s.io/apimachinery/pkg/util/validation/field"
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
	evaluators map[string]kindEvaluator
	env        reservation.Environment

	mu    sync.Mutex
	locks map[string]*sync.Mutex
}

// NewReservation compiles the consumption declaration of every resource. A
// declaration that does not compile is kept as an error, so that its kind is
// refused rather than counted as free.
func NewReservation(resources []config.Resource, env reservation.Environment) *Reservation {
	g := &Reservation{
		evaluators: map[string]kindEvaluator{},
		env:        env,
		locks:      map[string]*sync.Mutex{},
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

// withHeldPool marks roots as locked by this request, so that a second
// acquire of one of them further down the same request does not deadlock.
func withHeldPool(ctx context.Context, roots ...string) context.Context {
	held := map[string]bool{}
	if prev, ok := ctx.Value(heldPoolKey{}).(map[string]bool); ok {
		for k := range prev {
			held[k] = true
		}
	}
	for _, root := range roots {
		held[root] = true
	}
	return context.WithValue(ctx, heldPoolKey{}, held)
}

// acquire serialises the gate per pool root within this process, from the
// pool's read to the write, so that two writes into one pool cannot both
// count the same headroom. The pool is read from the API server once the
// lock is held, so every write committed before it is counted, whatever the
// informer cache has caught up with. The returned unlock is nil when ctx
// already holds root.
func (g *Reservation) acquire(ctx context.Context, root string) func() {
	if held, _ := ctx.Value(heldPoolKey{}).(map[string]bool); held[root] {
		return nil
	}
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

// snapshot resolves each instance type and preset at most once, so that
// one admission evaluates the release it replaces and every member of its
// pool against the same definitions.
type snapshot struct {
	env           reservation.Environment
	instancetypes map[string]resolved
	presets       map[string]resolved
}

type resolved struct {
	list reservation.ResourceList
	err  error
}

func newSnapshot(env reservation.Environment) *snapshot {
	return &snapshot{env: env, instancetypes: map[string]resolved{}, presets: map[string]resolved{}}
}

func (s *snapshot) Instancetype(name string) (reservation.ResourceList, error) {
	if r, ok := s.instancetypes[name]; ok {
		return r.list, r.err
	}
	rl, err := s.env.Instancetype(name)
	s.instancetypes[name] = resolved{rl, err}
	return rl, err
}

func (s *snapshot) Preset(name string) (reservation.ResourceList, error) {
	if r, ok := s.presets[name]; ok {
		return r.list, r.err
	}
	rl, err := s.env.Preset(name)
	s.presets[name] = resolved{rl, err}
	return rl, err
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
		// More reports false before a stray ] or }, so the end of the input
		// is checked for directly. Trailing whitespace is not a token.
		if _, err := dec.Token(); err != io.EOF {
			return "", fmt.Errorf("values carry data after their JSON document")
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
func (g *Reservation) reservedBy(hr *helmv2.HelmRelease, env reservation.Environment) (rl reservation.ResourceList, stale bool, err error) {
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
	rl, err = ev.EvaluateJSON(values, env)
	if err == nil {
		return rl, false, nil
	}
	// An environment that could not be read says nothing about the values:
	// what they reserve now is unknown, and the figure stored for other
	// values may be smaller.
	if prev != nil && !errors.Is(err, reservation.ErrUnavailable) {
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
// namespace it owns, through reader.
func (r *REST) tenantTree(ctx context.Context, reader client.Reader) (map[string]map[string]resource.Quantity, error) {
	list := &helmv2.HelmReleaseList{}
	selector := labels.SelectorFromSet(labels.Set{
		ApplicationKindLabel:  validation.TenantKind,
		ApplicationGroupLabel: appsv1alpha1.GroupName,
	})
	if err := reader.List(ctx, list, &client.ListOptions{LabelSelector: selector}); err != nil {
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

// reservedIn sums the reservations of the applications in namespaces, read
// from the API server, leaving out the release named by exclude. An application that cannot be
// evaluated is logged and left out; it must not block an unrelated write.
// An environment that cannot be read is different: the application is fine
// and its share unknown, so the sum is refused rather than short.
func (r *REST) reservedIn(ctx context.Context, namespaces []string, exclude client.ObjectKey, env reservation.Environment) (poolFigure, error) {
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
		if err := r.w.List(ctx, list, &client.ListOptions{Namespace: ns, LabelSelector: selector}); err != nil {
			return fig, err
		}
		for i := range list.Items {
			hr := &list.Items[i]
			if hr.Namespace == exclude.Namespace && hr.Name == exclude.Name {
				continue
			}
			rl, stale, err := r.reservation.reservedBy(hr, env)
			if errors.Is(err, reservation.ErrUnavailable) {
				return fig, err
			}
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
// to store on the release, the pool lock to give back once it is written,
// and the stored release the decision was made against.
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

// baseOf identifies what a decision subtracted for a stored release: its
// values and the figure stored on it. The figure is part of it because a
// release can be admitted again with the same values and another figure, an
// instance type redefined in between, and the delta taken from the old one
// would then be wrong.
func baseOf(hr *helmv2.HelmRelease) (string, error) {
	h, err := valuesHash(helmReleaseValues(hr))
	if err != nil {
		return "", err
	}
	return h + "\x00" + hr.Annotations[ReservedAnnotation], nil
}

// stale reports whether the stored release changed since the decision was
// made, so that a conflict retry carrying the admitted figure onto a
// different object would skip the check the change calls for.
func (a *reservationAdmission) stale(cur *helmv2.HelmRelease) bool {
	if a == nil || a.baseHash == "" {
		return false
	}
	b, err := baseOf(cur)
	return err != nil || b != a.baseHash
}

// base records the stored release a decision taken before it was read was
// made against, for stale to compare a conflict retry with.
func (a *reservationAdmission) base(cur *helmv2.HelmRelease) error {
	if a == nil {
		return nil
	}
	b, err := baseOf(cur)
	if err != nil {
		return err
	}
	a.baseHash = b
	return nil
}

// finalUpdateError carries an error out of a conflict retry loop without
// being retried as one: it deliberately does not unwrap to its APIStatus.
type finalUpdateError struct{ err error }

func (e finalUpdateError) Error() string { return e.err.Error() }

// lockTenantPool holds, from the quota check to the Tenant's write, the
// pool the Tenant's quota is carved out of and, when the Tenant had a quota
// of its own, the pool that quota bounded. The first keeps a carve-out and
// an order into the same pool from both counting the same headroom. The
// second keeps a Tenant from dropping its quota, which merges its namespaces
// into the parent's pool, while an order admitted against its own pool has
// not been written yet. It returns a nil admission when the gate is off or
// neither the new nor the old Tenant declares a quota, and ctx marked with
// the pools held.
func (r *REST) lockTenantPool(ctx context.Context, app *appsv1alpha1.Application, old *appsv1alpha1.Application) (context.Context, *reservationAdmission, error) {
	if r.reservation == nil || r.kindName != validation.TenantKind {
		return ctx, nil, nil
	}
	declares := func(a *appsv1alpha1.Application) bool {
		if a == nil || a.Spec == nil {
			return false
		}
		quotas, err := parseDeclaredQuotas(a.Spec.Raw)
		return err == nil && len(quotas) > 0
	}
	hadQuota := declares(old)
	if !declares(app) && !hadQuota {
		return ctx, nil, nil
	}
	parentRoot, err := r.poolRootFor(ctx, app.Namespace)
	if err != nil {
		return ctx, nil, err
	}
	var roots []string
	if parentRoot != "" {
		roots = append(roots, parentRoot)
	}
	if hadQuota {
		roots = append(roots, r.computeTenantNamespace(app.Namespace, app.Name))
	}
	if len(roots) == 0 {
		return ctx, nil, nil
	}
	// A child's root always extends its parent's, so taking the parent's
	// first is the same order every request takes them in.
	var unlocks []func()
	for _, root := range roots {
		if u := r.reservation.acquire(ctx, root); u != nil {
			unlocks = append(unlocks, u)
		}
	}
	adm := &reservationAdmission{gate: r.reservation, root: parentRoot, unlock: func() {
		for i := len(unlocks) - 1; i >= 0; i-- {
			unlocks[i]()
		}
	}}
	// The parent's root was chosen before the lock was held; an ancestor
	// that became bounded meanwhile would put this Tenant under another lock
	// than the applications of its pool.
	tree, err := r.tenantTree(ctx, r.w)
	if err != nil {
		adm.release()
		return ctx, nil, err
	}
	if p := poolOf(tree, app.Namespace); (p == nil) != (parentRoot == "") || p != nil && p.root != parentRoot {
		adm.release()
		return ctx, nil, apierrors.NewConflict(r.gvr.GroupResource(), app.Name,
			fmt.Errorf("the tenant pool of namespace %s changed while this write was checked; retry it", app.Namespace))
	}
	return withHeldPool(ctx, roots...), adm, nil
}

// poolRootFor chooses the pool root that governs ns, from the cache when it
// knows one and from the API server when it does not, so that a budget the
// cache has not seen yet cannot leave a write ungated.
func (r *REST) poolRootFor(ctx context.Context, ns string) (string, error) {
	for _, reader := range []client.Reader{r.c, r.w} {
		tree, err := r.tenantTree(ctx, reader)
		if err != nil {
			return "", err
		}
		if p := poolOf(tree, ns); p != nil {
			return p.root, nil
		}
	}
	return "", nil
}

// checkTenantCarveOut is the Tenant quota check with the gate on: a bounded
// Tenant's quota has to fit what is left of the pool it is carved out of,
// whether its immediate parent is bounded or the pool belongs to a further
// ancestor. What is left is the pool's budget less its other carve-outs and
// less what its applications reserve, all read from the API server under the
// pool lock, with quota keys compared as the budget names them. The Tenant's
// own subtree is left out: on an update it is what is being resized.
func (r *REST) checkTenantCarveOut(ctx context.Context, app *appsv1alpha1.Application) error {
	fldPath := field.NewPath("spec").Child(resourceQuotasField)
	var raw []byte
	if app.Spec != nil {
		raw = app.Spec.Raw
	}
	declared, err := parseDeclaredQuotas(raw)
	if err != nil {
		return apierrors.NewInvalid(r.gvk.GroupKind(), app.Name, field.ErrorList{field.Invalid(fldPath, "", err.Error())})
	}
	if len(declared) == 0 {
		return nil
	}
	tree, err := r.tenantTree(ctx, r.w)
	if err != nil {
		return err
	}
	own := r.computeTenantNamespace(app.Namespace, app.Name)
	for ns := range tree {
		if ns == own || strings.HasPrefix(ns, own+"-") {
			delete(tree, ns)
		}
	}
	p := poolOf(tree, app.Namespace)
	if p == nil {
		return nil
	}
	fig, err := r.reservedIn(ctx, p.members, client.ObjectKey{}, newSnapshot(r.reservation.env))
	if errors.Is(err, reservation.ErrUnavailable) {
		return apierrors.NewServiceUnavailable(fmt.Sprintf("cannot sum tenant pool %q yet: %v", p.root, err))
	}
	if err != nil {
		return err
	}
	want := budgetOf(declared)
	var errs field.ErrorList
	for _, k := range want.Keys() {
		limit, bounded := p.budget[k]
		if !bounded {
			continue
		}
		requested := want[k]
		carved, reserved := p.carved[k], fig.reserved[k]
		remaining := limit.DeepCopy()
		remaining.Sub(carved)
		remaining.Sub(reserved)
		if requested.Cmp(remaining) <= 0 {
			continue
		}
		errs = append(errs, field.Forbidden(fldPath.Key(k), fmt.Sprintf(
			"requested %s exceeds the remaining %q budget of tenant pool %q: %s allowed, %s carved out by other sub-tenants, %s reserved by %d applications; a tenant may not be granted more quota than its pool has left",
			requested.String(), k, p.root, limit.String(), carved.String(), reserved.String(), fig.applications)))
	}
	if len(errs) > 0 {
		return apierrors.NewInvalid(r.gvk.GroupKind(), app.Name, errs)
	}
	return nil
}

// checkTenantQuota runs the Tenant quota check that applies: against the
// pool under the reservation gate, against the parent's declared quota and
// ResourceQuota usage without it.
func (r *REST) checkTenantQuota(ctx context.Context, app *appsv1alpha1.Application) error {
	if r.reservation != nil {
		return r.checkTenantCarveOut(ctx, app)
	}
	if errs := r.validateTenantResourceQuotas(ctx, app); len(errs) > 0 {
		return apierrors.NewInvalid(r.gvk.GroupKind(), app.Name, errs)
	}
	return nil
}

// admitReservation is the reservation gate for a write of app, whose stored
// release is old (nil on Create). It returns a nil reservationAdmission when
// the gate is off or the kind declares nothing. Otherwise the caller holds
// the pool lock until it calls release, after its write, and must annotate
// the release it writes.
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
	env := newSnapshot(g.env)
	want, err := ev.EvaluateJSON(raw, env)
	if errors.Is(err, reservation.ErrUnavailable) {
		return nil, nil, apierrors.NewServiceUnavailable(fmt.Sprintf("cannot compute what this %s reserves from its tenant's quota yet: %v", r.kindName, err))
	}
	if err != nil {
		return nil, field.ErrorList{field.Invalid(specPath, "", fmt.Sprintf("cannot compute what this %s reserves from its tenant's quota: %v", r.kindName, err))}, nil
	}
	hash, err := valuesHash(raw)
	if err != nil {
		return nil, field.ErrorList{field.Invalid(specPath, "", err.Error())}, nil
	}
	adm := &reservationAdmission{gate: g, stored: &storedReservation{ValuesHash: hash, Resources: want}}
	if old != nil {
		if adm.baseHash, err = baseOf(old); err != nil {
			return nil, field.ErrorList{field.Invalid(specPath, "", err.Error())}, nil
		}
	}

	delta := want.DeepCopy()
	if old != nil {
		prev, _, err := g.reservedBy(old, env)
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

	root, err := r.poolRootFor(ctx, app.Namespace)
	if err != nil {
		return nil, field.ErrorList{field.InternalError(specPath, err)}, nil
	}
	if root == "" {
		return adm, nil, nil
	}
	adm.root = root
	adm.unlock = g.acquire(ctx, root)
	// The cached read above only chose the lock. Budget, carve-outs and
	// members are read from the API server under it.
	tree, err := r.tenantTree(ctx, r.w)
	if err != nil {
		adm.release()
		return nil, field.ErrorList{field.InternalError(specPath, err)}, nil
	}
	p := poolOf(tree, app.Namespace)
	if p == nil || p.root != adm.root {
		adm.release()
		return nil, nil, apierrors.NewConflict(r.gvr.GroupResource(), app.Name,
			fmt.Errorf("the tenant pool of namespace %s changed while this write was checked; retry it", app.Namespace))
	}

	// The object written is left out of the pool's figure and checked at
	// its full new reservation, so that the figure does not depend on which
	// copy of it the delta was taken from.
	self := client.ObjectKey{Namespace: app.Namespace, Name: r.releaseConfig.Prefix + app.Name}
	fig, err := r.reservedIn(ctx, p.members, self, env)
	if errors.Is(err, reservation.ErrUnavailable) {
		adm.release()
		return nil, nil, apierrors.NewServiceUnavailable(fmt.Sprintf("cannot sum tenant pool %q yet: %v", p.root, err))
	}
	if err != nil {
		adm.release()
		return nil, field.ErrorList{field.InternalError(specPath, err)}, nil
	}

	var errs field.ErrorList
	for _, k := range delta.Keys() {
		limit, bounded := p.budget[k]
		if growth := delta[k]; !bounded || growth.Sign() <= 0 {
			continue
		}
		requested := want[k]
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
