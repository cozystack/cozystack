// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Cozystack Authors.

// SecurityGroup registry – namespaced projection over CiliumNetworkPolicy
// objects labelled "sdn.cozystack.io/securitygroup=true". The marker label is
// hidden from the SecurityGroup view.

package securitygroup

import (
	"context"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metainternal "k8s.io/apimachinery/pkg/apis/meta/internalversion"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/selection"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/duration"
	"k8s.io/apimachinery/pkg/watch"
	"k8s.io/apiserver/pkg/endpoints/request"
	"k8s.io/apiserver/pkg/registry/rest"
	"sigs.k8s.io/controller-runtime/pkg/client"

	sdnv1alpha1 "github.com/cozystack/cozystack/pkg/apis/sdn/v1alpha1"
	"github.com/cozystack/cozystack/pkg/registry"
	fieldfilter "github.com/cozystack/cozystack/pkg/registry/fields"
	"github.com/cozystack/cozystack/pkg/registry/sorting"
)

// -----------------------------------------------------------------------------
// Constants & helpers
// -----------------------------------------------------------------------------

const (
	// sgLabelKey marks the CiliumNetworkPolicy objects owned by the SecurityGroup
	// API. Only marked policies are visible through this storage; unmarked
	// policies (e.g. platform tenant-isolation policies) are left untouched.
	sgLabelKey   = "sdn.cozystack.io/securitygroup"
	sgLabelValue = "true"

	singularName = sdnv1alpha1.SecurityGroupSingularName
	kindSG       = sdnv1alpha1.SecurityGroupKind
	kindSGList   = sdnv1alpha1.SecurityGroupListKind
)

// stripInternal returns a copy of m without the SecurityGroup marker label.
func stripInternal(m map[string]string) map[string]string {
	if m == nil {
		return nil
	}
	out := make(map[string]string, len(m))
	for k, v := range m {
		if k == sgLabelKey {
			continue
		}
		out[k] = v
	}
	return out
}

func policyToSecurityGroup(np *CiliumNetworkPolicy) *sdnv1alpha1.SecurityGroup {
	sg := &sdnv1alpha1.SecurityGroup{
		TypeMeta: metav1.TypeMeta{
			APIVersion: sdnv1alpha1.SchemeGroupVersion.String(),
			Kind:       kindSG,
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:              np.Name,
			Namespace:         np.Namespace,
			UID:               np.UID,
			ResourceVersion:   np.ResourceVersion,
			CreationTimestamp: np.CreationTimestamp,
			Labels:            stripInternal(np.Labels),
			Annotations:       np.Annotations,
			OwnerReferences:   np.OwnerReferences,
			Finalizers:        np.Finalizers,
		},
	}
	if np.Spec != nil {
		sg.Spec = *np.Spec.DeepCopy()
	}
	return sg
}

func securityGroupToPolicy(sg *sdnv1alpha1.SecurityGroup, cur *CiliumNetworkPolicy) *CiliumNetworkPolicy {
	var out CiliumNetworkPolicy
	if cur != nil {
		// Preserve system metadata the SecurityGroup view does not model
		// (resourceVersion, uid, managedFields, …); user-facing labels,
		// annotations, ownerReferences, finalizers and spec are replaced
		// wholesale below.
		out = *cur.DeepCopy()
	}
	out.TypeMeta = metav1.TypeMeta{APIVersion: cnpAPIVersionFull, Kind: cnpKind}
	out.Name, out.Namespace = sg.Name, sg.Namespace
	// Carry the client-supplied resourceVersion so optimistic concurrency is
	// preserved; the update path falls back to the current one only when empty.
	out.ResourceVersion = sg.ResourceVersion

	// Replace labels/annotations with exactly what the request carries, matching
	// Kubernetes PUT semantics — a label or annotation the caller drops must
	// disappear, not linger from the previous object.
	out.Labels = make(map[string]string, len(sg.Labels)+1)
	for k, v := range sg.Labels {
		out.Labels[k] = v
	}
	// The marker label is owned by the storage and must always win, so it is set
	// last. Otherwise a tenant could submit spec labels that overwrite it and
	// orphan an enforced policy — created and applied by Cilium, but invisible
	// to (and so uncleanable through) the SecurityGroup API.
	out.Labels[sgLabelKey] = sgLabelValue

	if len(sg.Annotations) > 0 {
		out.Annotations = make(map[string]string, len(sg.Annotations))
		for k, v := range sg.Annotations {
			out.Annotations[k] = v
		}
	} else {
		out.Annotations = nil
	}

	// Project ownerReferences and finalizers 1:1 so Kubernetes garbage
	// collection and finalizers work on SecurityGroup objects. Like labels and
	// annotations these follow replace semantics — they reflect the request.
	out.OwnerReferences = sg.DeepCopy().OwnerReferences
	out.Finalizers = append([]string(nil), sg.Finalizers...)

	out.Spec = sg.Spec.DeepCopy()
	// Normalize the protocol to upper case: validation accepts it case
	// insensitively, but the backing CiliumNetworkPolicy CRD enforces a strict
	// upper-case enum, so a raw "tcp" would be rejected on the backing write.
	normalizePortProtocols(out.Spec)
	return &out
}

