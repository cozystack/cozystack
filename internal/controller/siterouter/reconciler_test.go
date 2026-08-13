// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Cozystack Authors.

package siterouter

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	helmv2 "github.com/fluxcd/helm-controller/api/v2"
	corev1 "k8s.io/api/core/v1"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"github.com/cozystack/cozystack/internal/siterouter/denyset"
)

func TestValidateManagementCIDR(t *testing.T) {
	tests := []struct {
		name           string
		managementCIDR string
		allowOpen      bool
		wantErr        bool
	}{
		{
			name:           "empty without allow-open fails closed",
			managementCIDR: "",
			allowOpen:      false,
			wantErr:        true,
		},
		{
			name:           "empty with allow-open is permitted",
			managementCIDR: "",
			allowOpen:      true,
			wantErr:        false,
		},
		{
			name:           "valid CIDR is accepted",
			managementCIDR: "10.244.0.0/16",
			allowOpen:      false,
			wantErr:        false,
		},
		{
			name:           "valid CIDR is accepted regardless of allow-open",
			managementCIDR: "10.244.0.0/16",
			allowOpen:      true,
			wantErr:        false,
		},
		{
			name:           "malformed CIDR is rejected",
			managementCIDR: "10.244.0.0/33",
			allowOpen:      true,
			wantErr:        true,
		},
		{
			name:           "bare IP without mask is rejected",
			managementCIDR: "10.244.0.1",
			allowOpen:      false,
			wantErr:        true,
		},
		// The value becomes a source match in the guest's `firewall ipv4` management
		// rule, so an IPv6 range parses as a CIDR and then yields a rule that can
		// never match the controller — the controller would start and lock itself out
		// of every gateway. The chart's pattern is IPv4-only; the flag must agree.
		{
			name:           "IPv6 CIDR is rejected (the firewall rule is IPv4-only)",
			managementCIDR: "2001:db8::/64",
			allowOpen:      false,
			wantErr:        true,
		},
		{
			name:           "IPv6 CIDR is rejected regardless of allow-open",
			managementCIDR: "fd00::/8",
			allowOpen:      true,
			wantErr:        true,
		},
		{
			name:           "IPv4-in-IPv6 notation is accepted (it is a v4 range)",
			managementCIDR: "::ffff:10.244.0.0/112",
			allowOpen:      false,
			wantErr:        false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateManagementCIDR(tt.managementCIDR, tt.allowOpen)
			if tt.wantErr && err == nil {
				t.Fatalf("ValidateManagementCIDR(%q, %v) = nil, want error", tt.managementCIDR, tt.allowOpen)
			}
			if !tt.wantErr && err != nil {
				t.Fatalf("ValidateManagementCIDR(%q, %v) = %v, want nil", tt.managementCIDR, tt.allowOpen, err)
			}
		})
	}
}

func newTestReconciler(t *testing.T, objs ...client.Object) *SiteRouterReconciler {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(scheme); err != nil {
		t.Fatalf("add client-go scheme: %v", err)
	}
	if err := helmv2.AddToScheme(scheme); err != nil {
		t.Fatalf("add helm-controller scheme: %v", err)
	}
	fc := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(objs...).
		Build()
	return &SiteRouterReconciler{Client: fc, Scheme: scheme, ManagementCIDR: "10.244.0.0/16"}
}

func siteRouterHR(name string) *helmv2.HelmRelease {
	return &helmv2.HelmRelease{
		ObjectMeta: metav1.ObjectMeta{
			Name:      releasePrefix + name,
			Namespace: "tenant-test",
			Labels: map[string]string{
				appKindLabelKey:  siteRouterKind,
				appGroupLabelKey: appGroup,
				appNameLabelKey:  name,
			},
		},
	}
}

// TestReconcileNoInstance verifies a missing instance is a clean no-op.
func TestReconcileNoInstance(t *testing.T) {
	r := newTestReconciler(t)
	res, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Namespace: "tenant-test", Name: "site-router-absent"},
	})
	if err != nil {
		t.Fatalf("Reconcile absent instance: %v", err)
	}
	if res.RequeueAfter != 0 {
		t.Fatalf("expected no requeue for absent instance, got %+v", res)
	}
}

