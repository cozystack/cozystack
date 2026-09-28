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

package endpointattachment

import (
	"context"
	"strings"
	"testing"
	"time"

	helmv2 "github.com/fluxcd/helm-controller/api/v2"
	corev1 "k8s.io/api/core/v1"
	discoveryv1 "k8s.io/api/discovery/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	cozyv1alpha1 "github.com/cozystack/cozystack/api/localsdn/v1alpha1"
	appsv1alpha1 "github.com/cozystack/cozystack/pkg/apis/apps/v1alpha1"
	corev1alpha1 "github.com/cozystack/cozystack/pkg/apis/core/v1alpha1"
)

const ns = "tenant-foo"

func newScheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	s := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(s); err != nil {
		t.Fatalf("client-go scheme: %v", err)
	}
	if err := cozyv1alpha1.AddToScheme(s); err != nil {
		t.Fatalf("cozystack scheme: %v", err)
	}
	if err := helmv2.AddToScheme(s); err != nil {
		t.Fatalf("helm scheme: %v", err)
	}
	return s
}

func newClient(t *testing.T, objs ...client.Object) client.Client {
	t.Helper()
	return fake.NewClientBuilder().
		WithScheme(newScheme(t)).
		WithStatusSubresource(&cozyv1alpha1.EndpointAttachment{}).
		WithObjects(objs...).
		Build()
}

func appLabels(kind, name string) map[string]string {
	return map[string]string{
		appsv1alpha1.ApplicationGroupLabel: "apps.cozystack.io",
		appsv1alpha1.ApplicationKindLabel:  kind,
		appsv1alpha1.ApplicationNameLabel:  name,
	}
}

func helmRelease(name, uid, kind, app string) *helmv2.HelmRelease {
	return &helmv2.HelmRelease{ObjectMeta: metav1.ObjectMeta{
		Namespace: ns, Name: name, UID: types.UID(uid), Labels: appLabels(kind, app),
	}}
}

// endpoint builds a Service the way the lineage webhook leaves a tenant-facing
// one: lineage labels plus the tenantresource marker.
func endpoint(name, kind, app string, selector map[string]string, ports ...int32) *corev1.Service {
	labels := appLabels(kind, app)
	labels[corev1alpha1.TenantResourceLabelKey] = corev1alpha1.TenantResourceLabelValue
	svc := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: name, UID: types.UID("svc-" + name), Labels: labels},
		Spec:       corev1.ServiceSpec{Selector: selector},
	}
	for _, p := range ports {
		svc.Spec.Ports = append(svc.Spec.Ports, corev1.ServicePort{Name: "p", Port: p, Protocol: corev1.ProtocolTCP})
	}
	return svc
}

func attachment(name, uid, kind, app, service string, lb cozyv1alpha1.LoadBalancerAttachment) *cozyv1alpha1.EndpointAttachment {
	return &cozyv1alpha1.EndpointAttachment{
		ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: name, UID: types.UID(uid), Generation: 1},
		Spec: cozyv1alpha1.EndpointAttachmentSpec{
			ApplicationRef: cozyv1alpha1.EndpointApplicationReference{Group: "apps.cozystack.io", Kind: kind, Name: app},
			Endpoint:       cozyv1alpha1.EndpointReference{ServiceName: service},
			LoadBalancer:   &lb,
		},
	}
}

func claimObject(name, className, family string, bound bool, addresses ...string) *unstructured.Unstructured {
	u := newClaim()
	u.SetNamespace(ns)
	u.SetName(name)
	spec := map[string]interface{}{}
	if className != "" {
		spec["className"] = className
	}
	if family != "" {
		spec["family"] = family
	}
	u.Object["spec"] = spec
	status := map[string]interface{}{"className": className, "phase": "Pending"}
	if bound {
		status["phase"] = "Bound"
		var list []interface{}
		for _, a := range addresses {
			list = append(list, map[string]interface{}{"name": "ip-" + strings.ReplaceAll(a, ".", "-"), "address": a})
		}
		status["addresses"] = list
	}
	u.Object["status"] = status
	return u
}

func classObject(name, lbClass string) *unstructured.Unstructured {
	u := &unstructured.Unstructured{}
	u.SetGroupVersionKind(classGVK)
	u.SetName(name)
	u.Object["spec"] = map[string]interface{}{"provisioner": "test", "loadBalancerClass": lbClass}
	return u
}

func addressObject(addr string, holder *types.NamespacedName) *unstructured.Unstructured {
	u := &unstructured.Unstructured{}
	u.SetGroupVersionKind(addressGVK)
	u.SetName("ip-" + strings.ReplaceAll(addr, ".", "-"))
	u.Object["spec"] = map[string]interface{}{"address": addr, "className": "public"}
	status := map[string]interface{}{"phase": "Bound"}
	if holder != nil {
		status["associatedTo"] = map[string]interface{}{"kind": "Service", "namespace": holder.Namespace, "name": holder.Name}
	}
	u.Object["status"] = status
	return u
}

func endpointSlice(name, service string, ready bool, pods ...string) *discoveryv1.EndpointSlice {
	s := &discoveryv1.EndpointSlice{
		ObjectMeta:  metav1.ObjectMeta{Namespace: ns, Name: name, Labels: map[string]string{discoveryv1.LabelServiceName: service}},
		AddressType: discoveryv1.AddressTypeIPv4,
	}
	for _, p := range pods {
		s.Endpoints = append(s.Endpoints, discoveryv1.Endpoint{
			Addresses:  []string{"10.0.0.1"},
			Conditions: discoveryv1.EndpointConditions{Ready: new(ready)},
			TargetRef:  &corev1.ObjectReference{Kind: "Pod", Name: p, UID: types.UID("pod-" + p)},
		})
	}
	return s
}