// normalizePortProtocols upper-cases every port protocol in place so the value
// written to the backing CiliumNetworkPolicy matches its case-sensitive enum.
func normalizePortProtocols(spec *sdnv1alpha1.SecurityGroupSpec) {
	if spec == nil {
		return
	}
	norm := func(rules []sdnv1alpha1.PortRule) {
		for i := range rules {
			for j := range rules[i].Ports {
				if p := &rules[i].Ports[j]; p.Protocol != "" {
					p.Protocol = strings.ToUpper(p.Protocol)
				}
			}
		}
	}
	for i := range spec.Ingress {
		norm(spec.Ingress[i].ToPorts)
	}
	for i := range spec.Egress {
		norm(spec.Egress[i].ToPorts)
	}
}

func nsFrom(ctx context.Context) (string, error) {
	ns, ok := request.NamespaceFrom(ctx)
	if !ok {
		return "", apierrors.NewBadRequest("namespace required")
	}
	return ns, nil
}

func isSecurityGroup(np *CiliumNetworkPolicy) bool {
	return np.Labels != nil && np.Labels[sgLabelKey] == sgLabelValue
}

// createOptionsFromUpdate carries the caller's write intent (dry-run, field
// manager, field validation) from an update request into the create it triggers
// on the force-create path, so a dry-run apply cannot become a real write and
// field-manager attribution is preserved.
func createOptionsFromUpdate(opts *metav1.UpdateOptions) *metav1.CreateOptions {
	if opts == nil {
		return &metav1.CreateOptions{}
	}
	return &metav1.CreateOptions{
		DryRun:          opts.DryRun,
		FieldManager:    opts.FieldManager,
		FieldValidation: opts.FieldValidation,
	}
}

// -----------------------------------------------------------------------------
// REST storage
// -----------------------------------------------------------------------------

var (
	_ rest.Creater              = &REST{}
	_ rest.Getter               = &REST{}
	_ rest.Lister               = &REST{}
	_ rest.Updater              = &REST{}
	_ rest.Patcher              = &REST{}
	_ rest.GracefulDeleter      = &REST{}
	_ rest.Watcher              = &REST{}
	_ rest.TableConvertor       = &REST{}
	_ rest.Scoper               = &REST{}
	_ rest.SingularNameProvider = &REST{}
)

// REST is the storage backend translating SecurityGroup to CiliumNetworkPolicy.
type REST struct {
	c   client.Client
	w   client.WithWatch
	gvr schema.GroupVersionResource
}

// NewREST returns a SecurityGroup REST storage backed by the given clients.
func NewREST(c client.Client, w client.WithWatch) *REST {
	return &REST{
		c: c,
		w: w,
		gvr: schema.GroupVersionResource{
			Group:    sdnv1alpha1.GroupName,
			Version:  "v1alpha1",
			Resource: sdnv1alpha1.SecurityGroupPluralName,
		},
	}
}

// -----------------------------------------------------------------------------
// Basic meta
// -----------------------------------------------------------------------------

// NamespaceScoped reports that SecurityGroup is a namespaced resource.
func (*REST) NamespaceScoped() bool { return true }

// New returns an empty SecurityGroup.
func (*REST) New() runtime.Object { return &sdnv1alpha1.SecurityGroup{} }

// NewList returns an empty SecurityGroupList.
func (*REST) NewList() runtime.Object { return &sdnv1alpha1.SecurityGroupList{} }

// Kind returns the resource kind.
func (*REST) Kind() string { return kindSG }

// GroupVersionKind returns the GVK served by this storage.
func (r *REST) GroupVersionKind(_ schema.GroupVersion) schema.GroupVersionKind {
	return r.gvr.GroupVersion().WithKind(kindSG)
}

