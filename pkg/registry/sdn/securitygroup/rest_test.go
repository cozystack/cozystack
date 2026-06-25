// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Cozystack Authors.

package securitygroup

import (
	"context"
	"errors"
	"reflect"
	"sort"
	"strings"
	"testing"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metainternal "k8s.io/apimachinery/pkg/apis/meta/internalversion"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/fields"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/watch"
	"k8s.io/apiserver/pkg/endpoints/request"
	"k8s.io/apiserver/pkg/registry/rest"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	sdnv1alpha1 "github.com/cozystack/cozystack/pkg/apis/sdn/v1alpha1"
)

const testNamespace = "tenant-root"

func newTestREST(t *testing.T, policies ...*CiliumNetworkPolicy) *REST {
	t.Helper()

	scheme := runtime.NewScheme()
	if err := AddToScheme(scheme); err != nil {
		t.Fatalf("add cilium mirror to scheme: %v", err)
	}

	objs := make([]client.Object, 0, len(policies))
	for _, p := range policies {
		objs = append(objs, p)
	}
	fc := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(objs...).
		Build()

	return &REST{
		c: fc,
		w: fc,
		gvr: schema.GroupVersionResource{
			Group:    sdnv1alpha1.GroupName,
			Version:  "v1alpha1",
			Resource: sdnv1alpha1.SecurityGroupPluralName,
		},
	}
}

// markedPolicy is a CiliumNetworkPolicy carrying the SecurityGroup marker label.
func markedPolicy(name string) *CiliumNetworkPolicy {
	return &CiliumNetworkPolicy{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: testNamespace,
			Labels:    map[string]string{sgLabelKey: sgLabelValue},
		},
	}
}

// unmarkedPolicy is a CiliumNetworkPolicy without the marker label (e.g. a
// platform tenant-isolation policy) that SecurityGroup must not surface.
func unmarkedPolicy(name string) *CiliumNetworkPolicy {
	return &CiliumNetworkPolicy{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: testNamespace,
		},
	}
}

func sampleSpec() sdnv1alpha1.SecurityGroupSpec {
	return sdnv1alpha1.SecurityGroupSpec{
		Attachments: []sdnv1alpha1.ApplicationReference{
			{APIGroup: "apps.cozystack.io", Kind: "Postgres", Name: "db"},
		},
		Ingress: []sdnv1alpha1.IngressRule{
			{
				// Explicit APIGroup keeps the round-trip exact (an empty group is
				// surfaced as the defaulted apps.cozystack.io on read).
				FromApp:  []sdnv1alpha1.ApplicationReference{{APIGroup: "apps.cozystack.io", Kind: "Kubernetes", Name: "web"}},
				FromSG:   []string{"frontend"},
				FromCIDR: []string{"10.0.0.0/8"},
				ToPorts: []sdnv1alpha1.PortRule{
					{Ports: []sdnv1alpha1.PortProtocol{{Port: "5432", Protocol: "TCP"}}},
				},
			},
		},
		Egress: []sdnv1alpha1.EgressRule{
			{
				ToFQDNs: []sdnv1alpha1.FQDNSelector{{MatchPattern: "*.apt.example.org"}},
				ToCIDR:  []string{"192.0.2.0/24"},
			},
		},
	}
}

func ctxNS() context.Context {
	return request.WithNamespace(context.Background(), testNamespace)
}

func createSG(t *testing.T, r *REST, sg *sdnv1alpha1.SecurityGroup) *sdnv1alpha1.SecurityGroup {
	t.Helper()
	out, err := r.Create(ctxNS(), sg, nil, &metav1.CreateOptions{})
	if err != nil {
		t.Fatalf("Create returned error: %v", err)
	}
	got, ok := out.(*sdnv1alpha1.SecurityGroup)
	if !ok {
		t.Fatalf("Create returned %T, want *SecurityGroup", out)
	}
	return got
}

func TestCreateTranslatesSpecAndMarksPolicy(t *testing.T) {
	r := newTestREST(t)

	in := &sdnv1alpha1.SecurityGroup{
		ObjectMeta: metav1.ObjectMeta{Name: "sg-db", Namespace: testNamespace},
		Spec:       sampleSpec(),
	}
	got := createSG(t, r, in)

	// The returned SecurityGroup must not leak the internal marker label.
	if _, ok := got.Labels[sgLabelKey]; ok {
		t.Fatalf("Create leaked marker label into SecurityGroup view: %v", got.Labels)
	}

	// The backing CiliumNetworkPolicy must carry the marker label and the spec.
	np := &CiliumNetworkPolicy{}
	if err := r.c.Get(context.Background(), types.NamespacedName{Namespace: testNamespace, Name: "sg-db"}, np); err != nil {
		t.Fatalf("backing policy not found: %v", err)
	}
	if np.Labels[sgLabelKey] != sgLabelValue {
		t.Fatalf("backing policy missing marker label: %v", np.Labels)
	}
	if np.Spec == nil {
		t.Fatalf("backing policy has no spec")
	}
	// The endpointSelector must be the SecurityGroup's own membership label, not
	// any attached app's labels.
	wantSel := map[string]string{"securitygroup.sdn.cozystack.io/sg-db": ""}
	if !reflect.DeepEqual(np.Spec.EndpointSelector.MatchLabels, wantSel) {
		t.Fatalf("membership endpointSelector mismatch:\n got: %+v\nwant: %+v", np.Spec.EndpointSelector.MatchLabels, wantSel)
	}
	// The attachments are persisted as a storage-owned annotation on the backing
	// policy and hidden from the SecurityGroup view.
	if np.Annotations[attachmentsAnnotation] == "" {
		t.Fatalf("backing policy missing attachments annotation: %v", np.Annotations)
	}
	if _, ok := got.Annotations[attachmentsAnnotation]; ok {
		t.Fatalf("Create leaked attachments annotation into SecurityGroup view: %v", got.Annotations)
	}
	// fromApp projects to a lineage-label endpointSelector; fromSG to a
	// membership-label endpointSelector; fromCIDR carries over.
	ing := np.Spec.Ingress
	if len(ing) != 1 || len(ing[0].FromEndpoints) != 2 {
		t.Fatalf("ingress projection mismatch: %+v", ing)
	}
	wantApp := map[string]string{
		"apps.cozystack.io/application.group": "apps.cozystack.io",
		"apps.cozystack.io/application.kind":  "Kubernetes",
		"apps.cozystack.io/application.name":  "web",
	}
	if !reflect.DeepEqual(ing[0].FromEndpoints[0].MatchLabels, wantApp) {
		t.Fatalf("fromApp not projected to lineage labels: %+v", ing[0].FromEndpoints[0].MatchLabels)
	}
	wantSG := map[string]string{"securitygroup.sdn.cozystack.io/frontend": ""}
	if !reflect.DeepEqual(ing[0].FromEndpoints[1].MatchLabels, wantSG) {
		t.Fatalf("fromSG not projected to membership label: %+v", ing[0].FromEndpoints[1].MatchLabels)
	}
	if len(ing[0].FromCIDR) != 1 || ing[0].FromCIDR[0] != "10.0.0.0/8" {
		t.Fatalf("fromCIDR not carried over: %+v", ing[0].FromCIDR)
	}
}

