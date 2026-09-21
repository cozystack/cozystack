// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Cozystack Authors.

package siterouter

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	helmv2 "github.com/fluxcd/helm-controller/api/v2"
	corev1 "k8s.io/api/core/v1"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

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
	err := r.validateDeclaredNetworks(context.Background(), inst)
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

// lineageOnlyPod builds a pod that carries this instance's lineage labels but is
// NOT the gateway VM's virt-launcher pod — it has no vm.kubevirt.io/name. The CDI
// importer for the boot DataVolume is the real-world instance of this shape: the
// lineage webhook mutates every pod whose ownership graph reaches the instance's
// HelmRelease (importer pod -> prime PVC -> DataVolume, which carries
// helm.toolkit.fluxcd.io/name from helm-controller's origin-label post-renderer),
// so it is stamped with application.{kind,name} exactly like the gateway pod.
// Running, and named to sort ahead of virt-launcher-*, which is what the live
// importer did when it was picked as the gateway in CI.
func lineageOnlyPod(name, instance, podIP string) *corev1.Pod {
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: "tenant-test",
			Labels: map[string]string{
				appKindLabelKey: siteRouterKind,
				appNameLabelKey: instance,
			},
		},
		Status: corev1.PodStatus{Phase: corev1.PodRunning, PodIP: podIP},
	}
}

// TestDiscoverGatewayPod_RequiresVMNameLabel is the discovery guard: the lineage
// labels identify the application INSTANCE, not the gateway VM, so selecting on
// them alone matches every pod the lineage webhook stamps for this instance — the
// boot DataVolume's CDI importer among them. Callers then treat whatever comes
// back as the gateway, which is why this must reject a non-gateway pod outright
// rather than merely deprioritise it.
func TestDiscoverGatewayPod_RequiresVMNameLabel(t *testing.T) {
	t.Run("a lineage-labelled non-gateway pod is not discovered as the gateway", func(t *testing.T) {
		importer := lineageOnlyPod("importer-prime-c815d6c1", "demo", "10.244.0.212")
		r := newTestReconciler(t, importer)

		pod, err := r.discoverGatewayPod(context.Background(), &instance{name: "demo", namespace: "tenant-test"})
		if err != nil {
			t.Fatalf("discoverGatewayPod: %v", err)
		}
		if pod != nil {
			t.Fatalf("pod %q carries the lineage labels but not %s; discovery must return nil, got it as the gateway",
				pod.Name, vmNameLabel)
		}
	})

	t.Run("the gateway pod wins over a lineage-labelled non-gateway pod", func(t *testing.T) {
		// Both Running, and the importer sorts first by name — so a selector that
		// matches both hands back the importer, not the gateway.
		importer := lineageOnlyPod("importer-prime-c815d6c1", "demo", "10.244.0.212")
		gateway := gwPod("virt-launcher-"+releasePrefix+"demo-abcde", "demo", "10.244.0.5")
		r := newTestReconciler(t, importer, gateway)

		pod, err := r.discoverGatewayPod(context.Background(), &instance{name: "demo", namespace: "tenant-test"})
		if err != nil {
			t.Fatalf("discoverGatewayPod: %v", err)
		}
		if pod == nil {
			t.Fatalf("expected the gateway pod %q, got nil", gateway.Name)
		}
		if pod.Name != gateway.Name {
			t.Fatalf("discovered %q, want the gateway pod %q", pod.Name, gateway.Name)
		}
	})
}