// GetSingularName returns the singular resource name.
func (*REST) GetSingularName() string { return singularName }

// buildSelector merges the required marker label with any user-provided
// requirements from opts.LabelSelector. Returns (selector, true) on success;
// (nil, false) when the user selector matches nothing.
func buildSelector(opts *metainternal.ListOptions) (labels.Selector, bool) {
	ls := labels.NewSelector()
	req, _ := labels.NewRequirement(sgLabelKey, selection.Equals, []string{sgLabelValue})
	ls = ls.Add(*req)
	if opts.LabelSelector != nil {
		reqs, selectable := opts.LabelSelector.Requirements()
		if !selectable {
			return nil, false
		}
		if len(reqs) > 0 {
			ls = ls.Add(reqs...)
		}
	}
	return ls, true
}

// -----------------------------------------------------------------------------
// CRUD
// -----------------------------------------------------------------------------

// Create translates a SecurityGroup into a CiliumNetworkPolicy and creates it.
func (r *REST) Create(
	ctx context.Context,
	obj runtime.Object,
	createValidation rest.ValidateObjectFunc,
	opts *metav1.CreateOptions,
) (runtime.Object, error) {
	in, ok := obj.(*sdnv1alpha1.SecurityGroup)
	if !ok {
		return nil, fmt.Errorf("expected SecurityGroup, got %T", obj)
	}

	// Bind the object to the request namespace. The aggregated apiserver already
	// rejects a conflicting body namespace before reaching the storage, but
	// enforcing it here keeps the storage self-defending against a cross-namespace
	// write regardless of the calling path.
	ns, err := nsFrom(ctx)
	if err != nil {
		return nil, err
	}
	if in.Namespace != "" && in.Namespace != ns {
		return nil, apierrors.NewBadRequest("metadata.namespace must match request namespace")
	}
	in = in.DeepCopy()
	in.Namespace = ns

	if err := validateSecurityGroup(in); err != nil {
		return nil, err
	}

	// Run the admission chain (validating webhooks + ValidatingAdmissionPolicies)
	// before persisting. Custom REST handlers must invoke this explicitly —
	// unlike genericregistry.Store, which wires it automatically.
	if createValidation != nil {
		if err := createValidation(ctx, in); err != nil {
			return nil, err
		}
	}

	np := securityGroupToPolicy(in, nil)
	if err := r.c.Create(ctx, np, &client.CreateOptions{Raw: opts}); err != nil {
		return nil, err
	}
	return policyToSecurityGroup(np), nil
}

// Get returns the SecurityGroup with the given name.
func (r *REST) Get(
	ctx context.Context,
	name string,
	opts *metav1.GetOptions,
) (runtime.Object, error) {
	ns, err := nsFrom(ctx)
	if err != nil {
		return nil, err
	}
	np := &CiliumNetworkPolicy{}
	if err := r.c.Get(ctx, types.NamespacedName{Namespace: ns, Name: name}, np, &client.GetOptions{Raw: opts}); err != nil {
		return nil, err
	}
	if !isSecurityGroup(np) {
		return nil, apierrors.NewNotFound(r.gvr.GroupResource(), name)
	}
	return policyToSecurityGroup(np), nil
}

// List returns all SecurityGroups in the request namespace.
func (r *REST) List(ctx context.Context, opts *metainternal.ListOptions) (runtime.Object, error) {
	// A cluster-wide list (kubectl get securitygroups -A) carries an empty
	// namespace in the context; NamespaceValue returns "" for it and the client
	// then lists across all namespaces, so this must not require a namespace.
	ns := request.NamespaceValue(ctx)

	ls, selectable := buildSelector(opts)

	emptyList := func() *sdnv1alpha1.SecurityGroupList {
		return &sdnv1alpha1.SecurityGroupList{
			TypeMeta: metav1.TypeMeta{
				APIVersion: sdnv1alpha1.SchemeGroupVersion.String(),
				Kind:       kindSGList,
			},
		}
	}

	if !selectable {
		return emptyList(), nil
	}

	// controller-runtime cache doesn't support field selectors, so parse and
	// apply metadata.name / metadata.namespace filters manually.
	// See: https://github.com/kubernetes-sigs/controller-runtime/issues/612
	fieldFilter, err := fieldfilter.ParseFieldSelector(opts.FieldSelector)
	if err != nil {
		return nil, err
	}
	if fieldFilter.Namespace != "" && ns != "" && ns != fieldFilter.Namespace {
		return emptyList(), nil
	}

	list := &CiliumNetworkPolicyList{}
	if err := r.c.List(ctx, list,
		&client.ListOptions{
			Namespace:     ns,
			LabelSelector: ls,
		}); err != nil {
		return nil, err
	}

	listRV := list.ResourceVersion
	if listRV == "" {
		listRV, _ = registry.MaxResourceVersion(list)
	}

	out := &sdnv1alpha1.SecurityGroupList{
		TypeMeta: metav1.TypeMeta{
			APIVersion: sdnv1alpha1.SchemeGroupVersion.String(),
			Kind:       kindSGList,
		},
		ListMeta: metav1.ListMeta{ResourceVersion: listRV},
	}

	for i := range list.Items {
		if !fieldFilter.MatchesName(list.Items[i].Name) {
			continue
		}
		if !fieldFilter.MatchesNamespace(list.Items[i].Namespace) {
			continue
		}
		out.Items = append(out.Items, *policyToSecurityGroup(&list.Items[i]))
	}
	sorting.ByNamespacedName[sdnv1alpha1.SecurityGroup, *sdnv1alpha1.SecurityGroup](out.Items)
	return out, nil
}