// TestReconcileInstanceAddsFinalizer verifies the scaffold reconcile discovers
// the instance and establishes the cleanup finalizer without performing any
// mediation.
func TestReconcileInstanceAddsFinalizer(t *testing.T) {
	hr := siteRouterHR("demo")
	r := newTestReconciler(t, hr)

	if _, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Namespace: hr.Namespace, Name: hr.Name},
	}); err != nil {
		t.Fatalf("Reconcile instance: %v", err)
	}

	got := &helmv2.HelmRelease{}
	if err := r.Get(context.Background(), types.NamespacedName{Namespace: hr.Namespace, Name: hr.Name}, got); err != nil {
		t.Fatalf("get instance after reconcile: %v", err)
	}
	found := false
	for _, f := range got.Finalizers {
		if f == finalizer {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected finalizer %q on instance, got %v", finalizer, got.Finalizers)
	}
}

// TestInstanceName covers deriving the bare instance name from the HelmRelease.
func TestInstanceName(t *testing.T) {
	if got := instanceName(siteRouterHR("demo")); got != "demo" {
		t.Fatalf("instanceName from labeled HR = %q, want demo", got)
	}
	unlabeled := &helmv2.HelmRelease{ObjectMeta: metav1.ObjectMeta{Name: "site-router-demo"}}
	if got := instanceName(unlabeled); got != "demo" {
		t.Fatalf("instanceName from prefix strip = %q, want demo", got)
	}
}

// siteRouterHRWithValues builds a SiteRouter HelmRelease whose spec.values decode
// to the given map — the authoritative tenant inputs the controller reads (D7).
func siteRouterHRWithValues(t *testing.T, name string, values map[string]interface{}) *helmv2.HelmRelease {
	t.Helper()
	hr := siteRouterHR(name)
	raw, err := json.Marshal(values)
	if err != nil {
		t.Fatalf("marshal values: %v", err)
	}
	hr.Spec.Values = &apiextensionsv1.JSON{Raw: raw}
	return hr
}

// cozystackConfigMap is the cozy-system/cozystack ConfigMap the controller reads
// the cluster pod/service/join CIDRs from for deny-set validation.
func cozystackConfigMap() *corev1.ConfigMap {
	return &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Name: "cozystack", Namespace: "cozy-system"},
		Data: map[string]string{
			"ipv4-pod-cidr":  "10.244.0.0/16",
			"ipv4-svc-cidr":  "10.96.0.0/16",
			"ipv4-join-cidr": "100.64.0.0/16",
		},
	}
}

// TestReconcile_DenySetRejection encodes the T07 Acceptance "an overlapping
// remoteCIDR is rejected with InvalidRemoteCIDR, route not programmed": a
// remoteCIDR overlapping the cluster pod CIDR must fail validation with a
// machine-readable reason naming the offending CIDR, and the tenant namespace
// must NOT gain a routes annotation.
func TestReconcile_DenySetRejection(t *testing.T) {
	hr := siteRouterHRWithValues(t, "demo", map[string]interface{}{
		"remoteCIDRs": []interface{}{"10.244.7.0/24"}, // overlaps pod 10.244.0.0/16
	})
	ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "tenant-test"}}
	r := newTestReconciler(t, hr, ns, cozystackConfigMap())

	_, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Namespace: hr.Namespace, Name: hr.Name},
	})
	if err == nil {
		t.Fatalf("expected reconcile to fail deny-set validation, got nil error")
	}
	if !strings.Contains(err.Error(), denyset.ReasonInvalidRemoteCIDR) {
		t.Errorf("error %q should carry reason %q", err.Error(), denyset.ReasonInvalidRemoteCIDR)
	}
	if !strings.Contains(err.Error(), "10.244.7.0/24") {
		t.Errorf("error %q should name the offending CIDR 10.244.7.0/24", err.Error())
	}

	got := &corev1.Namespace{}
	if err := r.Get(context.Background(), types.NamespacedName{Name: "tenant-test"}, got); err != nil {
		t.Fatalf("get namespace: %v", err)
	}
	if v, programmed := got.Annotations[routesAnnotation]; programmed {
		t.Errorf("a rejected remoteCIDR must not program routes, namespace has %s=%q", routesAnnotation, v)
	}
}

