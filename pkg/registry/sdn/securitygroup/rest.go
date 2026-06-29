// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Cozystack Authors.

// SecurityGroup registry – namespaced projection over CiliumNetworkPolicy
// objects labelled "sdn.cozystack.io/securitygroup=true". The marker label is
// hidden from the SecurityGroup view.

package securitygroup

import (
	"context"
	"encoding/json"
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

	// attachmentsAnnotation stores the SecurityGroup's spec.attachments on the
	// backing CiliumNetworkPolicy as a JSON array of ApplicationReference. The
	// attachment list has no home in the CiliumNetworkPolicy spec (the backing
	// endpointSelector is the SecurityGroup's own membership label, not the
	// attached apps' labels), so the storage owns this annotation: it is
	// re-asserted from spec.attachments on every write and hidden from the
	// SecurityGroup view on read, exactly like the marker label. The
	// securitygroup-controller reads it to know which apps' pods to label.
	attachmentsAnnotation = "sdn.cozystack.io/attachments"

	// appGroupLabelKey, appKindLabelKey and appNameLabelKey are the lineage
	// labels the lineage mutating webhook stamps on every managed-app pod
	// (see internal/lineagecontrollerwebhook/webhook.go ManagerGroupKey/
	// ManagerKindKey/ManagerNameKey). fromApp/toApp peers project into
	// endpointSelectors on these keys, and the securitygroup-controller resolves
	// attachments to pods by them. Mirrored here to keep this registry package
	// from depending on internal/.
	appGroupLabelKey = "apps.cozystack.io/application.group"
	appKindLabelKey  = "apps.cozystack.io/application.kind"
	appNameLabelKey  = "apps.cozystack.io/application.name"

	// defaultAppGroup is the API group an ApplicationReference defaults to when
	// APIGroup is empty — the group under which Cozystack serves managed apps.
	defaultAppGroup = "apps.cozystack.io"

	singularName = sdnv1alpha1.SecurityGroupSingularName
	kindSG       = sdnv1alpha1.SecurityGroupKind
	kindSGList   = sdnv1alpha1.SecurityGroupListKind
)

// membershipLabelKey returns the SecurityGroup's own membership label key, the
// backing CiliumNetworkPolicy's endpointSelector match key and the key a
// fromSG/toSG peer resolves to on the referenced group.
func membershipLabelKey(name string) string {
	return sdnv1alpha1.MembershipLabelPrefix + name
}

// buildEndpointSelector returns the backing endpointSelector — the
// SecurityGroup's own membership label — so the policy applies to exactly the
// pods the securitygroup-controller has stamped as members.
func buildEndpointSelector(name string) metav1.LabelSelector {
	return metav1.LabelSelector{MatchLabels: map[string]string{membershipLabelKey(name): ""}}
}

// appLabels projects an ApplicationReference into the lineage labels that select
// the referenced application's pods. An empty APIGroup defaults to
// apps.cozystack.io.
func appLabels(ref sdnv1alpha1.ApplicationReference) map[string]string {
	group := ref.APIGroup
	if group == "" {
		group = defaultAppGroup
	}
	return map[string]string{
		appGroupLabelKey: group,
		appKindLabelKey:  ref.Kind,
		appNameLabelKey:  ref.Name,
	}
}

// appFromSelector reads an endpointSelector built from appLabels back into an
// ApplicationReference. It matches only a selector carrying exactly the three
// lineage keys, so a fromSG selector (a single membership key) is not mistaken
// for an app peer.
func appFromSelector(sel metav1.LabelSelector) (sdnv1alpha1.ApplicationReference, bool) {
	if len(sel.MatchLabels) != 3 {
		return sdnv1alpha1.ApplicationReference{}, false
	}
	g, gok := sel.MatchLabels[appGroupLabelKey]
	k, kok := sel.MatchLabels[appKindLabelKey]
	n, nok := sel.MatchLabels[appNameLabelKey]
	if !gok || !kok || !nok {
		return sdnv1alpha1.ApplicationReference{}, false
	}
	return sdnv1alpha1.ApplicationReference{APIGroup: g, Kind: k, Name: n}, true
}