func run(t *testing.T, c client.Client, r *Reconciler, ea *cozyv1alpha1.EndpointAttachment) *cozyv1alpha1.EndpointAttachment {
	t.Helper()
	if r == nil {
		r = &Reconciler{}
	}
	r.Client = c
	if _, err := r.Reconcile(context.TODO(), ctrl.Request{NamespacedName: client.ObjectKeyFromObject(ea)}); err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	got := &cozyv1alpha1.EndpointAttachment{}
	if err := c.Get(context.TODO(), client.ObjectKeyFromObject(ea), got); err != nil {
		t.Fatalf("get attachment: %v", err)
	}
	return got
}

func renderedServices(t *testing.T, c client.Client, ea *cozyv1alpha1.EndpointAttachment) []corev1.Service {
	t.Helper()
	list := &corev1.ServiceList{}
	if err := c.List(context.TODO(), list, client.InNamespace(ns)); err != nil {
		t.Fatalf("list services: %v", err)
	}
	var out []corev1.Service
	for _, s := range list.Items {
		if isControlledBy(&s, ea) {
			out = append(out, s)
		}
	}
	return out
}

func onlyRendered(t *testing.T, c client.Client, ea *cozyv1alpha1.EndpointAttachment) *corev1.Service {
	t.Helper()
	got := renderedServices(t, c, ea)
	if len(got) != 1 {
		t.Fatalf("expected exactly one rendered Service, got %d", len(got))
	}
	return &got[0]
}

func expectCondition(t *testing.T, ea *cozyv1alpha1.EndpointAttachment, condType string, status metav1.ConditionStatus, reason string) {
	t.Helper()
	c := apimeta.FindStatusCondition(ea.Status.Conditions, condType)
	if c == nil {
		t.Fatalf("condition %s missing; status: %+v", condType, ea.Status)
	}
	if c.Status != status || c.Reason != reason {
		t.Fatalf("condition %s = %s/%s (%s), want %s/%s", condType, c.Status, c.Reason, c.Message, status, reason)
	}
}

func setIngress(t *testing.T, c client.Client, svc *corev1.Service, ips ...string) {
	t.Helper()
	live := &corev1.Service{}
	if err := c.Get(context.TODO(), client.ObjectKeyFromObject(svc), live); err != nil {
		t.Fatalf("get service: %v", err)
	}
	live.Status.LoadBalancer.Ingress = nil
	for _, ip := range ips {
		live.Status.LoadBalancer.Ingress = append(live.Status.LoadBalancer.Ingress, corev1.LoadBalancerIngress{IP: ip})
	}
	if err := c.Status().Update(context.TODO(), live); err != nil {
		t.Fatalf("set ingress: %v", err)
	}
}

// TestResolveDenyMatrix pins the authorization seam: both label families are
// required on the endpoint Service, and each failure carries its own reason.
func TestResolveDenyMatrix(t *testing.T) {
	tests := []struct {
		name       string
		mutate     func(*corev1.Service)
		noService  bool
		noRelease  bool
		wantStatus metav1.ConditionStatus
		wantReason string
	}{
		{name: "both families present", wantStatus: metav1.ConditionTrue, wantReason: cozyv1alpha1.ReasonResolved},
		{name: "kind label mismatches", mutate: func(s *corev1.Service) { s.Labels[appsv1alpha1.ApplicationKindLabel] = "MariaDB" },
			wantStatus: metav1.ConditionFalse, wantReason: cozyv1alpha1.ReasonEndpointLineageMismatch},
		{name: "name label mismatches", mutate: func(s *corev1.Service) { s.Labels[appsv1alpha1.ApplicationNameLabel] = "other" },
			wantStatus: metav1.ConditionFalse, wantReason: cozyv1alpha1.ReasonEndpointLineageMismatch},
		{name: "group label mismatches", mutate: func(s *corev1.Service) { s.Labels[appsv1alpha1.ApplicationGroupLabel] = "foreign.example.com" },
			wantStatus: metav1.ConditionFalse, wantReason: cozyv1alpha1.ReasonEndpointLineageMismatch},
		{name: "lineage without marker", mutate: func(s *corev1.Service) { delete(s.Labels, corev1alpha1.TenantResourceLabelKey) },
			wantStatus: metav1.ConditionFalse, wantReason: cozyv1alpha1.ReasonEndpointNotTenantFacing},
		{name: "marker false", mutate: func(s *corev1.Service) { s.Labels[corev1alpha1.TenantResourceLabelKey] = "false" },
			wantStatus: metav1.ConditionFalse, wantReason: cozyv1alpha1.ReasonEndpointNotTenantFacing},
		{name: "service missing", noService: true, wantStatus: metav1.ConditionFalse, wantReason: cozyv1alpha1.ReasonEndpointNotFound},
		{name: "application missing", noRelease: true, wantStatus: metav1.ConditionFalse, wantReason: cozyv1alpha1.ReasonApplicationNotFound},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ea := attachment("ro-public", "ea-1", "Postgres", "mydb", "postgres-mydb-ro", cozyv1alpha1.LoadBalancerAttachment{ClaimName: "held"})
			objs := []client.Object{ea}
			if !tt.noRelease {
				objs = append(objs, helmRelease("postgres-mydb", "hr-1", "Postgres", "mydb"))
			}
			if !tt.noService {
				svc := endpoint("postgres-mydb-ro", "Postgres", "mydb", map[string]string{"role": "replica"}, 5432)
				if tt.mutate != nil {
					tt.mutate(svc)
				}
				objs = append(objs, svc)
			}
			got := run(t, newClient(t, objs...), nil, ea)
			expectCondition(t, got, cozyv1alpha1.ConditionResolved, tt.wantStatus, tt.wantReason)
			if tt.wantStatus == metav1.ConditionFalse && len(renderedServices(t, newClient(t), got)) != 0 {
				t.Fatal("nothing may be rendered for an unresolved endpoint")
			}
		})
	}
}