func TestCreateGetSpecRoundTrip(t *testing.T) {
	r := newTestREST(t)
	in := &sdnv1alpha1.SecurityGroup{
		ObjectMeta: metav1.ObjectMeta{Name: "sg-db", Namespace: testNamespace},
		Spec:       sampleSpec(),
	}
	createSG(t, r, in)

	out, err := r.Get(ctxNS(), "sg-db", &metav1.GetOptions{})
	if err != nil {
		t.Fatalf("Get returned error: %v", err)
	}
	got := out.(*sdnv1alpha1.SecurityGroup)
	if !reflect.DeepEqual(got.Spec, sampleSpec()) {
		t.Fatalf("spec round-trip mismatch:\n got: %+v\nwant: %+v", got.Spec, sampleSpec())
	}
}

func TestCreateDefaultsAttachmentAPIGroup(t *testing.T) {
	r := newTestREST(t)
	in := &sdnv1alpha1.SecurityGroup{
		ObjectMeta: metav1.ObjectMeta{Name: "sg-db", Namespace: testNamespace},
		Spec: sdnv1alpha1.SecurityGroupSpec{
			Attachments: []sdnv1alpha1.ApplicationReference{{Kind: "Postgres", Name: "db"}},
		},
	}
	createSG(t, r, in)

	// The backing policy's attachments annotation defaults the empty APIGroup to
	// apps.cozystack.io so the controller resolves the right lineage labels.
	np := &CiliumNetworkPolicy{}
	if err := r.c.Get(context.Background(), types.NamespacedName{Namespace: testNamespace, Name: "sg-db"}, np); err != nil {
		t.Fatalf("backing policy not found: %v", err)
	}
	att := decodeAttachments(np.Annotations[attachmentsAnnotation])
	if len(att) != 1 || att[0].APIGroup != "apps.cozystack.io" {
		t.Fatalf("attachment APIGroup not defaulted: %+v", att)
	}
}

func TestEndpointSelectorIsMembershipLabel(t *testing.T) {
	r := newTestREST(t)
	createSG(t, r, &sdnv1alpha1.SecurityGroup{
		ObjectMeta: metav1.ObjectMeta{Name: "sg-db", Namespace: testNamespace},
		Spec: sdnv1alpha1.SecurityGroupSpec{
			Attachments: []sdnv1alpha1.ApplicationReference{{Kind: "Postgres", Name: "db"}},
		},
	})

	np := &CiliumNetworkPolicy{}
	if err := r.c.Get(context.Background(), types.NamespacedName{Namespace: testNamespace, Name: "sg-db"}, np); err != nil {
		t.Fatalf("backing policy not found: %v", err)
	}
	want := map[string]string{"securitygroup.sdn.cozystack.io/sg-db": ""}
	if np.Spec == nil || !reflect.DeepEqual(np.Spec.EndpointSelector.MatchLabels, want) {
		t.Fatalf("membership endpointSelector = %+v, want matchLabels %+v", np.Spec, want)
	}
	if len(np.Spec.EndpointSelector.MatchExpressions) != 0 {
		t.Fatalf("membership endpointSelector must carry no matchExpressions: %+v", np.Spec.EndpointSelector.MatchExpressions)
	}
}

func TestGetUnmarkedPolicyIsNotFound(t *testing.T) {
	r := newTestREST(t, unmarkedPolicy("tenant-isolation"))
	_, err := r.Get(ctxNS(), "tenant-isolation", &metav1.GetOptions{})
	if !apierrors.IsNotFound(err) {
		t.Fatalf("Get of unmarked policy: got err %v, want NotFound", err)
	}
}

func TestListReturnsOnlyMarkedPolicies(t *testing.T) {
	r := newTestREST(t,
		markedPolicy("sg-b"),
		markedPolicy("sg-a"),
		unmarkedPolicy("tenant-isolation"),
	)

	out, err := r.List(ctxNS(), &metainternal.ListOptions{})
	if err != nil {
		t.Fatalf("List returned error: %v", err)
	}
	list := out.(*sdnv1alpha1.SecurityGroupList)

	names := make([]string, len(list.Items))
	for i, it := range list.Items {
		names[i] = it.Name
		if _, ok := it.Labels[sgLabelKey]; ok {
			t.Fatalf("List leaked marker label for %q", it.Name)
		}
	}
	// List must exclude the unmarked policy and be sorted by name.
	want := []string{"sg-a", "sg-b"}
	if !sort.StringsAreSorted(names) || !reflect.DeepEqual(names, want) {
		t.Fatalf("List names = %v, want sorted %v", names, want)
	}
}

func TestUpdateModifiesSpec(t *testing.T) {
	r := newTestREST(t)
	createSG(t, r, &sdnv1alpha1.SecurityGroup{
		ObjectMeta: metav1.ObjectMeta{Name: "sg-db", Namespace: testNamespace},
		Spec:       sampleSpec(),
	})

	updated := &sdnv1alpha1.SecurityGroup{
		ObjectMeta: metav1.ObjectMeta{Name: "sg-db", Namespace: testNamespace},
		Spec: sdnv1alpha1.SecurityGroupSpec{
			Attachments: []sdnv1alpha1.ApplicationReference{{APIGroup: "apps.cozystack.io", Kind: "Postgres", Name: "db"}},
			Ingress: []sdnv1alpha1.IngressRule{
				{ToPorts: []sdnv1alpha1.PortRule{{Ports: []sdnv1alpha1.PortProtocol{{Port: "6379", Protocol: "TCP"}}}}},
			},
		},
	}
	out, created, err := r.Update(ctxNS(), "sg-db",
		rest.DefaultUpdatedObjectInfo(updated), nil, nil, false, &metav1.UpdateOptions{})
	if err != nil {
		t.Fatalf("Update returned error: %v", err)
	}
	if created {
		t.Fatalf("Update reported created=true for an existing object")
	}
	got := out.(*sdnv1alpha1.SecurityGroup)
	if !reflect.DeepEqual(got.Spec, updated.Spec) {
		t.Fatalf("Update spec mismatch:\n got: %+v\nwant: %+v", got.Spec, updated.Spec)
	}
}

func TestDeleteUnmarkedPolicyIsNotFound(t *testing.T) {
	r := newTestREST(t, unmarkedPolicy("tenant-isolation"))
	_, _, err := r.Delete(ctxNS(), "tenant-isolation", nil, &metav1.DeleteOptions{})
	if !apierrors.IsNotFound(err) {
		t.Fatalf("Delete of unmarked policy: got err %v, want NotFound", err)
	}
}