// TestReconcile_ProgramsNamespaceRoutes is the positive counterpart to
// TestReconcile_DenySetRejection: a valid, cluster-disjoint remoteCIDR set drives
// the tenant namespace's ovn.kubernetes.io/routes annotation through the real
// server-side-apply path, one {dst,gw} entry per remoteCIDR pointing at the
// gateway pod IP.
func TestReconcile_ProgramsNamespaceRoutes(t *testing.T) {
	fakeV := &fakeVyOS{retrieveResult: json.RawMessage(`{"rule":{"5":{"action":"accept"}}}`)}
	r, _ := newVyOSReconciler(t, fakeV, readyObjects(t, "demo", routedValues(), "10.244.0.5")...)

	reconcileInstance(t, r, "demo")

	ns := &corev1.Namespace{}
	if err := r.Get(context.Background(), types.NamespacedName{Name: "tenant-test"}, ns); err != nil {
		t.Fatalf("get namespace: %v", err)
	}
	ann := ns.Annotations[routesAnnotation]
	if ann == "" {
		t.Fatalf("expected the tenant namespace to carry %s after reconcile", routesAnnotation)
	}
	set := routeSet(t, ann)
	if set["172.31.0.0/16"] != "10.244.0.5" {
		t.Errorf("expected route 172.31.0.0/16 -> 10.244.0.5, got %q", ann)
	}
	if set["10.10.0.0/16"] != "10.244.0.5" {
		t.Errorf("expected route 10.10.0.0/16 -> 10.244.0.5, got %q", ann)
	}
	hr := &helmv2.HelmRelease{}
	if err := r.Get(context.Background(), types.NamespacedName{Namespace: "tenant-test", Name: releasePrefix + "demo"}, hr); err != nil {
		t.Fatalf("get HelmRelease: %v", err)
	}
	if got := hr.Annotations[routeGatewayIPAnnotation]; got != "10.244.0.5" {
		t.Errorf("route owner annotation = %q, want gateway IP 10.244.0.5", got)
	}
}

// TestReconcile_DenySetChangeWithdrawsExistingRoutes proves a remoteCIDR that
// becomes unsafe after cluster topology changes cannot leave its previously
// programmed namespace route behind.
func TestReconcile_DenySetChangeWithdrawsExistingRoutes(t *testing.T) {
	hr := siteRouterHRWithValues(t, "demo", map[string]interface{}{
		"remoteCIDRs": []interface{}{"192.168.100.0/24"},
	})
	hr.Annotations = map[string]string{routeGatewayIPAnnotation: "10.244.0.5"}
	ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{
		Name: "tenant-test",
		Annotations: map[string]string{
			routesAnnotation: `[{"dst":"192.168.100.0/24","gw":"10.244.0.5"},{"dst":"172.31.0.0/16","gw":"10.244.0.9"}]`,
		},
	}}
	node := &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{Name: "worker-0"},
		Status: corev1.NodeStatus{Addresses: []corev1.NodeAddress{{
			Type: corev1.NodeInternalIP, Address: "192.168.100.10",
		}}},
	}
	r := newTestReconciler(t, hr, ns, node, cozystackConfigMap())

	_, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: types.NamespacedName{Namespace: hr.Namespace, Name: hr.Name}})
	if err == nil || !strings.Contains(err.Error(), denyset.ReasonInvalidRemoteCIDR) {
		t.Fatalf("expected node-overlap denial, got %v", err)
	}
	got := &corev1.Namespace{}
	if err := r.Get(context.Background(), types.NamespacedName{Name: "tenant-test"}, got); err != nil {
		t.Fatalf("get namespace: %v", err)
	}
	set := routeSet(t, got.Annotations[routesAnnotation])
	if _, ok := set["192.168.100.0/24"]; ok {
		t.Errorf("newly invalid route must be withdrawn, got %q", got.Annotations[routesAnnotation])
	}
	if set["172.31.0.0/16"] != "10.244.0.9" {
		t.Errorf("sibling route must survive deny-set withdrawal, got %q", got.Annotations[routesAnnotation])
	}
}

// TestReconcile_EmptyRemoteCIDRsWithdrawsRoute encodes the R4 fix: emptying
// remoteCIDRs (non-empty -> []) must still reconcile the namespace annotation and
// withdraw this gateway's stale entry. The old early-return skipped mergeRoutes
// whenever the desired set was empty, stranding the entry forever. A co-tenant
// entry (different gw) must survive.
func TestReconcile_EmptyRemoteCIDRsWithdrawsRoute(t *testing.T) {
	fakeV := &fakeVyOS{retrieveResult: json.RawMessage(`{"rule":{"5":{"action":"accept"}}}`)}
	values := map[string]interface{}{
		"tunnel":      map[string]interface{}{"type": "ipsec"},
		"peer":        map[string]interface{}{"address": "203.0.113.10"},
		"remoteCIDRs": []interface{}{}, // emptied
	}
	objs := readyObjects(t, "demo", values, "10.244.0.5")
	// Seed the namespace with this gateway's now-stale entry plus a co-tenant entry.
	for _, o := range objs {
		if ns, ok := o.(*corev1.Namespace); ok {
			ns.Annotations = map[string]string{
				routesAnnotation: `[{"dst":"172.31.0.0/16","gw":"10.244.0.5"},{"dst":"192.0.2.0/24","gw":"10.244.0.9"}]`,
			}
		}
	}
	r, _ := newVyOSReconciler(t, fakeV, objs...)

	reconcileInstance(t, r, "demo")

	ns := &corev1.Namespace{}
	if err := r.Get(context.Background(), types.NamespacedName{Name: "tenant-test"}, ns); err != nil {
		t.Fatalf("get namespace: %v", err)
	}
	set := routeSet(t, ns.Annotations[routesAnnotation])
	if _, ok := set["172.31.0.0/16"]; ok {
		t.Errorf("emptying remoteCIDRs must withdraw this gateway's stale entry, got %q", ns.Annotations[routesAnnotation])
	}
	if set["192.0.2.0/24"] != "10.244.0.9" {
		t.Errorf("a co-tenant entry must survive the withdrawal, got %q", ns.Annotations[routesAnnotation])
	}
}