// TestRenderMirrorsEndpoint walks the resolve → claim → render → report
// pipeline for the flagship database case with a referenced claim.
func TestRenderMirrorsEndpoint(t *testing.T) {
	ep := endpoint("postgres-mydb-ro", "Postgres", "mydb", map[string]string{"cnpg.io/cluster": "postgres-mydb", "role": "replica"}, 5432, 9187)
	ea := attachment("ro-public", "ea-1", "Postgres", "mydb", "postgres-mydb-ro", cozyv1alpha1.LoadBalancerAttachment{ClaimName: "held", Ports: []int32{5432}})
	c := newClient(t,
		helmRelease("postgres-mydb", "hr-1", "Postgres", "mydb"), ep, ea,
		claimObject("held", "public", "IPv4", true, "203.0.113.7"),
		classObject("public", "metallb.io/public"),
		addressObject("203.0.113.7", nil),
	)

	got := run(t, c, nil, ea)
	expectCondition(t, got, cozyv1alpha1.ConditionResolved, metav1.ConditionTrue, cozyv1alpha1.ReasonResolved)
	expectCondition(t, got, cozyv1alpha1.ConditionProvisioned, metav1.ConditionFalse, cozyv1alpha1.ReasonWaitingForAddress)
	if got.Status.Phase != cozyv1alpha1.EndpointAttachmentPending {
		t.Fatalf("phase = %s, want Pending", got.Status.Phase)
	}
	if got.Status.ClaimName != "held" || len(got.Status.Addresses) != 1 || got.Status.Addresses[0] != "203.0.113.7" {
		t.Fatalf("status did not mirror the claim: %+v", got.Status)
	}

	svc := onlyRendered(t, c, ea)
	if !strings.HasPrefix(svc.Name, "ro-public-") || svc.Name == "ro-public-" {
		t.Fatalf("Service must be generateName'd from the attachment, got %q", svc.Name)
	}
	if got.Status.ServiceName != svc.Name {
		t.Fatalf("status.serviceName = %q, want %q", got.Status.ServiceName, svc.Name)
	}
	if svc.Annotations[ClaimAnnotation] != "held" {
		t.Fatalf("claim annotation = %q", svc.Annotations[ClaimAnnotation])
	}
	if svc.Spec.LoadBalancerClass == nil || *svc.Spec.LoadBalancerClass != "metallb.io/public" {
		t.Fatalf("loadBalancerClass must be read from the class before creation, got %v", svc.Spec.LoadBalancerClass)
	}
	if svc.Spec.Type != corev1.ServiceTypeLoadBalancer || svc.Spec.ExternalTrafficPolicy != corev1.ServiceExternalTrafficPolicyLocal {
		t.Fatalf("unexpected type/policy: %s/%s", svc.Spec.Type, svc.Spec.ExternalTrafficPolicy)
	}
	if svc.Spec.AllocateLoadBalancerNodePorts == nil || *svc.Spec.AllocateLoadBalancerNodePorts {
		t.Fatal("node ports must be disabled by default")
	}
	if !equalStringMaps(svc.Spec.Selector, ep.Spec.Selector) {
		t.Fatalf("selector not mirrored: %v", svc.Spec.Selector)
	}
	if len(svc.Spec.Ports) != 1 || svc.Spec.Ports[0].Port != 5432 {
		t.Fatalf("ports must be the endpoint's filtered by spec.ports, got %v", svc.Spec.Ports)
	}
	if _, ok := svc.Labels[ServiceProxyNameLabel]; ok {
		t.Fatal("a plain attachment must not be delegated to the datapath proxy")
	}
	for _, k := range []string{appsv1alpha1.ApplicationKindLabel, corev1alpha1.TenantResourceLabelKey} {
		if _, ok := svc.Labels[k]; ok {
			t.Fatalf("rendered Service must not copy the endpoint's %s label", k)
		}
	}
	if ref := metav1.GetControllerOf(svc); ref == nil || ref.UID != ea.UID {
		t.Fatalf("Service must be controller-owned by the attachment, got %+v", svc.OwnerReferences)
	}

	// The address lands on the Service: Provisioned flips, phase Attached.
	setIngress(t, c, svc, "203.0.113.7")
	got = run(t, c, nil, ea)
	expectCondition(t, got, cozyv1alpha1.ConditionProvisioned, metav1.ConditionTrue, cozyv1alpha1.ReasonProvisioned)
	if got.Status.Phase != cozyv1alpha1.EndpointAttachmentAttached {
		t.Fatalf("phase = %s, want Attached", got.Status.Phase)
	}
	if hr := helmReleaseOwner(got); hr == nil || hr.UID != "hr-1" {
		t.Fatalf("attachment must be owned by the application's HelmRelease, got %+v", got.OwnerReferences)
	}
	if got.Labels[appsv1alpha1.ApplicationNameLabel] != "mydb" {
		t.Fatalf("lineage labels must be stamped on the attachment, got %v", got.Labels)
	}

	// The operator relabels; the mirror follows on the next reconcile.
	live := &corev1.Service{}
	if err := c.Get(context.TODO(), client.ObjectKeyFromObject(ep), live); err != nil {
		t.Fatal(err)
	}
	live.Spec.Selector = map[string]string{"cnpg.io/cluster": "postgres-mydb", "role": "primary"}
	if err := c.Update(context.TODO(), live); err != nil {
		t.Fatal(err)
	}
	run(t, c, nil, ea)
	svc = onlyRendered(t, c, ea)
	if svc.Spec.Selector["role"] != "primary" {
		t.Fatalf("selector drift not mirrored: %v", svc.Spec.Selector)
	}
	if svc.Name != got.Status.ServiceName {
		t.Fatal("re-mirroring must update the Service in place, not replace it")
	}
}