func TestDeleteRemovesBackingPolicy(t *testing.T) {
	r := newTestREST(t, markedPolicy("sg-db"))
	_, deleted, err := r.Delete(ctxNS(), "sg-db", nil, &metav1.DeleteOptions{})
	if err != nil {
		t.Fatalf("Delete returned error: %v", err)
	}
	if !deleted {
		t.Fatalf("Delete reported deleted=false")
	}
	np := &CiliumNetworkPolicy{}
	err = r.c.Get(context.Background(), types.NamespacedName{Namespace: testNamespace, Name: "sg-db"}, np)
	if !apierrors.IsNotFound(err) {
		t.Fatalf("backing policy still present after delete: err=%v", err)
	}
}

func TestCreateOverExistingUnmarkedPolicyFails(t *testing.T) {
	r := newTestREST(t, unmarkedPolicy("tenant-isolation"))

	in := &sdnv1alpha1.SecurityGroup{
		ObjectMeta: metav1.ObjectMeta{Name: "tenant-isolation", Namespace: testNamespace},
		Spec:       sampleSpec(),
	}
	_, err := r.Create(ctxNS(), in, nil, &metav1.CreateOptions{})
	if !apierrors.IsAlreadyExists(err) {
		t.Fatalf("Create over unmarked policy: got err %v, want AlreadyExists", err)
	}

	// The platform policy must be left untouched (no marker, no spec).
	np := &CiliumNetworkPolicy{}
	if err := r.c.Get(context.Background(), types.NamespacedName{Namespace: testNamespace, Name: "tenant-isolation"}, np); err != nil {
		t.Fatalf("unmarked policy disappeared: %v", err)
	}
	if _, ok := np.Labels[sgLabelKey]; ok {
		t.Fatalf("Create hijacked the unmarked platform policy with the marker label: %v", np.Labels)
	}
	if np.Spec != nil {
		t.Fatalf("Create clobbered the unmarked platform policy spec: %+v", np.Spec)
	}
}

func TestUpdateForceCreateOverUnmarkedPolicyDoesNotClobber(t *testing.T) {
	r := newTestREST(t, unmarkedPolicy("tenant-isolation"))

	in := &sdnv1alpha1.SecurityGroup{
		ObjectMeta: metav1.ObjectMeta{Name: "tenant-isolation", Namespace: testNamespace},
		Spec:       sampleSpec(),
	}
	_, _, err := r.Update(ctxNS(), "tenant-isolation",
		rest.DefaultUpdatedObjectInfo(in), nil, nil, true, &metav1.UpdateOptions{})
	if !apierrors.IsNotFound(err) {
		t.Fatalf("Update force-create over unmarked policy: got err %v, want NotFound", err)
	}

	np := &CiliumNetworkPolicy{}
	if err := r.c.Get(context.Background(), types.NamespacedName{Namespace: testNamespace, Name: "tenant-isolation"}, np); err != nil {
		t.Fatalf("unmarked policy disappeared: %v", err)
	}
	if _, ok := np.Labels[sgLabelKey]; ok {
		t.Fatalf("Update hijacked the unmarked platform policy with the marker label: %v", np.Labels)
	}
	if np.Spec != nil {
		t.Fatalf("Update clobbered the unmarked platform policy spec: %+v", np.Spec)
	}
}

func TestUpdateForceCreateOnAbsentObjectCreates(t *testing.T) {
	r := newTestREST(t)
	in := &sdnv1alpha1.SecurityGroup{
		ObjectMeta: metav1.ObjectMeta{Name: "sg-db", Namespace: testNamespace},
		Spec:       sampleSpec(),
	}
	out, created, err := r.Update(ctxNS(), "sg-db",
		rest.DefaultUpdatedObjectInfo(in), nil, nil, true, &metav1.UpdateOptions{})
	if err != nil {
		t.Fatalf("Update force-create returned error: %v", err)
	}
	if !created {
		t.Fatalf("Update force-create on absent object reported created=false")
	}
	got := out.(*sdnv1alpha1.SecurityGroup)
	if !reflect.DeepEqual(got.Spec, sampleSpec()) {
		t.Fatalf("force-create spec mismatch:\n got: %+v\nwant: %+v", got.Spec, sampleSpec())
	}
	np := &CiliumNetworkPolicy{}
	if err := r.c.Get(context.Background(), types.NamespacedName{Namespace: testNamespace, Name: "sg-db"}, np); err != nil {
		t.Fatalf("backing policy not created: %v", err)
	}
	if np.Labels[sgLabelKey] != sgLabelValue {
		t.Fatalf("force-created policy missing marker label: %v", np.Labels)
	}
}

func TestListFieldSelectorByName(t *testing.T) {
	r := newTestREST(t, markedPolicy("sg-a"), markedPolicy("sg-b"))

	out, err := r.List(ctxNS(), &metainternal.ListOptions{
		FieldSelector: fields.SelectorFromSet(fields.Set{"metadata.name": "sg-a"}),
	})
	if err != nil {
		t.Fatalf("List returned error: %v", err)
	}
	list := out.(*sdnv1alpha1.SecurityGroupList)
	if len(list.Items) != 1 || list.Items[0].Name != "sg-a" {
		names := make([]string, len(list.Items))
		for i, it := range list.Items {
			names[i] = it.Name
		}
		t.Fatalf("field-selector metadata.name=sg-a: got %v, want [sg-a]", names)
	}
}

func TestConvertToTable(t *testing.T) {
	r := newTestREST(t)

	list := &sdnv1alpha1.SecurityGroupList{Items: []sdnv1alpha1.SecurityGroup{
		{ObjectMeta: metav1.ObjectMeta{Name: "sg-a"}},
		{ObjectMeta: metav1.ObjectMeta{Name: "sg-b"}},
	}}
	tbl, err := r.ConvertToTable(context.Background(), list, nil)
	if err != nil {
		t.Fatalf("ConvertToTable(list) error: %v", err)
	}
	if len(tbl.Rows) != 2 || len(tbl.ColumnDefinitions) != 2 {
		t.Fatalf("ConvertToTable(list): rows=%d cols=%d, want 2/2", len(tbl.Rows), len(tbl.ColumnDefinitions))
	}

	single := &sdnv1alpha1.SecurityGroup{ObjectMeta: metav1.ObjectMeta{Name: "sg-a"}}
	tbl, err = r.ConvertToTable(context.Background(), single, nil)
	if err != nil {
		t.Fatalf("ConvertToTable(single) error: %v", err)
	}
	if len(tbl.Rows) != 1 {
		t.Fatalf("ConvertToTable(single): rows=%d, want 1", len(tbl.Rows))
	}

	if _, err := r.ConvertToTable(context.Background(), &metav1.Status{}, nil); err == nil {
		t.Fatalf("ConvertToTable(unexpected type): expected an error, got nil")
	}
}