// Update creates or updates the CiliumNetworkPolicy backing the SecurityGroup.
func (r *REST) Update(
	ctx context.Context,
	name string,
	objInfo rest.UpdatedObjectInfo,
	createValidation rest.ValidateObjectFunc,
	updateValidation rest.ValidateObjectUpdateFunc,
	forceCreate bool,
	opts *metav1.UpdateOptions,
) (runtime.Object, bool, error) {
	ns, err := nsFrom(ctx)
	if err != nil {
		return nil, false, err
	}

	var cur *CiliumNetworkPolicy
	previous := &CiliumNetworkPolicy{}
	if err := r.c.Get(ctx, types.NamespacedName{Namespace: ns, Name: name}, previous, &client.GetOptions{Raw: &metav1.GetOptions{}}); err != nil {
		if !apierrors.IsNotFound(err) {
			return nil, false, err
		}
	} else {
		if !isSecurityGroup(previous) {
			return nil, false, apierrors.NewNotFound(r.gvr.GroupResource(), name)
		}
		cur = previous
	}

	oldObj := oldOrNil(cur)
	newObj, err := objInfo.UpdatedObject(ctx, oldObj)
	if err != nil {
		return nil, false, err
	}
	in, ok := newObj.(*sdnv1alpha1.SecurityGroup)
	if !ok {
		return nil, false, fmt.Errorf("expected SecurityGroup, got %T", newObj)
	}

	// Enforce name identity and bind the namespace to the request target. The
	// aggregated apiserver already rejects a mismatched name/namespace before the
	// storage, but enforcing it here stops a force-create/update from writing a
	// different object than the request addressed, independent of the caller.
	if in.Name != "" && in.Name != name {
		return nil, false, apierrors.NewBadRequest("metadata.name must match request name")
	}
	in = in.DeepCopy()
	in.Name = name
	in.Namespace = ns

	if err := validateSecurityGroup(in); err != nil {
		return nil, false, err
	}

	newNp := securityGroupToPolicy(in, cur)
	newNp.Namespace = ns
	if cur == nil {
		if !forceCreate {
			return nil, false, apierrors.NewNotFound(r.gvr.GroupResource(), name)
		}
		// Force-create path: run the create admission chain, mirroring Create.
		if createValidation != nil {
			if err := createValidation(ctx, in); err != nil {
				return nil, false, err
			}
		}
		err := r.c.Create(ctx, newNp, &client.CreateOptions{Raw: createOptionsFromUpdate(opts)})
		return policyToSecurityGroup(newNp), true, err
	}

	// Update path: run the update admission chain before persisting.
	if updateValidation != nil {
		if err := updateValidation(ctx, in, oldObj); err != nil {
			return nil, false, err
		}
	}

	// Honor the client-supplied resourceVersion for optimistic concurrency,
	// falling back to the current one only when the client sent none. Without
	// this a stale update would silently win instead of getting a 409 Conflict.
	if newNp.ResourceVersion == "" {
		newNp.ResourceVersion = cur.ResourceVersion
	}
	err = r.c.Update(ctx, newNp, &client.UpdateOptions{Raw: opts})
	return policyToSecurityGroup(newNp), false, err
}