// TestMintedClaim covers the className path: a claim is minted under the
// attachment's control and the Service waits for the class to resolve.
func TestMintedClaim(t *testing.T) {
	ep := endpoint("postgres-mydb-rw", "Postgres", "mydb", map[string]string{"role": "primary"}, 5432)
	ea := attachment("rw-public", "ea-1", "Postgres", "mydb", "postgres-mydb-rw", cozyv1alpha1.LoadBalancerAttachment{Family: cozyv1alpha1.AddressFamilyIPv4})
	c := newClient(t, helmRelease("postgres-mydb", "hr-1", "Postgres", "mydb"), ep, ea, classObject("public", ""))

	got := run(t, c, nil, ea)
	expectCondition(t, got, cozyv1alpha1.ConditionProvisioned, metav1.ConditionFalse, cozyv1alpha1.ReasonClaimPending)
	claims := &unstructured.UnstructuredList{}
	claims.SetGroupVersionKind(claimListGVK)
	if err := c.List(context.TODO(), claims, client.InNamespace(ns)); err != nil {
		t.Fatal(err)
	}
	if len(claims.Items) != 1 {
		t.Fatalf("expected one minted claim, got %d", len(claims.Items))
	}
	minted := claims.Items[0]
	if !isControlledBy(&minted, ea) {
		t.Fatalf("minted claim must be controller-owned by the attachment: %+v", minted.GetOwnerReferences())
	}
	if !strings.HasPrefix(minted.GetName(), "rw-public-") {
		t.Fatalf("minted claim name %q", minted.GetName())
	}
	if fam, _, _ := unstructured.NestedString(minted.Object, "spec", "family"); fam != "IPv4" {
		t.Fatalf("family not passed through: %q", fam)
	}
	if _, has, _ := unstructured.NestedString(minted.Object, "spec", "className"); has {
		t.Fatal("no className means the default class; none must be written")
	}
	if got.Status.ClaimName != minted.GetName() {
		t.Fatalf("status.claimName = %q", got.Status.ClaimName)
	}
	if len(renderedServices(t, c, ea)) != 0 {
		t.Fatal("no Service may exist before the claim's class fixes loadBalancerClass")
	}

	// Reconcile again: the minted claim is rediscovered, not re-minted.
	run(t, c, nil, ea)
	if err := c.List(context.TODO(), claims, client.InNamespace(ns)); err != nil {
		t.Fatal(err)
	}
	if len(claims.Items) != 1 {
		t.Fatalf("claim re-minted: %d claims", len(claims.Items))
	}

	// The substrate resolves the default class and binds.
	live := claims.Items[0]
	live.Object["status"] = map[string]interface{}{
		"className": "public", "phase": "Bound",
		"addresses": []interface{}{map[string]interface{}{"name": "ip-a", "address": "203.0.113.9"}},
	}
	if err := c.Update(context.TODO(), &live); err != nil {
		t.Fatal(err)
	}
	got = run(t, c, nil, ea)
	svc := onlyRendered(t, c, ea)
	if svc.Spec.LoadBalancerClass != nil {
		t.Fatalf("an empty class loadBalancerClass must stay empty on the Service, got %q", *svc.Spec.LoadBalancerClass)
	}
	if svc.Annotations[ClaimAnnotation] != minted.GetName() {
		t.Fatalf("claim annotation = %q", svc.Annotations[ClaimAnnotation])
	}
	expectCondition(t, got, cozyv1alpha1.ConditionProvisioned, metav1.ConditionFalse, cozyv1alpha1.ReasonWaitingForAddress)
}

// TestIdentityLabelledButUnownedServiceIsIgnored is the identity proof: a
// Service carrying the ownership label but no controller reference to the
// attachment is neither adopted nor updated; the controller renders its own.
func TestIdentityLabelledButUnownedServiceIsIgnored(t *testing.T) {
	ep := endpoint("postgres-mydb-ro", "Postgres", "mydb", map[string]string{"role": "replica"}, 5432)
	ea := attachment("ro-public", "ea-1", "Postgres", "mydb", "postgres-mydb-ro", cozyv1alpha1.LoadBalancerAttachment{ClaimName: "held"})
	impostor := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: "ro-public-fake", UID: "svc-fake",
			Labels:      map[string]string{OwnerLabel: ea.Name},
			Annotations: map[string]string{"tenant": "wrote-this"}},
		Spec: corev1.ServiceSpec{Type: corev1.ServiceTypeClusterIP, Selector: map[string]string{"app": "evil"},
			Ports: []corev1.ServicePort{{Port: 80}}},
	}
	strayOwner := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: "ro-public-stale", UID: "svc-stale",
			Labels: map[string]string{OwnerLabel: ea.Name},
			OwnerReferences: []metav1.OwnerReference{{
				APIVersion: cozyv1alpha1.GroupVersion.String(), Kind: "EndpointAttachment",
				Name: ea.Name, UID: "ea-previous-incarnation", Controller: new(true),
			}}},
		Spec: corev1.ServiceSpec{Type: corev1.ServiceTypeLoadBalancer, Selector: map[string]string{"app": "stale"},
			Ports: []corev1.ServicePort{{Port: 5432}}},
	}
	c := newClient(t, helmRelease("postgres-mydb", "hr-1", "Postgres", "mydb"), ep, ea, impostor, strayOwner,
		claimObject("held", "public", "IPv4", true, "203.0.113.7"), classObject("public", ""), addressObject("203.0.113.7", nil))

	run(t, c, nil, ea)

	own := onlyRendered(t, c, ea)
	if own.Name == impostor.Name || own.Name == strayOwner.Name {
		t.Fatalf("controller adopted %q instead of rendering its own Service", own.Name)
	}
	for _, name := range []string{impostor.Name, strayOwner.Name} {
		live := &corev1.Service{}
		if err := c.Get(context.TODO(), types.NamespacedName{Namespace: ns, Name: name}, live); err != nil {
			t.Fatalf("%s must be left alone, got %v", name, err)
		}
		if live.Spec.Selector["app"] == "" || live.Annotations[ClaimAnnotation] != "" {
			t.Fatalf("%s was modified: %+v", name, live.Spec)
		}
	}
}