func TestCreateValidationRejected(t *testing.T) {
	r := newTestREST(t)
	denied := func(_ context.Context, _ runtime.Object) error { return errors.New("denied by admission") }

	in := &sdnv1alpha1.SecurityGroup{
		ObjectMeta: metav1.ObjectMeta{Name: "sg-db", Namespace: testNamespace},
		Spec:       sampleSpec(),
	}
	if _, err := r.Create(ctxNS(), in, denied, &metav1.CreateOptions{}); err == nil {
		t.Fatalf("Create: expected admission rejection, got nil")
	}
	// Nothing must have been persisted.
	np := &CiliumNetworkPolicy{}
	err := r.c.Get(context.Background(), types.NamespacedName{Namespace: testNamespace, Name: "sg-db"}, np)
	if !apierrors.IsNotFound(err) {
		t.Fatalf("Create persisted a policy despite admission rejection: err=%v", err)
	}
}

func TestUpdateValidationRejected(t *testing.T) {
	r := newTestREST(t, markedPolicy("sg-db"))
	denied := func(_ context.Context, _, _ runtime.Object) error { return errors.New("denied by admission") }

	in := &sdnv1alpha1.SecurityGroup{
		ObjectMeta: metav1.ObjectMeta{Name: "sg-db", Namespace: testNamespace},
		Spec:       sampleSpec(),
	}
	if _, _, err := r.Update(ctxNS(), "sg-db",
		rest.DefaultUpdatedObjectInfo(in), nil, denied, false, &metav1.UpdateOptions{}); err == nil {
		t.Fatalf("Update: expected admission rejection, got nil")
	}
}

func TestDeleteValidationRejected(t *testing.T) {
	r := newTestREST(t, markedPolicy("sg-db"))
	denied := func(_ context.Context, _ runtime.Object) error { return errors.New("denied by admission") }

	if _, _, err := r.Delete(ctxNS(), "sg-db", denied, &metav1.DeleteOptions{}); err == nil {
		t.Fatalf("Delete: expected admission rejection, got nil")
	}
	// The policy must still exist.
	np := &CiliumNetworkPolicy{}
	if err := r.c.Get(context.Background(), types.NamespacedName{Namespace: testNamespace, Name: "sg-db"}, np); err != nil {
		t.Fatalf("Delete removed the policy despite admission rejection: %v", err)
	}
}

// assertVisibleAndMarked verifies the named SecurityGroup is retrievable through
// the API (not orphaned) and that its backing policy carries the marker.
func assertVisibleAndMarked(t *testing.T, r *REST, name string) {
	t.Helper()
	if _, err := r.Get(ctxNS(), name, &metav1.GetOptions{}); err != nil {
		t.Fatalf("Get(%q): policy was orphaned from the SecurityGroup view: %v", name, err)
	}
	np := &CiliumNetworkPolicy{}
	if err := r.c.Get(context.Background(), types.NamespacedName{Namespace: testNamespace, Name: name}, np); err != nil {
		t.Fatalf("backing policy %q not found: %v", name, err)
	}
	if np.Labels[sgLabelKey] != sgLabelValue {
		t.Fatalf("marker label was overwritten on %q: %v", name, np.Labels)
	}
}

func TestCreateCannotOverrideMarkerLabel(t *testing.T) {
	// A tenant has full write on securitygroups, so they could try to overwrite
	// the storage-owned marker via spec labels and orphan an enforced policy.
	for _, badValue := range []string{"false", "anything"} {
		r := newTestREST(t)
		in := &sdnv1alpha1.SecurityGroup{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "sg-db",
				Namespace: testNamespace,
				Labels:    map[string]string{sgLabelKey: badValue},
			},
			Spec: sampleSpec(),
		}
		if _, err := r.Create(ctxNS(), in, nil, &metav1.CreateOptions{}); err != nil {
			t.Fatalf("Create with marker=%q returned error: %v", badValue, err)
		}
		assertVisibleAndMarked(t, r, "sg-db")
	}
}

func TestUpdateCannotOverrideMarkerLabel(t *testing.T) {
	r := newTestREST(t, markedPolicy("sg-db"))
	in := &sdnv1alpha1.SecurityGroup{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "sg-db",
			Namespace: testNamespace,
			Labels:    map[string]string{sgLabelKey: "false"},
		},
		Spec: sampleSpec(),
	}
	if _, _, err := r.Update(ctxNS(), "sg-db",
		rest.DefaultUpdatedObjectInfo(in), nil, nil, false, &metav1.UpdateOptions{}); err != nil {
		t.Fatalf("Update returned error: %v", err)
	}
	assertVisibleAndMarked(t, r, "sg-db")
}

func TestUpdateReplacesLabelsAndAnnotations(t *testing.T) {
	r := newTestREST(t)

	// Seed via Create so the backing policy carries a user label and annotation.
	createSG(t, r, &sdnv1alpha1.SecurityGroup{
		ObjectMeta: metav1.ObjectMeta{
			Name:        "sg-db",
			Namespace:   testNamespace,
			Labels:      map[string]string{"team": "data"},
			Annotations: map[string]string{"note": "keep-me-not"},
		},
		Spec: sampleSpec(),
	})

	// Update with the label and annotation removed — Kubernetes replace semantics
	// require them to disappear, not linger from the previous object.
	updated := &sdnv1alpha1.SecurityGroup{
		ObjectMeta: metav1.ObjectMeta{Name: "sg-db", Namespace: testNamespace},
		Spec:       sampleSpec(),
	}
	out, _, err := r.Update(ctxNS(), "sg-db",
		rest.DefaultUpdatedObjectInfo(updated), nil, nil, false, &metav1.UpdateOptions{})
	if err != nil {
		t.Fatalf("Update returned error: %v", err)
	}
	got := out.(*sdnv1alpha1.SecurityGroup)
	if _, ok := got.Labels["team"]; ok {
		t.Fatalf("Update kept a removed label in the view: %v", got.Labels)
	}
	if _, ok := got.Annotations["note"]; ok {
		t.Fatalf("Update kept a removed annotation in the view: %v", got.Annotations)
	}

	// The backing policy must also drop them while keeping the marker.
	np := &CiliumNetworkPolicy{}
	if err := r.c.Get(context.Background(), types.NamespacedName{Namespace: testNamespace, Name: "sg-db"}, np); err != nil {
		t.Fatalf("backing policy not found: %v", err)
	}
	if _, ok := np.Labels["team"]; ok {
		t.Fatalf("backing policy kept a removed label: %v", np.Labels)
	}
	if _, ok := np.Annotations["note"]; ok {
		t.Fatalf("backing policy kept a removed annotation: %v", np.Annotations)
	}
	if np.Labels[sgLabelKey] != sgLabelValue {
		t.Fatalf("backing policy lost the marker label: %v", np.Labels)
	}
}

// ctxAllNS mimics a cluster-wide request (kubectl get/watch -A): the namespace
// in context is empty.
func ctxAllNS() context.Context {
	return request.WithNamespace(context.Background(), metav1.NamespaceAll)
}

func markedPolicyIn(name, namespace string) *CiliumNetworkPolicy {
	return &CiliumNetworkPolicy{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
			Labels:    map[string]string{sgLabelKey: sgLabelValue},
		},
	}
}