// oldOrNil returns the SecurityGroup projection of cur, or nil when cur is nil,
// so UpdatedObject receives a typed nil interface for creates.
func oldOrNil(cur *CiliumNetworkPolicy) runtime.Object {
	if cur == nil {
		return nil
	}
	return policyToSecurityGroup(cur)
}

// Delete removes the CiliumNetworkPolicy backing the SecurityGroup.
func (r *REST) Delete(
	ctx context.Context,
	name string,
	deleteValidation rest.ValidateObjectFunc,
	opts *metav1.DeleteOptions,
) (runtime.Object, bool, error) {
	ns, err := nsFrom(ctx)
	if err != nil {
		return nil, false, err
	}
	current := &CiliumNetworkPolicy{}
	if err := r.c.Get(ctx, types.NamespacedName{Namespace: ns, Name: name}, current, &client.GetOptions{Raw: &metav1.GetOptions{}}); err != nil {
		return nil, false, err
	}
	if !isSecurityGroup(current) {
		return nil, false, apierrors.NewNotFound(r.gvr.GroupResource(), name)
	}
	// There is a benign get-then-delete window: if the marker were flipped off
	// between the Get and the Delete, an unmarked policy could be removed. Only
	// an actor with direct cilium.io write (platform/admin, outside the tenant
	// threat model) can flip the marker, so this is acceptable.
	// Run the delete admission chain on the resolved SecurityGroup before
	// removing the backing policy.
	if deleteValidation != nil {
		if err := deleteValidation(ctx, policyToSecurityGroup(current)); err != nil {
			return nil, false, err
		}
	}
	err = r.c.Delete(ctx, &CiliumNetworkPolicy{ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: name}}, &client.DeleteOptions{Raw: opts})
	return nil, err == nil, err
}

// PATCH is served by the generic apiserver handler via Get + Update (the storage
// only needs to satisfy rest.Patcher = Getter + Updater); there is no storage
// Patch method to implement. The Update path re-asserts the marker label, so a
// patch that drops it cannot orphan the backing policy.

// -----------------------------------------------------------------------------
// Watcher
// -----------------------------------------------------------------------------

// Watch streams SecurityGroup events translated from CiliumNetworkPolicy.
func (r *REST) Watch(ctx context.Context, opts *metainternal.ListOptions) (watch.Interface, error) {
	// Cluster-wide watch carries an empty namespace; NamespaceValue returns ""
	// and the client watches across all namespaces.
	ns := request.NamespaceValue(ctx)

	ls, selectable := buildSelector(opts)
	if !selectable {
		ch := make(chan watch.Event)
		close(ch)
		return watch.NewProxyWatcher(ch), nil
	}

	// Mirror List: a watch scoped by fieldSelector=metadata.name=… must not
	// stream every object.
	fieldFilter, err := fieldfilter.ParseFieldSelector(opts.FieldSelector)
	if err != nil {
		return nil, err
	}
	if fieldFilter.Namespace != "" && ns != "" && ns != fieldFilter.Namespace {
		ch := make(chan watch.Event)
		close(ch)
		return watch.NewProxyWatcher(ch), nil
	}

	sendInitialEvents := opts.SendInitialEvents != nil && *opts.SendInitialEvents

	npList := &CiliumNetworkPolicyList{}
	base, err := r.w.Watch(ctx, npList, &client.ListOptions{
		Namespace:     ns,
		LabelSelector: ls,
		Raw: &metav1.ListOptions{
			Watch:           true,
			ResourceVersion: opts.ResourceVersion,
			// Forward the WatchList request so the backing API server replays
			// existing objects as initial ADDED events and emits the terminating
			// bookmark; the InitialEventsBookmarker is built to consume that
			// backing bookmark (OnBackingBookmark) and only synthesizes one as a
			// fallback. ResourceVersionMatch must accompany SendInitialEvents.
			SendInitialEvents:    opts.SendInitialEvents,
			ResourceVersionMatch: opts.ResourceVersionMatch,
			// AllowWatchBookmarks and SendInitialEvents are independent watch
			// features: honor an explicit client bookmark request, and also enable
			// bookmarks when initial events are requested so the terminating
			// initial-events bookmark can fire.
			AllowWatchBookmarks: opts.AllowWatchBookmarks || sendInitialEvents,
		},
	})
	if err != nil {
		return nil, err
	}

	var startingRV uint64
	if opts.ResourceVersion != "" {
		if rv, err := strconv.ParseUint(opts.ResourceVersion, 10, 64); err == nil {
			startingRV = rv
		}
	}

	bookmarker := registry.NewInitialEventsBookmarker(sendInitialEvents, opts.ResourceVersion, func() runtime.Object {
		return &sdnv1alpha1.SecurityGroup{
			TypeMeta: metav1.TypeMeta{
				APIVersion: sdnv1alpha1.SchemeGroupVersion.String(),
				Kind:       kindSG,
			},
		}
	})

	ch := make(chan watch.Event)
	proxy := watch.NewProxyWatcher(ch)

	go func() {
		defer proxy.Stop()
		defer base.Stop()

		send := func(ev watch.Event) bool {
			select {
			case ch <- ev:
				return true
			case <-proxy.StopChan():
				return false
			case <-ctx.Done():
				return false
			}
		}

		for ev := range base.ResultChan() {
			if ev.Type == watch.Bookmark {
				if np, ok := ev.Object.(*CiliumNetworkPolicy); ok {
					bookmark, _ := bookmarker.OnBackingBookmark(np.ResourceVersion)
					if !send(bookmark) {
						return
					}
				}
				continue
			}

			np, ok := ev.Object.(*CiliumNetworkPolicy)
			if !ok || np == nil {
				continue
			}
			bookmarker.Observe(np.ResourceVersion)

			// DELETED events must always pass through: when a policy's labels
			// mutate out of the selector, the apiserver synthesizes a DELETED with
			// the new (non-matching) labels — dropping it would leave cached
			// clients with stale entries.
			if ev.Type != watch.Deleted && !ls.Matches(labels.Set(np.Labels)) {
				continue
			}
			if ev.Type != watch.Deleted && (!fieldFilter.MatchesName(np.Name) || !fieldFilter.MatchesNamespace(np.Namespace)) {
				continue
			}

			sg := policyToSecurityGroup(np)

			if ev.Type == watch.Added && startingRV > 0 {
				objRV, parseErr := strconv.ParseUint(sg.ResourceVersion, 10, 64)
				if parseErr == nil && objRV <= startingRV {
					continue
				}
			}

			if bookmark, ok := bookmarker.BeforeLiveEvent(ev.Type); ok {
				if !send(bookmark) {
					return
				}
			}

			if !send(watch.Event{Type: ev.Type, Object: sg}) {
				return
			}
		}

		if bookmark, ok := bookmarker.OnClose(); ok {
			send(bookmark)
		}
	}()

	return proxy, nil
}