// TestIdentitySameNameRecreatedApplication: the attachment is bound to one
// application incarnation by UID. A same-name successor is not re-adopted;
// the attachment reports the change, withdraws, and waits for GC.
func TestIdentitySameNameRecreatedApplication(t *testing.T) {
	ep := endpoint("postgres-mydb-ro", "Postgres", "mydb", map[string]string{"role": "replica"}, 5432)
	ea := attachment("ro-public", "ea-1", "Postgres", "mydb", "postgres-mydb-ro", cozyv1alpha1.LoadBalancerAttachment{ClaimName: "held"})
	c := newClient(t, helmRelease("postgres-mydb", "hr-old", "Postgres", "mydb"), ep, ea,
		claimObject("held", "public", "IPv4", true, "203.0.113.7"), classObject("public", ""), addressObject("203.0.113.7", nil))
	got := run(t, c, nil, ea)
	if helmReleaseOwner(got).UID != "hr-old" {
		t.Fatalf("owner = %+v", got.OwnerReferences)
	}
	rendered := onlyRendered(t, c, ea)

	// The application is deleted and recreated under the same name.
	old := &helmv2.HelmRelease{}
	if err := c.Get(context.TODO(), types.NamespacedName{Namespace: ns, Name: "postgres-mydb"}, old); err != nil {
		t.Fatal(err)
	}
	if err := c.Delete(context.TODO(), old); err != nil {
		t.Fatal(err)
	}
	if err := c.Create(context.TODO(), helmRelease("postgres-mydb", "hr-new", "Postgres", "mydb")); err != nil {
		t.Fatal(err)
	}

	got = run(t, c, nil, ea)
	expectCondition(t, got, cozyv1alpha1.ConditionResolved, metav1.ConditionFalse, cozyv1alpha1.ReasonApplicationIncarnationChange)
	if helmReleaseOwner(got).UID != "hr-old" {
		t.Fatalf("owner reference must never be rewritten to the successor, got %+v", got.OwnerReferences)
	}
	if err := c.Get(context.TODO(), client.ObjectKeyFromObject(rendered), &corev1.Service{}); !apierrors.IsNotFound(err) {
		t.Fatalf("rendered Service must be withdrawn from the successor, got %v", err)
	}
	if got.Status.Phase != cozyv1alpha1.EndpointAttachmentDetached {
		t.Fatalf("phase = %s, want Detached", got.Status.Phase)
	}
}

// TestHoldAndMove is the proposal's contract in one test: a reserved claim
// is attached, the attachment goes away, the claim is untouched and still
// holds its address, and a second attachment on a different application's
// endpoint serves the same address.
func TestHoldAndMove(t *testing.T) {
	epA := endpoint("postgres-a-rw", "Postgres", "a", map[string]string{"cluster": "a"}, 5432)
	epB := endpoint("mariadb-b-primary", "MariaDB", "b", map[string]string{"cluster": "b"}, 3306)
	ea1 := attachment("a-public", "ea-1", "Postgres", "a", "postgres-a-rw", cozyv1alpha1.LoadBalancerAttachment{ClaimName: "held"})
	c := newClient(t,
		helmRelease("postgres-a", "hr-a", "Postgres", "a"), helmRelease("mariadb-b", "hr-b", "MariaDB", "b"),
		epA, epB, ea1,
		claimObject("held", "public", "IPv4", true, "203.0.113.7"), classObject("public", ""), addressObject("203.0.113.7", nil))

	run(t, c, nil, ea1)
	s1 := onlyRendered(t, c, ea1)
	setIngress(t, c, s1, "203.0.113.7")
	got := run(t, c, nil, ea1)
	expectCondition(t, got, cozyv1alpha1.ConditionProvisioned, metav1.ConditionTrue, cozyv1alpha1.ReasonProvisioned)

	// The substrate associates the address to S1. A second attachment on the
	// same claim is refused, not fought over.
	addr := addressObject("203.0.113.7", &types.NamespacedName{Namespace: ns, Name: s1.Name})
	live := &unstructured.Unstructured{}
	live.SetGroupVersionKind(addressGVK)
	if err := c.Get(context.TODO(), client.ObjectKeyFromObject(addr), live); err != nil {
		t.Fatal(err)
	}
	live.Object["status"] = addr.Object["status"]
	if err := c.Update(context.TODO(), live); err != nil {
		t.Fatal(err)
	}
	ea2 := attachment("b-public", "ea-2", "MariaDB", "b", "mariadb-b-primary", cozyv1alpha1.LoadBalancerAttachment{ClaimName: "held"})
	if err := c.Create(context.TODO(), ea2); err != nil {
		t.Fatal(err)
	}
	got2 := run(t, c, nil, ea2)
	expectCondition(t, got2, cozyv1alpha1.ConditionProvisioned, metav1.ConditionFalse, cozyv1alpha1.ReasonClaimInUse)
	if len(renderedServices(t, c, ea2)) != 0 {
		t.Fatal("a claim worn by another Service must not be contended")
	}

	// Detach: the first attachment is deleted (its Service goes with it by
	// GC, simulated here). The claim was never owned or written.
	if err := c.Delete(context.TODO(), got); err != nil {
		t.Fatal(err)
	}
	if err := c.Delete(context.TODO(), s1); err != nil {
		t.Fatal(err)
	}
	held := newClaim()
	if err := c.Get(context.TODO(), types.NamespacedName{Namespace: ns, Name: "held"}, held); err != nil {
		t.Fatalf("referenced claim must survive detachment: %v", err)
	}
	if len(held.GetOwnerReferences()) != 0 {
		t.Fatalf("referenced claim must never be owned: %+v", held.GetOwnerReferences())
	}
	if v := parseClaim(held); !v.Bound || v.Addresses[0].Address != "203.0.113.7" {
		t.Fatalf("claim must still hold its address: %+v", v)
	}

	// Move: the holder is gone, so the association record is stale and the
	// second attachment takes the address to a different application.
	got2 = run(t, c, nil, ea2)
	s2 := onlyRendered(t, c, ea2)
	if s2.Annotations[ClaimAnnotation] != "held" || !equalStringMaps(s2.Spec.Selector, epB.Spec.Selector) {
		t.Fatalf("second attachment must consume the same claim on the new target: %+v", s2)
	}
	setIngress(t, c, s2, "203.0.113.7")
	got2 = run(t, c, nil, ea2)
	expectCondition(t, got2, cozyv1alpha1.ConditionProvisioned, metav1.ConditionTrue, cozyv1alpha1.ReasonProvisioned)
	if got2.Status.Addresses[0] != "203.0.113.7" {
		t.Fatalf("same address must serve the new target, got %v", got2.Status.Addresses)
	}
}