func TestListClusterWide(t *testing.T) {
	r := newTestREST(t,
		markedPolicyIn("sg-a", "tenant-a"),
		markedPolicyIn("sg-b", "tenant-b"),
	)
	out, err := r.List(ctxAllNS(), &metainternal.ListOptions{})
	if err != nil {
		t.Fatalf("cluster-wide List returned error: %v", err)
	}
	list := out.(*sdnv1alpha1.SecurityGroupList)
	if len(list.Items) != 2 {
		names := make([]string, len(list.Items))
		for i, it := range list.Items {
			names[i] = it.Namespace + "/" + it.Name
		}
		t.Fatalf("cluster-wide List returned %v, want 2 items across namespaces", names)
	}
}

func TestUpdateForceCreatePropagatesDryRun(t *testing.T) {
	var captured []string
	scheme := runtime.NewScheme()
	if err := AddToScheme(scheme); err != nil {
		t.Fatalf("add cilium mirror to scheme: %v", err)
	}
	fc := fake.NewClientBuilder().
		WithScheme(scheme).
		WithInterceptorFuncs(interceptor.Funcs{
			// Capture the create options the force-create path passes through.
			Create: func(_ context.Context, _ client.WithWatch, _ client.Object, opts ...client.CreateOption) error {
				for _, o := range opts {
					if co, ok := o.(*client.CreateOptions); ok && co.Raw != nil {
						captured = co.Raw.DryRun
					}
				}
				return nil
			},
		}).
		Build()
	r := &REST{c: fc, w: fc, gvr: schema.GroupVersionResource{
		Group: sdnv1alpha1.GroupName, Version: "v1alpha1", Resource: sdnv1alpha1.SecurityGroupPluralName,
	}}

	in := &sdnv1alpha1.SecurityGroup{
		ObjectMeta: metav1.ObjectMeta{Name: "sg-db", Namespace: testNamespace},
		Spec:       sampleSpec(),
	}
	if _, _, err := r.Update(ctxNS(), "sg-db",
		rest.DefaultUpdatedObjectInfo(in), nil, nil, true,
		&metav1.UpdateOptions{DryRun: []string{metav1.DryRunAll}}); err != nil {
		t.Fatalf("Update force-create returned error: %v", err)
	}
	if len(captured) != 1 || captured[0] != metav1.DryRunAll {
		t.Fatalf("force-create dropped the dry-run intent: got %v, want [%s]", captured, metav1.DryRunAll)
	}
}

func TestOwnerReferencesAndFinalizersRoundTrip(t *testing.T) {
	r := newTestREST(t)
	in := &sdnv1alpha1.SecurityGroup{
		ObjectMeta: metav1.ObjectMeta{
			Name:            "sg-db",
			Namespace:       testNamespace,
			OwnerReferences: []metav1.OwnerReference{{APIVersion: "v1", Kind: "ConfigMap", Name: "owner", UID: "owner-uid"}},
			Finalizers:      []string{"sdn.cozystack.io/test"},
		},
		Spec: sampleSpec(),
	}
	createSG(t, r, in)

	out, err := r.Get(ctxNS(), "sg-db", &metav1.GetOptions{})
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	got := out.(*sdnv1alpha1.SecurityGroup)
	if len(got.OwnerReferences) != 1 || got.OwnerReferences[0].Name != "owner" {
		t.Fatalf("ownerReferences not projected to the view: %+v", got.OwnerReferences)
	}
	if len(got.Finalizers) != 1 || got.Finalizers[0] != "sdn.cozystack.io/test" {
		t.Fatalf("finalizers not projected to the view: %+v", got.Finalizers)
	}

	np := &CiliumNetworkPolicy{}
	if err := r.c.Get(context.Background(), types.NamespacedName{Namespace: testNamespace, Name: "sg-db"}, np); err != nil {
		t.Fatalf("backing policy not found: %v", err)
	}
	if len(np.OwnerReferences) != 1 || len(np.Finalizers) != 1 {
		t.Fatalf("backing policy missing ownerReferences/finalizers: %+v / %+v", np.OwnerReferences, np.Finalizers)
	}
}

func TestUpdatePreservesMembershipFinalizer(t *testing.T) {
	// The securitygroup-controller adds the membership finalizer to the backing
	// policy via a merge patch, so it is not in the tenant-facing request body. A
	// full-replace update (kubectl replace) whose body omits finalizers must not
	// strip it — otherwise a delete-before-reconcile would hard-delete the policy
	// and orphan the membership labels on member pods.
	cnp := markedPolicy("sg-db")
	cnp.Finalizers = []string{sdnv1alpha1.MembershipFinalizer}
	r := newTestREST(t, cnp)

	updated := &sdnv1alpha1.SecurityGroup{
		ObjectMeta: metav1.ObjectMeta{Name: "sg-db", Namespace: testNamespace},
		Spec:       sampleSpec(),
	}
	if _, _, err := r.Update(ctxNS(), "sg-db",
		rest.DefaultUpdatedObjectInfo(updated), nil, nil, false, &metav1.UpdateOptions{}); err != nil {
		t.Fatalf("Update returned error: %v", err)
	}

	np := &CiliumNetworkPolicy{}
	if err := r.c.Get(context.Background(), types.NamespacedName{Namespace: testNamespace, Name: "sg-db"}, np); err != nil {
		t.Fatalf("backing policy not found: %v", err)
	}
	found := false
	for _, f := range np.Finalizers {
		if f == sdnv1alpha1.MembershipFinalizer {
			found = true
		}
	}
	if !found {
		t.Fatalf("a full-replace update stripped the membership finalizer: %+v", np.Finalizers)
	}
}

func TestUpdateDuringDeletionPreservesMembershipFinalizer(t *testing.T) {
	// A tenant has full verbs on securitygroups, so they can delete then PUT the
	// still-terminating object with a finalizer-less body. Removing the membership
	// finalizer is exclusively the controller's job; a tenant write must never
	// strip it, even while the object is terminating — otherwise the backing
	// policy is hard-deleted before member-pod labels are cleaned up.
	cnp := markedPolicy("sg-db")
	cnp.Finalizers = []string{sdnv1alpha1.MembershipFinalizer}
	r := newTestREST(t, cnp)

	// Transition the backing policy to terminating (the finalizer keeps it).
	live := &CiliumNetworkPolicy{}
	if err := r.c.Get(context.Background(), types.NamespacedName{Namespace: testNamespace, Name: "sg-db"}, live); err != nil {
		t.Fatalf("Get: %v", err)
	}
	if err := r.c.Delete(context.Background(), live); err != nil {
		t.Fatalf("Delete: %v", err)
	}

	updated := &sdnv1alpha1.SecurityGroup{
		ObjectMeta: metav1.ObjectMeta{Name: "sg-db", Namespace: testNamespace},
		Spec:       sampleSpec(),
	}
	if _, _, err := r.Update(ctxNS(), "sg-db",
		rest.DefaultUpdatedObjectInfo(updated), nil, nil, false, &metav1.UpdateOptions{}); err != nil {
		t.Fatalf("Update returned error: %v", err)
	}

	np := &CiliumNetworkPolicy{}
	if err := r.c.Get(context.Background(), types.NamespacedName{Namespace: testNamespace, Name: "sg-db"}, np); err != nil {
		t.Fatalf("backing policy not found — was it hard-deleted? %v", err)
	}
	if !hasFinalizer(np.Finalizers, sdnv1alpha1.MembershipFinalizer) {
		t.Fatalf("a tenant update during deletion stripped the membership finalizer: %+v", np.Finalizers)
	}
}

