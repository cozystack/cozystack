// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Cozystack Authors.

package securitygroup

import (
	"context"
	"errors"
	"reflect"
	"sort"
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
		EndpointSelector: metav1.LabelSelector{
			MatchLabels: map[string]string{"app": "db"},
		},
		Ingress: []sdnv1alpha1.IngressRule{
			{
				FromEndpoints: []metav1.LabelSelector{
					{MatchLabels: map[string]string{"app": "web"}},
				},
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
	if np.Spec == nil || !reflect.DeepEqual(*np.Spec, sampleSpec()) {
		t.Fatalf("backing policy spec mismatch: %+v", np.Spec)
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
			EndpointSelector: metav1.LabelSelector{MatchLabels: map[string]string{"app": "db"}},
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
			EndpointSelector: metav1.LabelSelector{MatchLabels: map[string]string{"app": "x"}},
			Ingress:          []sdnv1alpha1.IngressRule{in},
		},
	}
}

func TestCreateRejectsEmptyEndpointSelector(t *testing.T) {
	r := newTestREST(t)
	// No endpointSelector — would otherwise project to a namespace-wide policy.
	in := &sdnv1alpha1.SecurityGroup{
		ObjectMeta: metav1.ObjectMeta{Name: "sg-wide", Namespace: testNamespace},
		Spec: sdnv1alpha1.SecurityGroupSpec{
			Ingress: []sdnv1alpha1.IngressRule{{ToPorts: []sdnv1alpha1.PortRule{{Ports: []sdnv1alpha1.PortProtocol{{Port: "443", Protocol: "TCP"}}}}}},
		},
	}
	if _, err := r.Create(ctxNS(), in, nil, &metav1.CreateOptions{}); !apierrors.IsInvalid(err) {
		t.Fatalf("Create with empty endpointSelector: got err %v, want Invalid", err)
	}
	np := &CiliumNetworkPolicy{}
	if err := r.c.Get(context.Background(), types.NamespacedName{Namespace: testNamespace, Name: "sg-wide"}, np); !apierrors.IsNotFound(err) {
		t.Fatalf("namespace-wide SecurityGroup was persisted: err=%v", err)
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
			EndpointSelector: metav1.LabelSelector{MatchLabels: map[string]string{"app": "db"}},
			Egress:           []sdnv1alpha1.EgressRule{{ToFQDNs: []sdnv1alpha1.FQDNSelector{{}}}},
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