// TestDetachKeepsAddress: an endpoint that disappears (a scale-down) turns
// the attachment Detached, withdraws the Service, keeps the claim, and the
// same claim is reused when the endpoint returns.
func TestDetachKeepsAddress(t *testing.T) {
	ep := endpoint("mariadb-x-secondary", "MariaDB", "x", map[string]string{"role": "secondary"}, 3306)
	ea := attachment("sec", "ea-1", "MariaDB", "x", "mariadb-x-secondary", cozyv1alpha1.LoadBalancerAttachment{ClassName: "public"})
	c := newClient(t, helmRelease("mariadb-x", "hr-1", "MariaDB", "x"), ep, ea, classObject("public", ""))
	got := run(t, c, nil, ea)
	minted := got.Status.ClaimName
	claim := newClaim()
	if err := c.Get(context.TODO(), types.NamespacedName{Namespace: ns, Name: minted}, claim); err != nil {
		t.Fatal(err)
	}
	claim.Object["status"] = map[string]interface{}{"className": "public", "phase": "Bound",
		"addresses": []interface{}{map[string]interface{}{"name": "ip-x", "address": "203.0.113.20"}}}
	if err := c.Update(context.TODO(), claim); err != nil {
		t.Fatal(err)
	}
	run(t, c, nil, ea)
	svc := onlyRendered(t, c, ea)

	if err := c.Delete(context.TODO(), ep); err != nil {
		t.Fatal(err)
	}
	got = run(t, c, nil, ea)
	if got.Status.Phase != cozyv1alpha1.EndpointAttachmentDetached {
		t.Fatalf("phase = %s, want Detached (%+v)", got.Status.Phase, got.Status.Conditions)
	}
	expectCondition(t, got, cozyv1alpha1.ConditionProvisioned, metav1.ConditionFalse, cozyv1alpha1.ReasonEndpointDetached)
	if err := c.Get(context.TODO(), client.ObjectKeyFromObject(svc), &corev1.Service{}); !apierrors.IsNotFound(err) {
		t.Fatalf("Service must be torn down on detach, got %v", err)
	}
	if err := c.Get(context.TODO(), types.NamespacedName{Namespace: ns, Name: minted}, newClaim()); err != nil {
		t.Fatalf("minted claim must be kept across a detach: %v", err)
	}
	if got.Status.ClaimName != minted || got.Status.ServiceName != "" {
		t.Fatalf("status after detach: %+v", got.Status)
	}

	if err := c.Create(context.TODO(), endpoint("mariadb-x-secondary", "MariaDB", "x", map[string]string{"role": "secondary"}, 3306)); err != nil {
		t.Fatal(err)
	}
	got = run(t, c, nil, ea)
	again := onlyRendered(t, c, ea)
	if again.Annotations[ClaimAnnotation] != minted {
		t.Fatalf("reattachment must reuse the held claim, got %q", again.Annotations[ClaimAnnotation])
	}
	if got.Status.Phase != cozyv1alpha1.EndpointAttachmentPending {
		t.Fatalf("phase = %s, want Pending until the address lands", got.Status.Phase)
	}
}

func TestSentinelEndpointNeedsPortsOrMethod(t *testing.T) {
	vm := endpoint("vm-instance-box", "VMInstance", "box", map[string]string{"vm": "box"}, SentinelPort)
	ea := attachment("box-ip", "ea-1", "VMInstance", "box", "vm-instance-box", cozyv1alpha1.LoadBalancerAttachment{ClaimName: "held"})
	c := newClient(t, helmRelease("vm-instance-box", "hr-1", "VMInstance", "box"), vm, ea,
		claimObject("held", "public", "IPv4", true, "203.0.113.7"), classObject("public", ""), addressObject("203.0.113.7", nil))
	got := run(t, c, nil, ea)
	expectCondition(t, got, cozyv1alpha1.ConditionResolved, metav1.ConditionTrue, cozyv1alpha1.ReasonResolved)
	expectCondition(t, got, cozyv1alpha1.ConditionProvisioned, metav1.ConditionFalse, cozyv1alpha1.ReasonNothingToPublish)
	if len(renderedServices(t, c, ea)) != 0 {
		t.Fatal("nothing must be rendered from a sentinel-only endpoint")
	}
}