func TestWatchForwardsSendInitialEvents(t *testing.T) {
	var captured *metav1.ListOptions
	scheme := runtime.NewScheme()
	if err := AddToScheme(scheme); err != nil {
		t.Fatalf("add cilium mirror to scheme: %v", err)
	}
	fc := fake.NewClientBuilder().
		WithScheme(scheme).
		WithInterceptorFuncs(interceptor.Funcs{
			// Capture the options forwarded to the backing watch; the backing API
			// server (not the fake client) does the initial-events replay, so the
			// unit test only verifies the request is propagated.
			Watch: func(_ context.Context, _ client.WithWatch, _ client.ObjectList, opts ...client.ListOption) (watch.Interface, error) {
				for _, o := range opts {
					if lo, ok := o.(*client.ListOptions); ok && lo.Raw != nil {
						captured = lo.Raw
					}
				}
				return watch.NewEmptyWatch(), nil
			},
		}).
		Build()
	r := &REST{c: fc, w: fc, gvr: schema.GroupVersionResource{
		Group: sdnv1alpha1.GroupName, Version: "v1alpha1", Resource: sdnv1alpha1.SecurityGroupPluralName,
	}}

	sendInitialEvents := true
	w, err := r.Watch(ctxNS(), &metainternal.ListOptions{
		SendInitialEvents:    &sendInitialEvents,
		ResourceVersionMatch: metav1.ResourceVersionMatchNotOlderThan,
	})
	if err != nil {
		t.Fatalf("Watch returned error: %v", err)
	}
	defer w.Stop()

	if captured == nil || captured.SendInitialEvents == nil || !*captured.SendInitialEvents {
		t.Fatalf("Watch did not forward SendInitialEvents to the backing watch: %+v", captured)
	}
	if captured.ResourceVersionMatch != metav1.ResourceVersionMatchNotOlderThan {
		t.Fatalf("Watch did not forward ResourceVersionMatch: got %q", captured.ResourceVersionMatch)
	}
}

func TestUpdateHonorsResourceVersion(t *testing.T) {
	r := newTestREST(t, markedPolicy("sg-db"))

	cur, err := r.Get(ctxNS(), "sg-db", &metav1.GetOptions{})
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	curRV := cur.(*sdnv1alpha1.SecurityGroup).ResourceVersion
	if curRV == "" {
		t.Fatalf("backing policy has no resourceVersion to test against")
	}

	// A stale (non-matching) resourceVersion must surface as a Conflict, not a
	// silent last-write-wins.
	stale := &sdnv1alpha1.SecurityGroup{
		ObjectMeta: metav1.ObjectMeta{Name: "sg-db", Namespace: testNamespace, ResourceVersion: curRV + "0"},
		Spec:       sampleSpec(),
	}
	if _, _, err := r.Update(ctxNS(), "sg-db",
		rest.DefaultUpdatedObjectInfo(stale), nil, nil, false, &metav1.UpdateOptions{}); !apierrors.IsConflict(err) {
		t.Fatalf("stale-resourceVersion update: got err %v, want Conflict", err)
	}

	// An empty resourceVersion must fall back to the current one and succeed.
	fresh := &sdnv1alpha1.SecurityGroup{
		ObjectMeta: metav1.ObjectMeta{Name: "sg-db", Namespace: testNamespace},
		Spec:       sampleSpec(),
	}
	if _, _, err := r.Update(ctxNS(), "sg-db",
		rest.DefaultUpdatedObjectInfo(fresh), nil, nil, false, &metav1.UpdateOptions{}); err != nil {
		t.Fatalf("empty-resourceVersion update should fall back and succeed, got: %v", err)
	}
}

func sgWithIngress(name string, in sdnv1alpha1.IngressRule) *sdnv1alpha1.SecurityGroup {
	return &sdnv1alpha1.SecurityGroup{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: testNamespace},
		Spec: sdnv1alpha1.SecurityGroupSpec{
			Attachments: []sdnv1alpha1.ApplicationReference{{Kind: "Postgres", Name: "x"}},
			Ingress:     []sdnv1alpha1.IngressRule{in},
		},
	}
}

func TestCreateAcceptsEmptyAttachments(t *testing.T) {
	r := newTestREST(t)
	// A group with no attachments selects no pods yet (members get added once it
	// is attached); it is a valid resource, not an error.
	in := &sdnv1alpha1.SecurityGroup{
		ObjectMeta: metav1.ObjectMeta{Name: "sg-empty", Namespace: testNamespace},
		Spec: sdnv1alpha1.SecurityGroupSpec{
			Ingress: []sdnv1alpha1.IngressRule{{ToPorts: []sdnv1alpha1.PortRule{{Ports: []sdnv1alpha1.PortProtocol{{Port: "443", Protocol: "TCP"}}}}}},
		},
	}
	got := createSG(t, r, in)
	if len(got.Spec.Attachments) != 0 {
		t.Fatalf("empty attachments should round-trip empty: %+v", got.Spec.Attachments)
	}
	// The backing policy still carries the membership endpointSelector and omits
	// the attachments annotation entirely.
	np := &CiliumNetworkPolicy{}
	if err := r.c.Get(context.Background(), types.NamespacedName{Namespace: testNamespace, Name: "sg-empty"}, np); err != nil {
		t.Fatalf("backing policy not found: %v", err)
	}
	if _, ok := np.Annotations[attachmentsAnnotation]; ok {
		t.Fatalf("empty attachments should not write an annotation: %v", np.Annotations)
	}
	if np.Spec.EndpointSelector.MatchLabels["securitygroup.sdn.cozystack.io/sg-empty"] != "" {
		t.Fatalf("membership selector missing for empty-attachment group: %+v", np.Spec.EndpointSelector)
	}
}

func TestCreateRejectsInvalidAttachmentLabelValue(t *testing.T) {
	cases := map[string]sdnv1alpha1.ApplicationReference{
		"name too long":     {Kind: "Postgres", Name: strings.Repeat("a", 64)},
		"kind too long":     {Kind: strings.Repeat("a", 64), Name: "db"},
		"name bad chars":    {Kind: "Postgres", Name: "db/inst"},
		"apiGroup too long": {APIGroup: strings.Repeat("a", 64), Kind: "Postgres", Name: "db"},
		"missing kind":      {Name: "db"},
		"missing name":      {Kind: "Postgres"},
		"reserved entity":   {Kind: "Postgres", Name: "world"},
	}
	for name, ref := range cases {
		t.Run(name, func(t *testing.T) {
			r := newTestREST(t)
			in := &sdnv1alpha1.SecurityGroup{
				ObjectMeta: metav1.ObjectMeta{Name: "sg-bad-ref", Namespace: testNamespace},
				Spec:       sdnv1alpha1.SecurityGroupSpec{Attachments: []sdnv1alpha1.ApplicationReference{ref}},
			}
			if _, err := r.Create(ctxNS(), in, nil, &metav1.CreateOptions{}); !apierrors.IsInvalid(err) {
				t.Fatalf("Create with %s: got err %v, want Invalid", name, err)
			}
			np := &CiliumNetworkPolicy{}
			if err := r.c.Get(context.Background(), types.NamespacedName{Namespace: testNamespace, Name: "sg-bad-ref"}, np); !apierrors.IsNotFound(err) {
				t.Fatalf("invalid SecurityGroup was persisted: err=%v", err)
			}
		})
	}
}