// -----------------------------------------------------------------------------
// TableConvertor
// -----------------------------------------------------------------------------

// ConvertToTable renders SecurityGroups for kubectl's table output.
func (r *REST) ConvertToTable(_ context.Context, obj runtime.Object, _ runtime.Object) (*metav1.Table, error) {
	now := time.Now()
	row := func(o *sdnv1alpha1.SecurityGroup) metav1.TableRow {
		return metav1.TableRow{
			Cells:  []interface{}{o.Name, duration.HumanDuration(now.Sub(o.CreationTimestamp.Time))},
			Object: runtime.RawExtension{Object: o},
		}
	}

	tbl := &metav1.Table{
		TypeMeta: metav1.TypeMeta{APIVersion: "meta.k8s.io/v1", Kind: "Table"},
		ColumnDefinitions: []metav1.TableColumnDefinition{
			{Name: "NAME", Type: "string"},
			{Name: "AGE", Type: "string"},
		},
	}

	switch v := obj.(type) {
	case *sdnv1alpha1.SecurityGroupList:
		for i := range v.Items {
			tbl.Rows = append(tbl.Rows, row(&v.Items[i]))
		}
		tbl.ResourceVersion = v.ResourceVersion
	case *sdnv1alpha1.SecurityGroup:
		tbl.Rows = append(tbl.Rows, row(v))
		tbl.ResourceVersion = v.ResourceVersion
	default:
		return nil, notAcceptable{r.gvr.GroupResource(), fmt.Sprintf("unexpected %T", obj)}
	}
	return tbl, nil
}

// -----------------------------------------------------------------------------
// Boiler-plate
// -----------------------------------------------------------------------------

// Destroy releases resources held by the storage. There are none.
func (*REST) Destroy() {}

type notAcceptable struct {
	resource schema.GroupResource
	message  string
}

func (e notAcceptable) Error() string { return e.message }
func (e notAcceptable) Status() metav1.Status {
	return metav1.Status{
		Status:  metav1.StatusFailure,
		Code:    http.StatusNotAcceptable,
		Reason:  metav1.StatusReason("NotAcceptable"),
		Message: e.message,
	}
}