// sgFromSelector reads a membership-label endpointSelector back into the name of
// the referenced SecurityGroup. It matches only a selector carrying exactly one
// membership key.
func sgFromSelector(sel metav1.LabelSelector) (string, bool) {
	if len(sel.MatchLabels) != 1 {
		return "", false
	}
	for k := range sel.MatchLabels {
		if strings.HasPrefix(k, sdnv1alpha1.MembershipLabelPrefix) {
			return strings.TrimPrefix(k, sdnv1alpha1.MembershipLabelPrefix), true
		}
	}
	return "", false
}

// projectIngress turns the SecurityGroup ingress rules into Cilium ingress
// rules: fromApp peers become lineage-label endpointSelectors, fromSG peers
// become membership-label endpointSelectors, and fromCIDR/toPorts carry over.
// It always returns a non-nil slice (empty when there are no rules): the backing
// CiliumNetworkPolicy's ingress section must be present on the wire to satisfy
// the CRD anyOf, and a non-nil empty slice serializes to an empty list rather
// than null (which the CRD, having no nullable fields, would reject).
func projectIngress(rules []sdnv1alpha1.IngressRule) []CiliumIngressRule {
	out := make([]CiliumIngressRule, len(rules))
	for i := range rules {
		var eps []metav1.LabelSelector
		for _, app := range rules[i].FromApp {
			eps = append(eps, metav1.LabelSelector{MatchLabels: appLabels(app)})
		}
		for _, name := range rules[i].FromSG {
			eps = append(eps, metav1.LabelSelector{MatchLabels: map[string]string{membershipLabelKey(name): ""}})
		}
		out[i] = CiliumIngressRule{
			FromEndpoints: eps,
			FromCIDR:      append([]string(nil), rules[i].FromCIDR...),
			ToPorts:       rules[i].ToPorts,
		}
	}
	return out
}

// projectEgress is the egress counterpart of projectIngress.
func projectEgress(rules []sdnv1alpha1.EgressRule) []CiliumEgressRule {
	if rules == nil {
		return nil
	}
	out := make([]CiliumEgressRule, len(rules))
	for i := range rules {
		var eps []metav1.LabelSelector
		for _, app := range rules[i].ToApp {
			eps = append(eps, metav1.LabelSelector{MatchLabels: appLabels(app)})
		}
		for _, name := range rules[i].ToSG {
			eps = append(eps, metav1.LabelSelector{MatchLabels: map[string]string{membershipLabelKey(name): ""}})
		}
		out[i] = CiliumEgressRule{
			ToEndpoints: eps,
			ToCIDR:      append([]string(nil), rules[i].ToCIDR...),
			ToFQDNs:     rules[i].ToFQDNs,
			ToPorts:     rules[i].ToPorts,
		}
	}
	return out
}

// reconstructIngress is the inverse of projectIngress, reading Cilium ingress
// rules back into the SecurityGroup view. Endpoint selectors that match neither
// the lineage-label nor the membership-label shape are ignored, so a
// hand-edited backing policy degrades gracefully rather than erroring.
func reconstructIngress(rules []CiliumIngressRule) []sdnv1alpha1.IngressRule {
	// Collapse an empty list to nil so the always-present empty ingress section the
	// projection emits for a rules-less group (to satisfy the CRD anyOf) is
	// invisible in the SecurityGroup view, keeping the rules-less round-trip exact.
	if len(rules) == 0 {
		return nil
	}
	out := make([]sdnv1alpha1.IngressRule, len(rules))
	for i := range rules {
		var apps []sdnv1alpha1.ApplicationReference
		var sgs []string
		for _, ep := range rules[i].FromEndpoints {
			if app, ok := appFromSelector(ep); ok {
				apps = append(apps, app)
			} else if name, ok := sgFromSelector(ep); ok {
				sgs = append(sgs, name)
			}
		}
		out[i] = sdnv1alpha1.IngressRule{
			FromApp:  apps,
			FromSG:   sgs,
			FromCIDR: append([]string(nil), rules[i].FromCIDR...),
			ToPorts:  rules[i].ToPorts,
		}
	}
	return out
}