func TestCreateRejectsInvalidOwnName(t *testing.T) {
	// The SecurityGroup name is projected into the backing policy's membership
	// label key, so a name that is a valid resource name but an invalid label key
	// (e.g. > 63 chars) must be rejected with a clean Invalid on metadata.name,
	// not a confusing failure on the backing CiliumNetworkPolicy write.
	r := newTestREST(t)
	longName := strings.Repeat("a", 64)
	in := &sdnv1alpha1.SecurityGroup{
		ObjectMeta: metav1.ObjectMeta{Name: longName, Namespace: testNamespace},
		Spec:       sdnv1alpha1.SecurityGroupSpec{Attachments: []sdnv1alpha1.ApplicationReference{{Kind: "Postgres", Name: "db"}}},
	}
	if _, err := r.Create(ctxNS(), in, nil, &metav1.CreateOptions{}); !apierrors.IsInvalid(err) {
		t.Fatalf("Create with an over-long name: got err %v, want Invalid", err)
	}
	np := &CiliumNetworkPolicy{}
	if err := r.c.Get(context.Background(), types.NamespacedName{Namespace: testNamespace, Name: longName}, np); !apierrors.IsNotFound(err) {
		t.Fatalf("invalid-named SecurityGroup was persisted: err=%v", err)
	}
}

func TestCreateRejectsInvalidSGPeer(t *testing.T) {
	cases := map[string]sdnv1alpha1.IngressRule{
		"empty fromSG name": {FromSG: []string{""}},
		"reserved entity":   {FromSG: []string{"cluster"}},
		"over-long name":    {FromSG: []string{strings.Repeat("a", 64)}},
	}
	for name, rule := range cases {
		t.Run(name, func(t *testing.T) {
			r := newTestREST(t)
			if _, err := r.Create(ctxNS(), sgWithIngress("sg-bad-sg", rule), nil, &metav1.CreateOptions{}); !apierrors.IsInvalid(err) {
				t.Fatalf("Create with %s: got err %v, want Invalid", name, err)
			}
		})
	}
}

func TestMultipleAttachmentsRoundTrip(t *testing.T) {
	r := newTestREST(t)
	in := &sdnv1alpha1.SecurityGroup{
		ObjectMeta: metav1.ObjectMeta{Name: "sg-multi", Namespace: testNamespace},
		Spec: sdnv1alpha1.SecurityGroupSpec{
			Attachments: []sdnv1alpha1.ApplicationReference{
				{APIGroup: "apps.cozystack.io", Kind: "Postgres", Name: "db"},
				{APIGroup: "apps.cozystack.io", Kind: "Kubernetes", Name: "web"},
			},
		},
	}
	createSG(t, r, in)
	out, err := r.Get(ctxNS(), "sg-multi", &metav1.GetOptions{})
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if !reflect.DeepEqual(out.(*sdnv1alpha1.SecurityGroup).Spec.Attachments, in.Spec.Attachments) {
		t.Fatalf("multi-attachment round-trip mismatch: %+v", out.(*sdnv1alpha1.SecurityGroup).Spec.Attachments)
	}
}

func TestCreateRejectsInvalidSpec(t *testing.T) {
	cases := map[string]sdnv1alpha1.IngressRule{
		"bad CIDR":     {FromCIDR: []string{"10.0.0.0/33"}},
		"port too big": {ToPorts: []sdnv1alpha1.PortRule{{Ports: []sdnv1alpha1.PortProtocol{{Port: "70000"}}}}},
		"port zero":    {ToPorts: []sdnv1alpha1.PortRule{{Ports: []sdnv1alpha1.PortProtocol{{Port: "0"}}}}},
		"bad protocol": {ToPorts: []sdnv1alpha1.PortRule{{Ports: []sdnv1alpha1.PortProtocol{{Port: "443", Protocol: "ICMP"}}}}},
	}
	for name, rule := range cases {
		t.Run(name, func(t *testing.T) {
			r := newTestREST(t)
			_, err := r.Create(ctxNS(), sgWithIngress("sg-bad", rule), nil, &metav1.CreateOptions{})
			if !apierrors.IsInvalid(err) {
				t.Fatalf("Create with %s: got err %v, want Invalid", name, err)
			}
			// Nothing must be persisted.
			np := &CiliumNetworkPolicy{}
			if err := r.c.Get(context.Background(), types.NamespacedName{Namespace: testNamespace, Name: "sg-bad"}, np); !apierrors.IsNotFound(err) {
				t.Fatalf("invalid SecurityGroup was persisted: err=%v", err)
			}
		})
	}
}

// TestCreateAcceptsBareIPCIDR asserts a bare IP (no prefix) is accepted in
// fromCIDR/toCIDR and projected 1:1. Cilium's CIDR.sanitize treats a bare IP as
// a single-host CIDR, so rejecting it here would make the validator stricter
// than Cilium and block a legitimate single-host rule.
func TestCreateAcceptsBareIPCIDR(t *testing.T) {
	r := newTestREST(t)
	in := &sdnv1alpha1.SecurityGroup{
		ObjectMeta: metav1.ObjectMeta{Name: "sg-host", Namespace: testNamespace},
		Spec: sdnv1alpha1.SecurityGroupSpec{
			Attachments: []sdnv1alpha1.ApplicationReference{{Kind: "Postgres", Name: "x"}},
			Ingress:     []sdnv1alpha1.IngressRule{{FromCIDR: []string{"10.0.0.5"}}},
			Egress:      []sdnv1alpha1.EgressRule{{ToCIDR: []string{"2001:db8::1"}}},
		},
	}
	if _, err := r.Create(ctxNS(), in, nil, &metav1.CreateOptions{}); err != nil {
		t.Fatalf("Create with bare-IP CIDRs returned error: %v", err)
	}
	np := &CiliumNetworkPolicy{}
	if err := r.c.Get(context.Background(), types.NamespacedName{Namespace: testNamespace, Name: "sg-host"}, np); err != nil {
		t.Fatalf("backing policy not found: %v", err)
	}
	if got := np.Spec.Ingress[0].FromCIDR; len(got) != 1 || got[0] != "10.0.0.5" {
		t.Fatalf("fromCIDR not projected 1:1: got %v", got)
	}
	if got := np.Spec.Egress[0].ToCIDR; len(got) != 1 || got[0] != "2001:db8::1" {
		t.Fatalf("toCIDR not projected 1:1: got %v", got)
	}
}