// TestMethodModes renders the two VM datapath modes and pins the capability
// gate: without the platform's declaration no Service exists at all.
func TestMethodModes(t *testing.T) {
	vmObjects := func(lb cozyv1alpha1.LoadBalancerAttachment) (*cozyv1alpha1.EndpointAttachment, client.Client) {
		vm := endpoint("vm-instance-box", "VMInstance", "box", map[string]string{"vm": "box"}, SentinelPort)
		ea := attachment("box-ip", "ea-1", "VMInstance", "box", "vm-instance-box", lb)
		return ea, newClient(t, helmRelease("vm-instance-box", "hr-1", "VMInstance", "box"), vm, ea,
			endpointSlice("vm-instance-box-1", "vm-instance-box", true, "virt-launcher-box"),
			claimObject("held", "public", "IPv4", true, "203.0.113.7"), classObject("public", ""), addressObject("203.0.113.7", nil))
	}

	t.Run("gated off renders nothing", func(t *testing.T) {
		ea, c := vmObjects(cozyv1alpha1.LoadBalancerAttachment{ClaimName: "held", Method: cozyv1alpha1.ExposureMethodPortList, Ports: []int32{22}})
		got := run(t, c, &Reconciler{CozyProxyContractImplemented: false}, ea)
		expectCondition(t, got, cozyv1alpha1.ConditionProvisioned, metav1.ConditionFalse, cozyv1alpha1.ReasonDatapathUnavailable)
		if len(renderedServices(t, c, ea)) != 0 {
			t.Fatal("an address nothing can serve must not be attracted")
		}
	})

	t.Run("PortList", func(t *testing.T) {
		ea, c := vmObjects(cozyv1alpha1.LoadBalancerAttachment{ClaimName: "held", Method: cozyv1alpha1.ExposureMethodPortList, Ports: []int32{443, 22}, AllowICMP: new(false)})
		run(t, c, &Reconciler{CozyProxyContractImplemented: true}, ea)
		svc := onlyRendered(t, c, ea)
		if svc.Labels[ServiceProxyNameLabel] != ServiceProxyName {
			t.Fatalf("labels = %v", svc.Labels)
		}
		if svc.Annotations[WholeIPAnnotation] != "false" || svc.Annotations[AllowICMPAnnotation] != "false" {
			t.Fatalf("annotations = %v", svc.Annotations)
		}
		if len(svc.Spec.Ports) != 2 || svc.Spec.Ports[0].Port != 22 || svc.Spec.Ports[0].TargetPort.IntValue() != 22 ||
			svc.Spec.Ports[0].Name != "port-22" || svc.Spec.Ports[1].Port != 443 {
			t.Fatalf("ports = %+v", svc.Spec.Ports)
		}
		if !equalStringMaps(svc.Spec.Selector, map[string]string{"vm": "box"}) {
			t.Fatalf("selector = %v", svc.Spec.Selector)
		}
	})

	t.Run("WholeIP", func(t *testing.T) {
		ea, c := vmObjects(cozyv1alpha1.LoadBalancerAttachment{ClaimName: "held", Method: cozyv1alpha1.ExposureMethodWholeIP})
		got := run(t, c, &Reconciler{CozyProxyContractImplemented: true}, ea)
		svc := onlyRendered(t, c, ea)
		if svc.Annotations[WholeIPAnnotation] != "true" {
			t.Fatalf("annotations = %v", svc.Annotations)
		}
		if _, ok := svc.Annotations[AllowICMPAnnotation]; ok {
			t.Fatal("allowICMP is a PortList knob")
		}
		if len(svc.Spec.Ports) != 1 || svc.Spec.Ports[0].Port != SentinelPort {
			t.Fatalf("whole-IP without ports renders the sentinel port, got %+v", svc.Spec.Ports)
		}
		expectCondition(t, got, cozyv1alpha1.ConditionProvisioned, metav1.ConditionFalse, cozyv1alpha1.ReasonWaitingForAddress)
	})

	t.Run("WholeIP with ports", func(t *testing.T) {
		ea, c := vmObjects(cozyv1alpha1.LoadBalancerAttachment{ClaimName: "held", Method: cozyv1alpha1.ExposureMethodWholeIP, Ports: []int32{80}})
		run(t, c, &Reconciler{CozyProxyContractImplemented: true}, ea)
		svc := onlyRendered(t, c, ea)
		if len(svc.Spec.Ports) != 1 || svc.Spec.Ports[0].Port != 80 || svc.Spec.Ports[0].Name != "port-80" {
			t.Fatalf("ports = %+v", svc.Spec.Ports)
		}
	})
}

// TestWholeIPMultipleBackendsHeldDark: 1:1 NAT has no meaning against two
// backends. The address is neither released nor coin-flipped: the Service
// keeps its identity and its claim but selects nothing until one backend
// remains.
func TestWholeIPMultipleBackendsHeldDark(t *testing.T) {
	vm := endpoint("vm-instance-box", "VMInstance", "box", map[string]string{"vm": "box"}, SentinelPort)
	ea := attachment("box-ip", "ea-1", "VMInstance", "box", "vm-instance-box", cozyv1alpha1.LoadBalancerAttachment{ClaimName: "held", Method: cozyv1alpha1.ExposureMethodWholeIP})
	slice := endpointSlice("vm-instance-box-1", "vm-instance-box", true, "virt-launcher-src", "virt-launcher-dst")
	c := newClient(t, helmRelease("vm-instance-box", "hr-1", "VMInstance", "box"), vm, ea, slice,
		claimObject("held", "public", "IPv4", true, "203.0.113.7"), classObject("public", ""), addressObject("203.0.113.7", nil))
	r := &Reconciler{CozyProxyContractImplemented: true}

	got := run(t, c, r, ea)
	expectCondition(t, got, cozyv1alpha1.ConditionProvisioned, metav1.ConditionFalse, cozyv1alpha1.ReasonMultipleBackends)
	dark := onlyRendered(t, c, ea)
	if dark.Spec.Selector["vm"] != "" {
		t.Fatalf("a whole-IP Service with two backends must not select them, got %v", dark.Spec.Selector)
	}
	if dark.Annotations[ClaimAnnotation] != "held" || dark.Annotations[WholeIPAnnotation] != "true" {
		t.Fatalf("the address must stay held on the same Service: %v", dark.Annotations)
	}
	setIngress(t, c, dark, "203.0.113.7")
	got = run(t, c, r, ea)
	expectCondition(t, got, cozyv1alpha1.ConditionProvisioned, metav1.ConditionFalse, cozyv1alpha1.ReasonMultipleBackends)

	// The migration completes: one backend remains, the mirror is restored on
	// the same Service.
	live := &discoveryv1.EndpointSlice{}
	if err := c.Get(context.TODO(), client.ObjectKeyFromObject(slice), live); err != nil {
		t.Fatal(err)
	}
	live.Endpoints = live.Endpoints[1:]
	if err := c.Update(context.TODO(), live); err != nil {
		t.Fatal(err)
	}
	got = run(t, c, r, ea)
	lit := onlyRendered(t, c, ea)
	if lit.Name != dark.Name || lit.Spec.Selector["vm"] != "box" {
		t.Fatalf("Service identity must survive the handover: %s %v", lit.Name, lit.Spec.Selector)
	}
	expectCondition(t, got, cozyv1alpha1.ConditionProvisioned, metav1.ConditionTrue, cozyv1alpha1.ReasonProvisioned)
}