// reconstructEgress is the egress counterpart of reconstructIngress.
func reconstructEgress(rules []CiliumEgressRule) []sdnv1alpha1.EgressRule {
	// Collapse an empty list to nil for the same reason as reconstructIngress, so a
	// SecurityGroup that carries no egress rules round-trips with egress unset.
	if len(rules) == 0 {
		return nil
	}
	out := make([]sdnv1alpha1.EgressRule, len(rules))
	for i := range rules {
		var apps []sdnv1alpha1.ApplicationReference
		var sgs []string
		for _, ep := range rules[i].ToEndpoints {
			if app, ok := appFromSelector(ep); ok {
				apps = append(apps, app)
			} else if name, ok := sgFromSelector(ep); ok {
				sgs = append(sgs, name)
			}
		}
		out[i] = sdnv1alpha1.EgressRule{
			ToApp:   apps,
			ToSG:    sgs,
			ToCIDR:  append([]string(nil), rules[i].ToCIDR...),
			ToFQDNs: rules[i].ToFQDNs,
			ToPorts: rules[i].ToPorts,
		}
	}
	return out
}

// encodeAttachments serializes spec.attachments for the backing-policy
// annotation. An empty APIGroup is canonicalized to apps.cozystack.io so the
// stored value is unambiguous for the securitygroup-controller (and surfaced
// that way on read). An empty list yields the empty string so the caller omits
// the annotation entirely.
func encodeAttachments(refs []sdnv1alpha1.ApplicationReference) string {
	if len(refs) == 0 {
		return ""
	}
	canon := make([]sdnv1alpha1.ApplicationReference, len(refs))
	for i, r := range refs {
		if r.APIGroup == "" {
			r.APIGroup = defaultAppGroup
		}
		canon[i] = r
	}
	b, err := json.Marshal(canon)
	if err != nil {
		return ""
	}
	return string(b)
}

// decodeAttachments parses the backing-policy annotation back into
// spec.attachments. A missing or malformed value yields nil, so a hand-edited
// backing policy degrades gracefully rather than erroring.
func decodeAttachments(s string) []sdnv1alpha1.ApplicationReference {
	if s == "" {
		return nil
	}
	var refs []sdnv1alpha1.ApplicationReference
	if err := json.Unmarshal([]byte(s), &refs); err != nil {
		return nil
	}
	return refs
}