// TestReconcile_DoesNotAimMediationAtALineageLabelledNonGatewayPod is the impact
// half of the guard above, through the real reconcile pipeline: the two things the
// controller does with the discovered pod must both land on the gateway VM. The
// tenant's kube-ovn next hop becomes the pod IP (programNamespaceRoutes), and the
// rendered router config plus the management-API token are POSTed to
// https://<pod IP>/configure with peer-certificate verification off
// (pushVyOSConfig) — so a wrong pod means tenant return traffic blackholed at it
// and the instance's API token handed to it.
func TestReconcile_DoesNotAimMediationAtALineageLabelledNonGatewayPod(t *testing.T) {
	fakeV := &fakeVyOS{retrieveResult: json.RawMessage(`{"rule":{"5":{"action":"accept"}}}`)}
	objs := append(readyObjects(t, "demo", routedValues(), "10.244.0.5"),
		lineageOnlyPod("importer-prime-c815d6c1", "demo", "10.244.0.212"))
	r, _ := newVyOSReconciler(t, fakeV, objs...)

	// Record what endpoint the config push was actually built against; the shared
	// fixture's factory discards its arguments.
	var gotBaseURL, gotToken string
	r.VyOSClientFactory = func(ep VyOSEndpoint) VyOSClient {
		gotBaseURL, gotToken = ep.URL, ep.Token
		return fakeV
	}

	reconcileInstance(t, r, "demo")

	if gotBaseURL != "https://10.244.0.5" {
		t.Errorf("config push endpoint = %q, want the gateway pod https://10.244.0.5 (10.244.0.212 is the lineage-labelled importer)", gotBaseURL)
	}
	if gotToken != "api-token-xyz" {
		t.Fatalf("config push token = %q, want the instance api-key token; the assertion above only means anything if the token travelled", gotToken)
	}

	ns := &corev1.Namespace{}
	if err := r.Get(context.Background(), types.NamespacedName{Name: "tenant-test"}, ns); err != nil {
		t.Fatalf("get namespace: %v", err)
	}
	routes := routeSet(t, ns.Annotations[routesAnnotation])
	// Guard the loop below against passing on an empty set: routedValues declares
	// two remoteCIDRs, so anything short of two entries is a route this reconcile
	// failed to program rather than a next hop it got right.
	if len(routes) != 2 {
		t.Fatalf("expected both remoteCIDRs programmed, got %d entries in %q", len(routes), ns.Annotations[routesAnnotation])
	}
	for dst, gw := range routes {
		if gw != "10.244.0.5" {
			t.Errorf("route %s -> %s, want next hop 10.244.0.5 (the gateway pod)", dst, gw)
		}
	}
}

// TestReconcile_DeletesEvenWithoutTheKindLabel is the ordering regression. The
// kind-label guard used to return before the deletion branch, so an instance
// that lost the label after the controller had already put its finalizer on it
// could never release it: reconcileDelete was unreachable, the HelmRelease sat
// in Terminating and the namespace behind it never finished deleting, with no
// Event and no condition to say why.
//
// Deletion now runs first and acquisition stays under the guard, which is safe
// because reconcileDelete is itself a no-op without the finalizer — the next test
// pins that half, so the hoist cannot quietly become a widening.
func TestReconcile_DeletesEvenWithoutTheKindLabel(t *testing.T) {
	hr := siteRouterHRWithValues(t, "demo", map[string]interface{}{
		"remoteCIDRs": []interface{}{"172.31.0.0/16"},
	})
	hr.Finalizers = []string{finalizer}
	delete(hr.Labels, appKindLabelKey) // the flip this test is about

	ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "tenant-test"}}
	r := newTestReconciler(t, hr, ns)

	if err := r.Delete(context.Background(), hr); err != nil {
		t.Fatalf("delete HR: %v", err)
	}
	if _, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Namespace: hr.Namespace, Name: hr.Name},
	}); err != nil {
		t.Fatalf("reconcile delete: %v", err)
	}

	got := &helmv2.HelmRelease{}
	err := r.Get(context.Background(), types.NamespacedName{Namespace: hr.Namespace, Name: hr.Name}, got)
	if err == nil && controllerutil.ContainsFinalizer(got, finalizer) {
		t.Fatalf("finalizer must be released even with the kind label gone, still %v", got.Finalizers)
	}
	if err != nil && !apierrors.IsNotFound(err) {
		t.Fatalf("get HR: %v", err)
	}
}

// TestReconcile_UnclaimedForeignHRIsNotTornDown is the other side of that hoist:
// an object this controller never claimed must still be left alone on delete.
func TestReconcile_UnclaimedForeignHRIsNotTornDown(t *testing.T) {
	hr := siteRouterHRWithValues(t, "demo", map[string]interface{}{})
	delete(hr.Labels, appKindLabelKey)
	// No finalizer: never claimed. Give it one that is not ours so the fake client
	// keeps the object around after Delete and the assertion has something to read.
	hr.Finalizers = []string{"example.com/other-controller"}

	ns := &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{
			Name:        "tenant-test",
			Annotations: map[string]string{routesAnnotation: `[{"dst":"172.31.0.0/16","gw":"10.244.0.5"}]`},
		},
	}
	r := newTestReconciler(t, hr, ns)

	if err := r.Delete(context.Background(), hr); err != nil {
		t.Fatalf("delete HR: %v", err)
	}
	if _, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Namespace: hr.Namespace, Name: hr.Name},
	}); err != nil {
		t.Fatalf("reconcile delete: %v", err)
	}

	gotNS := &corev1.Namespace{}
	if err := r.Get(context.Background(), types.NamespacedName{Name: "tenant-test"}, gotNS); err != nil {
		t.Fatalf("get namespace: %v", err)
	}
	if ann := gotNS.Annotations[routesAnnotation]; !strings.Contains(ann, "172.31.0.0/16") {
		t.Errorf("an unclaimed HR must not trigger teardown; namespace routes were touched: %q", ann)
	}
	if got := gotNS.Annotations[routesAnnotation]; got == "" {
		t.Errorf("namespace route annotation must survive untouched, got empty")
	}
}