func TestReconcile_RouteConflictPreservesSiblingNextHop(t *testing.T) {
	fakeV := &fakeVyOS{}
	objects := readyObjects(t, "demo", routedValues(), "10.244.0.5")
	for _, object := range objects {
		if ns, ok := object.(*corev1.Namespace); ok {
			ns.Annotations = map[string]string{
				routesAnnotation: `[{"dst":"172.31.0.0/16","gw":"10.244.0.9"}]`,
			}
		}
	}
	r, rec := newVyOSReconciler(t, fakeV, objects...)

	_, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: types.NamespacedName{
		Namespace: "tenant-test", Name: releasePrefix + "demo",
	}})
	if err == nil || !strings.Contains(err.Error(), reasonRouteConflict) {
		t.Fatalf("expected %s error, got %v", reasonRouteConflict, err)
	}
	if !hasEventReason(rec, reasonRouteConflict) {
		t.Errorf("expected %s Warning Event", reasonRouteConflict)
	}
	ns := &corev1.Namespace{}
	if err := r.Get(context.Background(), types.NamespacedName{Name: "tenant-test"}, ns); err != nil {
		t.Fatalf("get namespace: %v", err)
	}
	if got := routeSet(t, ns.Annotations[routesAnnotation])["172.31.0.0/16"]; got != "10.244.0.9" {
		t.Errorf("conflict must preserve sibling next hop 10.244.0.9, got %q", got)
	}
	if fakeV.Configures() != 0 {
		t.Errorf("route conflict must stop before guest configuration, got %d Configure calls", fakeV.Configures())
	}
}

// TestReconcile_FinalizerRestoresStateOnDelete encodes the T07 Acceptance
// "deleting the instance removes the routes annotation + restores port_security":
// on delete the controller must withdraw its own route entry from the namespace
// and restore the gateway pod's port_security before releasing the finalizer.
func TestReconcile_FinalizerRestoresStateOnDelete(t *testing.T) {
	hr := siteRouterHRWithValues(t, "demo", map[string]interface{}{
		"remoteCIDRs": []interface{}{"172.31.0.0/16"},
	})
	hr.Finalizers = []string{finalizer}
	hr.Annotations = map[string]string{routeGatewayIPAnnotation: "10.244.0.5"}

	ns := &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{
			Name:        "tenant-test",
			Annotations: map[string]string{routesAnnotation: `[{"dst":"172.31.0.0/16","gw":"10.244.0.5"}]`},
		},
	}
	gateway := gwPod("virt-launcher-site-router-demo-abcde", "demo", "10.244.0.5")
	gateway.Annotations = map[string]string{portSecurityAnnotation: portSecurityRelaxed}

	r := newTestReconciler(t, hr, ns, gateway)

	// Enter the deleting state: the finalizer keeps the HR around for cleanup.
	if err := r.Delete(context.Background(), hr); err != nil {
		t.Fatalf("delete HR: %v", err)
	}
	if _, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Namespace: hr.Namespace, Name: hr.Name},
	}); err != nil {
		t.Fatalf("reconcile delete: %v", err)
	}

	// port_security restored on the gateway pod (annotation cleared or flipped
	// back to enforcing — anything but the relaxed value).
	gotPod := &corev1.Pod{}
	if err := r.Get(context.Background(), types.NamespacedName{Namespace: "tenant-test", Name: gateway.Name}, gotPod); err != nil {
		t.Fatalf("get gateway pod: %v", err)
	}
	if v, set := gotPod.Annotations[portSecurityAnnotation]; set && v == portSecurityRelaxed {
		t.Errorf("gateway pod port_security must be restored on delete, still %s=%q", portSecurityAnnotation, v)
	}

	// This instance's route entry withdrawn from the namespace annotation.
	gotNS := &corev1.Namespace{}
	if err := r.Get(context.Background(), types.NamespacedName{Name: "tenant-test"}, gotNS); err != nil {
		t.Fatalf("get namespace: %v", err)
	}
	if ann := gotNS.Annotations[routesAnnotation]; strings.Contains(ann, "172.31.0.0/16") {
		t.Errorf("instance route entry must be removed on delete, namespace still has %s=%q", routesAnnotation, ann)
	}
}