// stripMarkerLabel returns a copy of m without the SecurityGroup marker label.
func stripMarkerLabel(m map[string]string) map[string]string {
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

// stripInternalAnnotations returns a copy of m without the storage-owned
// attachments annotation, which is surfaced as spec.attachments instead. A
// result with no entries is returned as nil so the SecurityGroup view carries no
// empty annotations map.
func stripInternalAnnotations(m map[string]string) map[string]string {
	if m == nil {
		return nil
	}
	out := make(map[string]string, len(m))
	for k, v := range m {
		if k == attachmentsAnnotation {
			continue
		}
		out[k] = v
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// hasFinalizer reports whether list contains the named finalizer.
func hasFinalizer(list []string, name string) bool {
	for _, f := range list {
		if f == name {
			return true
		}
	}
	return false
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
			// Surface the deletion timestamp so a SecurityGroup whose backing
			// policy is terminating (finalizer still pending) shows as Terminating
			// to kubectl, instead of looking live.
			DeletionTimestamp: np.DeletionTimestamp,
			Labels:            stripMarkerLabel(np.Labels),
			Annotations:       stripInternalAnnotations(np.Annotations),
			OwnerReferences:   np.OwnerReferences,
			Finalizers:        np.Finalizers,
		},
	}
	if np.Spec != nil {
		spec := np.Spec.DeepCopy()
		sg.Spec = sdnv1alpha1.SecurityGroupSpec{
			// Attachments live in a storage-owned annotation, not the
			// endpointSelector (which is the SecurityGroup's own membership label).
			Attachments: decodeAttachments(np.Annotations[attachmentsAnnotation]),
			Ingress:     reconstructIngress(spec.Ingress),
			Egress:      reconstructEgress(spec.Egress),
		}
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

	// Rebuild annotations from exactly what the request carries, then re-assert
	// the storage-owned attachments annotation last (like the marker label) from
	// spec.attachments — so the tenant cannot set or clobber it directly, and a
	// cleared attachments list drops the annotation rather than leaving it stale.
	out.Annotations = make(map[string]string, len(sg.Annotations)+1)
	for k, v := range sg.Annotations {
		out.Annotations[k] = v
	}
	if enc := encodeAttachments(sg.Spec.Attachments); enc != "" {
		out.Annotations[attachmentsAnnotation] = enc
	} else {
		delete(out.Annotations, attachmentsAnnotation)
	}
	if len(out.Annotations) == 0 {
		out.Annotations = nil
	}

	// Project ownerReferences and finalizers 1:1 so Kubernetes garbage
	// collection and finalizers work on SecurityGroup objects. Like labels and
	// annotations these follow replace semantics — they reflect the request.
	out.OwnerReferences = sg.DeepCopy().OwnerReferences
	out.Finalizers = append([]string(nil), sg.Finalizers...)

	// Re-assert the platform-owned membership finalizer the way the marker label
	// and attachments annotation are re-asserted. The securitygroup-controller
	// adds it via merge patch, so it is not in the tenant-facing request body; any
	// tenant write that omits it (a full-replace PUT) would otherwise strip it.
	// Removing this finalizer is exclusively the controller's job — it does so via
	// its own merge patch during the deletion-cleanup reconcile, never through
	// this projection — so re-assert it from cur unconditionally, INCLUDING while
	// the object is terminating. Otherwise a tenant could delete then PUT the
	// still-terminating object to drop the finalizer, hard-deleting the backing
	// policy before the controller strips the membership labels off member pods
	// and orphaning them.
	if cur != nil &&
		hasFinalizer(cur.Finalizers, sdnv1alpha1.MembershipFinalizer) &&
		!hasFinalizer(out.Finalizers, sdnv1alpha1.MembershipFinalizer) {
		out.Finalizers = append(out.Finalizers, sdnv1alpha1.MembershipFinalizer)
	}

	// The backing endpointSelector is the SecurityGroup's own membership label,
	// not a tenant-authored selector: the securitygroup-controller stamps that
	// label onto the attached applications' pods. Rules project app/SG peers into
	// endpoint selectors.
	spec := sg.Spec.DeepCopy()
	out.Spec = &CiliumNetworkPolicySpec{
		EndpointSelector: buildEndpointSelector(sg.Name),
		Ingress:          projectIngress(spec.Ingress),
		Egress:           projectEgress(spec.Egress),
	}
	// Normalize the protocol to upper case: validation accepts it case
	// insensitively, but the backing CiliumNetworkPolicy CRD enforces a strict
	// upper-case enum, so a raw "tcp" would be rejected on the backing write.
	normalizePortProtocols(out.Spec)
	return &out
}

// normalizePortProtocols upper-cases every port protocol in place so the value
// written to the backing CiliumNetworkPolicy matches its case-sensitive enum.
func normalizePortProtocols(spec *CiliumNetworkPolicySpec) {
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

			// watch.Error carries a *metav1.Status, not a CiliumNetworkPolicy, so
			// the type assertion below would silently drop it and leave the client
			// with a cleanly closed stream. Forward it verbatim so the client sees
			// the error (e.g. a 410 Gone for an expired resourceVersion) and
			// performs the required relist.
			if ev.Type == watch.Error {
				if !send(ev) {
					return
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
			// Unlike labels, a policy's name and namespace are immutable, so the
			// DELETED bypass that the label selector needs does not apply here: a
			// field-selected watch (e.g. metadata.name=sg-a) must not receive
			// sg-b's deletion. Apply the field filter to every event type.
			if !fieldFilter.MatchesName(np.Name) || !fieldFilter.MatchesNamespace(np.Namespace) {
				continue
			}

			sg := policyToSecurityGroup(np)

			// De-dup ADDED events the client has already seen when resuming a plain
			// watch from a resourceVersion. This must NOT run during a
			// sendInitialEvents replay: there the backing API intentionally re-emits
			// existing objects as initial ADDED events with RV <= startingRV, and
			// dropping them would break the initial-state replay.
			if !sendInitialEvents && ev.Type == watch.Added && startingRV > 0 {
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