// TestClassify_RecordsEveryFailure pins the inversion. classify used to record
// an Event only for six enumerated reasons, so every other failure — a denied
// nodes list, a rejected namespace patch, any error that is not a reconcileError
// — reached the manager with nothing written on the object. With no status
// condition by design (D9) the Event is the only channel there is.
func TestClassify_RecordsEveryFailure(t *testing.T) {
	tests := []struct {
		name       string
		err        error
		wantReason string
		wantSilent bool
	}{
		{
			name:       "plain error gets the catch-all reason",
			err:        errors.New("nodes is forbidden: cannot list resource nodes"),
			wantReason: reasonReconcileFailed,
		},
		{
			name:       "an enumerated typed wait keeps its own reason",
			err:        &reconcileError{reason: reasonGatewayPending, message: "no gateway pod yet"},
			wantReason: reasonGatewayPending,
		},
		{
			name:       "a reason nobody enumerated is still recorded",
			err:        &reconcileError{reason: "SomeReasonAddedLater", message: "whatever it was"},
			wantReason: "SomeReasonAddedLater",
		},
		{
			name:       "a reason its own step already recorded is not recorded twice",
			err:        &reconcileError{reason: reasonConfigureFailed, message: "already reported at source"},
			wantSilent: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			hr := siteRouterHR("demo")
			r := newTestReconciler(t, hr)
			rec := record.NewFakeRecorder(8)
			r.Recorder = rec
			inst := &instance{hr: hr, name: "demo", namespace: "tenant-test"}

			if _, err := r.classify(context.Background(), inst, tt.err); err == nil {
				t.Fatalf("classify must return the hard error")
			}

			events := recordedEvents(rec)
			if tt.wantSilent {
				if len(events) != 0 {
					t.Fatalf("expected no Event (the step already recorded one), got %+v", events)
				}
				return
			}
			if len(events) != 1 {
				t.Fatalf("expected exactly one Event, got %+v", events)
			}
			if !strings.Contains(events[0], tt.wantReason) {
				t.Errorf("Event %q should carry reason %q", events[0], tt.wantReason)
			}
		})
	}
}