// TestReconcile_FinalizerUsesPersistedGatewayAfterPodIsGone proves cleanup still
// removes only this instance's entries when helm-controller has already deleted
// the gateway pod before the SiteRouter finalizer runs.
func TestReconcile_FinalizerUsesPersistedGatewayAfterPodIsGone(t *testing.T) {
	hr := siteRouterHRWithValues(t, "demo", map[string]interface{}{
		"remoteCIDRs": []interface{}{"172.31.0.0/16"},
	})
	hr.Finalizers = []string{finalizer}
	hr.Annotations = map[string]string{routeGatewayIPAnnotation: "10.244.0.5"}
	ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{
		Name: "tenant-test",
		Annotations: map[string]string{
			routesAnnotation: `[{"dst":"172.31.0.0/16","gw":"10.244.0.5"},{"dst":"192.0.2.0/24","gw":"10.244.0.9"}]`,
		},
	}}
	r := newTestReconciler(t, hr, ns)
	if err := r.Delete(context.Background(), hr); err != nil {
		t.Fatalf("delete HR: %v", err)
	}
	if _, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: types.NamespacedName{Namespace: hr.Namespace, Name: hr.Name}}); err != nil {
		t.Fatalf("reconcile delete: %v", err)
	}
	got := &corev1.Namespace{}
	if err := r.Get(context.Background(), types.NamespacedName{Name: "tenant-test"}, got); err != nil {
		t.Fatalf("get namespace: %v", err)
	}
	set := routeSet(t, got.Annotations[routesAnnotation])
	if _, ok := set["172.31.0.0/16"]; ok {
		t.Errorf("persisted gateway owner must withdraw its route, got %q", got.Annotations[routesAnnotation])
	}
	if set["192.0.2.0/24"] != "10.244.0.9" {
		t.Errorf("sibling route must survive, got %q", got.Annotations[routesAnnotation])
	}
}

// TestValidateRemoteCIDRs_RecordsWarningEvent covers the only surface a deny-set
// rejection has on the reconcile path. The returned error is a HARD error, so
// classify stops the pipeline before updateStatus ever runs — there is no condition
// and no status write. The primary tenant path is synchronous fail-closed
// admission, but a CIDR that becomes invalid later (a cluster network reconfigure)
// or an apply that bypassed admission reaches only here, leaving the HelmRelease
// Ready while routes are silently not programmed. The Event is the explanation.
func TestValidateRemoteCIDRs_RecordsWarningEvent(t *testing.T) {
	hr := siteRouterHRWithValues(t, "demo", map[string]interface{}{
		"remoteCIDRs": []interface{}{"10.244.7.0/24"}, // overlaps pod 10.244.0.0/16
	})
	r := newTestReconciler(t, hr, cozystackConfigMap())
	rec := record.NewFakeRecorder(16)
	r.Recorder = rec

	inst := &instance{
		hr:        hr,
		name:      "demo",
		namespace: "tenant-test",
		values:    map[string]interface{}{"remoteCIDRs": []interface{}{"10.244.7.0/24"}},
	}
	err := r.validateRemoteCIDRs(context.Background(), inst)
	if err == nil {
		t.Fatalf("expected deny-set rejection, got nil")
	}

	// Drain the recorder ONCE: recordedEvents and hasEventReason both consume the
	// channel, so calling them in sequence would find an empty second read.
	events := recordedEvents(rec)
	var found string
	for _, e := range events {
		if strings.Contains(e, denyset.ReasonInvalidRemoteCIDR) {
			found = e
		}
	}
	if found == "" {
		t.Fatalf("expected a %q Warning event, got %+v", denyset.ReasonInvalidRemoteCIDR, events)
	}
	if !strings.Contains(found, "10.244.7.0/24") {
		t.Errorf("event %q should name the offending CIDR 10.244.7.0/24", found)
	}
	if !strings.Contains(found, "Warning") {
		t.Errorf("event %q should be a Warning, not Normal", found)
	}
}