func TestCreateNormalizesProtocolToUpper(t *testing.T) {
	r := newTestREST(t)
	// Numeric port, named port, and a lowercase protocol must all be accepted,
	// and the lowercase protocol must be upper-cased on the backing policy — the
	// Cilium CRD enforces a strict upper-case enum, so a raw "tcp" would 422.
	in := sdnv1alpha1.IngressRule{
		FromCIDR: []string{"10.0.0.0/8", "2001:db8::/32"},
		ToPorts: []sdnv1alpha1.PortRule{{Ports: []sdnv1alpha1.PortProtocol{
			{Port: "5432", Protocol: "tcp"},
			{Port: "https"},
			{Protocol: "ANY"},
		}}},
	}
	if _, err := r.Create(ctxNS(), sgWithIngress("sg-ok", in), nil, &metav1.CreateOptions{}); err != nil {
		t.Fatalf("Create with a valid spec returned error: %v", err)
	}

	np := &CiliumNetworkPolicy{}
	if err := r.c.Get(context.Background(), types.NamespacedName{Namespace: testNamespace, Name: "sg-ok"}, np); err != nil {
		t.Fatalf("backing policy not found: %v", err)
	}
	if got := np.Spec.Ingress[0].ToPorts[0].Ports[0].Protocol; got != "TCP" {
		t.Fatalf("backing policy protocol not normalized: got %q, want TCP", got)
	}
}

func TestUpdateNormalizesProtocolToUpper(t *testing.T) {
	r := newTestREST(t, markedPolicy("sg-db"))
	in := sgWithIngress("sg-db", sdnv1alpha1.IngressRule{
		ToPorts: []sdnv1alpha1.PortRule{{Ports: []sdnv1alpha1.PortProtocol{{Port: "53", Protocol: "udp"}}}},
	})
	if _, _, err := r.Update(ctxNS(), "sg-db",
		rest.DefaultUpdatedObjectInfo(in), nil, nil, false, &metav1.UpdateOptions{}); err != nil {
		t.Fatalf("Update returned error: %v", err)
	}
	np := &CiliumNetworkPolicy{}
	if err := r.c.Get(context.Background(), types.NamespacedName{Namespace: testNamespace, Name: "sg-db"}, np); err != nil {
		t.Fatalf("backing policy not found: %v", err)
	}
	if got := np.Spec.Ingress[0].ToPorts[0].Ports[0].Protocol; got != "UDP" {
		t.Fatalf("Update did not normalize protocol: got %q, want UDP", got)
	}
}

func TestUpdateRejectsInvalidSpec(t *testing.T) {
	r := newTestREST(t, markedPolicy("sg-db"))
	bad := sgWithIngress("sg-db", sdnv1alpha1.IngressRule{FromCIDR: []string{"not-a-cidr"}})
	if _, _, err := r.Update(ctxNS(), "sg-db",
		rest.DefaultUpdatedObjectInfo(bad), nil, nil, false, &metav1.UpdateOptions{}); !apierrors.IsInvalid(err) {
		t.Fatalf("Update with invalid spec: got err %v, want Invalid", err)
	}
}

func TestCreateRejectsEmptyFQDNSelector(t *testing.T) {
	r := newTestREST(t)
	in := &sdnv1alpha1.SecurityGroup{
		ObjectMeta: metav1.ObjectMeta{Name: "sg-fqdn", Namespace: testNamespace},
		Spec: sdnv1alpha1.SecurityGroupSpec{
			Attachments: []sdnv1alpha1.ApplicationReference{{Kind: "Postgres", Name: "db"}},
			Egress:      []sdnv1alpha1.EgressRule{{ToFQDNs: []sdnv1alpha1.FQDNSelector{{}}}},
		},
	}
	if _, err := r.Create(ctxNS(), in, nil, &metav1.CreateOptions{}); !apierrors.IsInvalid(err) {
		t.Fatalf("Create with empty FQDNSelector: got err %v, want Invalid", err)
	}
}

// TestCreateRejectsNamespaceMismatch asserts Create rejects a SecurityGroup
// whose metadata.namespace differs from the request namespace, so a caller
// cannot post to one namespace and persist into another. The aggregated
// apiserver already enforces this before the storage is reached; binding it in
// the storage too keeps the storage self-defending and pins the contract.
func TestCreateRejectsNamespaceMismatch(t *testing.T) {
	r := newTestREST(t)
	in := &sdnv1alpha1.SecurityGroup{
		ObjectMeta: metav1.ObjectMeta{Name: "sg-db", Namespace: "tenant-victim"},
		Spec:       sampleSpec(),
	}
	if _, err := r.Create(ctxNS(), in, nil, &metav1.CreateOptions{}); !apierrors.IsBadRequest(err) {
		t.Fatalf("Create with mismatched namespace: got err %v, want BadRequest", err)
	}
	// Nothing must have been persisted in either namespace.
	np := &CiliumNetworkPolicy{}
	if err := r.c.Get(context.Background(), types.NamespacedName{Namespace: "tenant-victim", Name: "sg-db"}, np); !apierrors.IsNotFound(err) {
		t.Fatalf("expected no policy in tenant-victim, got err %v", err)
	}
	if err := r.c.Get(context.Background(), types.NamespacedName{Namespace: testNamespace, Name: "sg-db"}, np); !apierrors.IsNotFound(err) {
		t.Fatalf("expected no policy in %s, got err %v", testNamespace, err)
	}
}

// TestUpdateRejectsNameMismatch asserts Update rejects an object whose
// metadata.name differs from the URL name, so a force-create/update cannot
// write a different object than the request target.
func TestUpdateRejectsNameMismatch(t *testing.T) {
	r := newTestREST(t)
	createSG(t, r, &sdnv1alpha1.SecurityGroup{
		ObjectMeta: metav1.ObjectMeta{Name: "sg-db", Namespace: testNamespace},
		Spec:       sampleSpec(),
	})

	mismatched := &sdnv1alpha1.SecurityGroup{
		ObjectMeta: metav1.ObjectMeta{Name: "sg-evil", Namespace: testNamespace},
		Spec:       sampleSpec(),
	}
	_, _, err := r.Update(ctxNS(), "sg-db",
		rest.DefaultUpdatedObjectInfo(mismatched), nil, nil, false, &metav1.UpdateOptions{})
	if !apierrors.IsBadRequest(err) {
		t.Fatalf("Update with mismatched name: got err %v, want BadRequest", err)
	}
	// The off-target name must not have been created.
	np := &CiliumNetworkPolicy{}
	if err := r.c.Get(context.Background(), types.NamespacedName{Namespace: testNamespace, Name: "sg-evil"}, np); !apierrors.IsNotFound(err) {
		t.Fatalf("expected no policy named sg-evil, got err %v", err)
	}
}