// TestProgramNamespaceRoutes_GatewayIPChange is the write-ordering regression.
//
// The routes annotation is shared by every site-router in the namespace, and the
// only thing distinguishing this gateway's entries from a co-tenant's is the gw
// address. When the gateway pod is replaced, mergeRoutes needs BOTH the new IP
// and the previously recorded one to migrate those entries.
//
// Recording the new IP before writing the entries loses that. If the namespace
// patch then fails, the next reconcile reads the new IP as "previous", the entry
// still keyed to the old one looks like somebody else's, and mergeRoutes returns
// RouteConflict forever with reconcileDelete unable to identify it either.
// Nothing short of hand-editing the namespace recovers it.
func TestProgramNamespaceRoutes_GatewayIPChange(t *testing.T) {
	newFixture := func(t *testing.T, failPatch bool) (*SiteRouterReconciler, *instance) {
		t.Helper()
		hr := siteRouterHRWithValues(t, "demo", map[string]interface{}{
			"remoteCIDRs": []interface{}{"172.31.0.0/16"},
		})
		hr.Annotations = map[string]string{routeGatewayIPAnnotation: "10.244.0.5"}
		ns := &corev1.Namespace{
			ObjectMeta: metav1.ObjectMeta{
				Name:        "tenant-test",
				Annotations: map[string]string{routesAnnotation: `[{"dst":"172.31.0.0/16","gw":"10.244.0.5"}]`},
			},
		}
		// The replacement gateway pod, on a different IP.
		gateway := gwPod("virt-launcher-"+releasePrefix+"demo-fghij", "demo", "10.244.0.6")

		scheme := runtime.NewScheme()
		if err := clientgoscheme.AddToScheme(scheme); err != nil {
			t.Fatalf("add client-go scheme: %v", err)
		}
		if err := helmv2.AddToScheme(scheme); err != nil {
			t.Fatalf("add helm-controller scheme: %v", err)
		}
		b := fake.NewClientBuilder().WithScheme(scheme).WithObjects(hr, ns, gateway, cozystackConfigMap())
		if failPatch {
			b = b.WithInterceptorFuncs(interceptor.Funcs{
				Patch: func(ctx context.Context, c client.WithWatch, obj client.Object, patch client.Patch, opts ...client.PatchOption) error {
					if _, isNS := obj.(*corev1.Namespace); isNS {
						return errors.New("simulated apiserver failure writing the namespace annotation")
					}
					return c.Patch(ctx, obj, patch, opts...)
				},
			})
		}
		r := &SiteRouterReconciler{Client: b.Build(), Scheme: scheme, ManagementCIDR: "10.244.0.0/16"}

		live := &helmv2.HelmRelease{}
		if err := r.Get(context.Background(), types.NamespacedName{Namespace: hr.Namespace, Name: hr.Name}, live); err != nil {
			t.Fatalf("get HelmRelease: %v", err)
		}
		values, err := decodeValues(live)
		if err != nil {
			t.Fatalf("decode values: %v", err)
		}
		return r, &instance{hr: live, name: "demo", namespace: "tenant-test", values: values, gatewayPod: gateway}
	}

	t.Run("a failed namespace write leaves the previous owner recorded", func(t *testing.T) {
		r, inst := newFixture(t, true)

		if err := r.programNamespaceRoutes(context.Background(), inst); err == nil {
			t.Fatal("expected the namespace patch failure to propagate")
		}

		hr := &helmv2.HelmRelease{}
		if err := r.Get(context.Background(), types.NamespacedName{Namespace: "tenant-test", Name: releasePrefix + "demo"}, hr); err != nil {
			t.Fatalf("get HelmRelease: %v", err)
		}
		if got := hr.Annotations[routeGatewayIPAnnotation]; got != "10.244.0.5" {
			t.Fatalf("route owner = %q, want the PREVIOUS gateway IP 10.244.0.5 still recorded; "+
				"recording %q before the entries it claims strands the old entry permanently", got, got)
		}
	})

	t.Run("the migration converges when the namespace write succeeds", func(t *testing.T) {
		r, inst := newFixture(t, false)

		if err := r.programNamespaceRoutes(context.Background(), inst); err != nil {
			t.Fatalf("program routes: %v", err)
		}

		ns := &corev1.Namespace{}
		if err := r.Get(context.Background(), types.NamespacedName{Name: "tenant-test"}, ns); err != nil {
			t.Fatalf("get namespace: %v", err)
		}
		if got := routeSet(t, ns.Annotations[routesAnnotation])["172.31.0.0/16"]; got != "10.244.0.6" {
			t.Errorf("route should have migrated to the replacement gateway, got gw %q", got)
		}

		hr := &helmv2.HelmRelease{}
		if err := r.Get(context.Background(), types.NamespacedName{Namespace: "tenant-test", Name: releasePrefix + "demo"}, hr); err != nil {
			t.Fatalf("get HelmRelease: %v", err)
		}
		if got := hr.Annotations[routeGatewayIPAnnotation]; got != "10.244.0.6" {
			t.Errorf("route owner = %q, want the new gateway IP 10.244.0.6 once the entries carry it", got)
		}
	})

	t.Run("a repeated migration is idempotent, so a crash before the owner write converges", func(t *testing.T) {
		// The state the first subtest leaves behind: entries already migrated,
		// owner still recording the old IP. Running again must not conflict.
		r, inst := newFixture(t, false)
		if err := r.programNamespaceRoutes(context.Background(), inst); err != nil {
			t.Fatalf("first pass: %v", err)
		}
		// Put the owner annotation back to the old IP, as a crash between the two
		// writes would leave it, and reconcile again.
		inst.hr.Annotations[routeGatewayIPAnnotation] = "10.244.0.5"
		if err := r.Update(context.Background(), inst.hr); err != nil {
			t.Fatalf("rewind owner annotation: %v", err)
		}
		if err := r.programNamespaceRoutes(context.Background(), inst); err != nil {
			t.Fatalf("second pass must converge rather than conflict: %v", err)
		}
		ns := &corev1.Namespace{}
		if err := r.Get(context.Background(), types.NamespacedName{Name: "tenant-test"}, ns); err != nil {
			t.Fatalf("get namespace: %v", err)
		}
		if got := routeSet(t, ns.Annotations[routesAnnotation])["172.31.0.0/16"]; got != "10.244.0.6" {
			t.Errorf("route should still point at the replacement gateway, got gw %q", got)
		}
	})
}