// TestBackendExclusivity keys the one-delegation-per-backend rule on the pod,
// in either mode, counting chart-rendered Services as well as attachments.
// The older delegation wins and the newer one renders no Service.
func TestBackendExclusivity(t *testing.T) {
	legacy := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: "vm-instance-box", UID: "svc-legacy",
			CreationTimestamp: metav1.NewTime(time.Now().Add(-time.Hour)),
			Labels: func() map[string]string {
				l := appLabels("VMInstance", "box")
				l[corev1alpha1.TenantResourceLabelKey] = corev1alpha1.TenantResourceLabelValue
				l[ServiceProxyNameLabel] = ServiceProxyName
				return l
			}(),
			Annotations: map[string]string{WholeIPAnnotation: "false", AllowICMPAnnotation: "true"}},
		Spec: corev1.ServiceSpec{Type: corev1.ServiceTypeLoadBalancer, Selector: map[string]string{"vm": "box"},
			Ports: []corev1.ServicePort{{Name: "port-22", Port: 22}}},
	}
	ea := attachment("box-ip", "ea-1", "VMInstance", "box", "vm-instance-box", cozyv1alpha1.LoadBalancerAttachment{ClaimName: "held", Method: cozyv1alpha1.ExposureMethodWholeIP})
	c := newClient(t, helmRelease("vm-instance-box", "hr-1", "VMInstance", "box"), legacy, ea,
		endpointSlice("vm-instance-box-1", "vm-instance-box", true, "virt-launcher-box"),
		claimObject("held", "public", "IPv4", true, "203.0.113.7"), classObject("public", ""), addressObject("203.0.113.7", nil))
	r := &Reconciler{CozyProxyContractImplemented: true}

	got := run(t, c, r, ea)
	expectCondition(t, got, cozyv1alpha1.ConditionProvisioned, metav1.ConditionFalse, cozyv1alpha1.ReasonBackendAlreadyDelegated)
	if len(renderedServices(t, c, ea)) != 0 {
		t.Fatal("the losing delegation must not present a Service the datapath would steal the slot for")
	}

	// The legacy exposure is switched off: the slot frees and the attachment
	// renders.
	if err := c.Delete(context.TODO(), legacy); err != nil {
		t.Fatal(err)
	}
	if err := c.Create(context.TODO(), endpoint("vm-instance-box", "VMInstance", "box", map[string]string{"vm": "box"}, SentinelPort)); err != nil {
		t.Fatal(err)
	}
	run(t, c, r, ea)
	own := onlyRendered(t, c, ea)

	// A second whole-IP attachment on the same VM loses to the first, with
	// the distinct reason; the first keeps its Service.
	if err := c.Create(context.TODO(), endpointSlice(own.Name+"-1", own.Name, true, "virt-launcher-box")); err != nil {
		t.Fatal(err)
	}
	ea2 := attachment("box-ip-2", "ea-2", "VMInstance", "box", "vm-instance-box", cozyv1alpha1.LoadBalancerAttachment{ClaimName: "held2", Method: cozyv1alpha1.ExposureMethodWholeIP})
	if err := c.Create(context.TODO(), ea2); err != nil {
		t.Fatal(err)
	}
	if err := c.Create(context.TODO(), claimObject("held2", "public", "IPv4", true, "203.0.113.8")); err != nil {
		t.Fatal(err)
	}
	got2 := run(t, c, r, ea2)
	expectCondition(t, got2, cozyv1alpha1.ConditionProvisioned, metav1.ConditionFalse, cozyv1alpha1.ReasonBackendAlreadyDelegated)
	if len(renderedServices(t, c, ea2)) != 0 {
		t.Fatal("second whole-IP attachment must render nothing")
	}
	if len(renderedServices(t, c, ea)) != 1 {
		t.Fatal("the winner keeps its Service")
	}
}

// TestSubstrateAbsent: without the claim kind served, the attachment
// resolves, reports that it is waiting on the substrate, and renders nothing.
func TestSubstrateAbsent(t *testing.T) {
	ep := endpoint("postgres-mydb-ro", "Postgres", "mydb", map[string]string{"role": "replica"}, 5432)
	ea := attachment("ro-public", "ea-1", "Postgres", "mydb", "postgres-mydb-ro", cozyv1alpha1.LoadBalancerAttachment{})
	noKind := &apimeta.NoKindMatchError{GroupKind: claimGVK.GroupKind(), SearchedVersions: []string{"v1alpha1"}}
	c := fake.NewClientBuilder().WithScheme(newScheme(t)).
		WithStatusSubresource(&cozyv1alpha1.EndpointAttachment{}).
		WithObjects(helmRelease("postgres-mydb", "hr-1", "Postgres", "mydb"), ep, ea).
		WithInterceptorFuncs(interceptor.Funcs{
			List: func(ctx context.Context, c client.WithWatch, list client.ObjectList, opts ...client.ListOption) error {
				if _, ok := list.(*unstructured.UnstructuredList); ok {
					return noKind
				}
				return c.List(ctx, list, opts...)
			},
		}).Build()
	got := run(t, c, nil, ea)
	expectCondition(t, got, cozyv1alpha1.ConditionResolved, metav1.ConditionTrue, cozyv1alpha1.ReasonResolved)
	expectCondition(t, got, cozyv1alpha1.ConditionProvisioned, metav1.ConditionFalse, cozyv1alpha1.ReasonClaimPending)
	if len(renderedServices(t, c, ea)) != 0 {
		t.Fatal("no Service without a claim to consume")
	}
}

func TestFamilyConflictWithReferencedClaim(t *testing.T) {
	ep := endpoint("postgres-mydb-ro", "Postgres", "mydb", map[string]string{"role": "replica"}, 5432)
	ea := attachment("ro-public", "ea-1", "Postgres", "mydb", "postgres-mydb-ro", cozyv1alpha1.LoadBalancerAttachment{ClaimName: "held", Family: cozyv1alpha1.AddressFamilyIPv6})
	c := newClient(t, helmRelease("postgres-mydb", "hr-1", "Postgres", "mydb"), ep, ea,
		claimObject("held", "public", "IPv4", true, "203.0.113.7"), classObject("public", ""))
	got := run(t, c, nil, ea)
	expectCondition(t, got, cozyv1alpha1.ConditionProvisioned, metav1.ConditionFalse, cozyv1alpha1.ReasonFamilyConflict)
	if len(renderedServices(t, c, ea)) != 0 {
		t.Fatal("nothing is rendered against the wrong family")
	}
}
