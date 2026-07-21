// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Cozystack Authors.

package siterouter

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"

	helmv2 "github.com/fluxcd/helm-controller/api/v2"
	corev1 "k8s.io/api/core/v1"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"github.com/cozystack/cozystack/internal/vyos"
	"github.com/cozystack/cozystack/internal/vyos/render"
)

// -----------------------------------------------------------------------------
// fakeVyOS — the injectable VyOS client stub, ported from cozyportal's
// router_controller_test.go and extended with Retrieve (the source-filter
// confirmation query, net-new for OSS). It records every Configure batch,
// lets a test inject a Configure failure, and serves canned Show/Retrieve
// results. Concurrent-safe though the controller is single-threaded per
// reconcile.
// -----------------------------------------------------------------------------

type fakeVyOS struct {
	mu             sync.Mutex
	configureCalls [][]vyos.Operation
	configureErr   error

	ipsecObservations []vyos.IPSecObservation
	bgpObservations   []vyos.BGPObservation
	ethObservations   []vyos.EthernetObservation
	ethErr            error

	// retrieveResult / retrieveErr back confirmSourceFilterActive's guest query.
	// A nil/`null` result means the tunnel-ingress rule set is not present yet.
	retrieveResult json.RawMessage
	retrieveErr    error
}

func (f *fakeVyOS) Configure(_ context.Context, ops []vyos.Operation) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.configureCalls = append(f.configureCalls, ops)
	return f.configureErr
}

func (f *fakeVyOS) ShowVPNIPSecSA(_ context.Context) ([]vyos.IPSecObservation, error) {
	return f.ipsecObservations, nil
}

func (f *fakeVyOS) ShowBGPSummary(_ context.Context) ([]vyos.BGPObservation, error) {
	return f.bgpObservations, nil
}

func (f *fakeVyOS) ShowInterfacesDetail(_ context.Context) ([]vyos.EthernetObservation, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.ethObservations, f.ethErr
}

func (f *fakeVyOS) Retrieve(_ context.Context, _ []string) (json.RawMessage, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.retrieveResult, f.retrieveErr
}

// setRetrieve swaps the canned /retrieve result mid-test, standing in for the
// guest source filter coming up, being wiped, or recovering between polls.
func (f *fakeVyOS) setRetrieve(raw json.RawMessage) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.retrieveResult = raw
}

// Configures reports the number of Configure batches applied so far.
func (f *fakeVyOS) Configures() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.configureCalls)
}

// lastOps returns the most recently applied Configure batch, or nil if none.
func (f *fakeVyOS) lastOps() []vyos.Operation {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.configureCalls) == 0 {
		return nil
	}
	return f.configureCalls[len(f.configureCalls)-1]
}

// -----------------------------------------------------------------------------
// Test fixtures
// -----------------------------------------------------------------------------

// newVyOSReconciler builds a reconciler wired to inject the given fakeVyOS for
// every gateway endpoint, with a fake event recorder and a scheme that also
// understands the (unstructured) VirtualMachineInstance kind.
func newVyOSReconciler(t *testing.T, v VyOSClient, objs ...client.Object) (*SiteRouterReconciler, *record.FakeRecorder) {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(scheme); err != nil {
		t.Fatalf("add client-go scheme: %v", err)
	}
	if err := helmv2.AddToScheme(scheme); err != nil {
		t.Fatalf("add helm-controller scheme: %v", err)
	}
	scheme.AddKnownTypeWithName(vmiGVK, &unstructured.Unstructured{})
	scheme.AddKnownTypeWithName(vmiGVK.GroupVersion().WithKind("VirtualMachineInstanceList"), &unstructured.UnstructuredList{})

	fc := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(objs...).
		Build()

	rec := record.NewFakeRecorder(64)
	r := &SiteRouterReconciler{
		Client:            fc,
		Scheme:            scheme,
		Recorder:          rec,
		ManagementCIDR:    "10.244.0.0/16",
		VyOSClientFactory: func(_, _ string) VyOSClient { return v },
	}
	return r, rec
}

// routedValues is the standard happy-path spec.values: a single ipsec peer, two
// disjoint remote CIDRs. Neither CIDR overlaps the cluster pod/svc/join networks,
// so deny-set validation passes and the reconcile reaches the config push.
func routedValues() map[string]interface{} {
	return map[string]interface{}{
		"tunnel":      map[string]interface{}{"type": "ipsec"},
		"peer":        map[string]interface{}{"address": "203.0.113.10"},
		"remoteCIDRs": []interface{}{"172.31.0.0/16", "10.10.0.0/16"},
	}
}

// readyObjects returns the full object set a config-push reconcile needs: the
// SiteRouter HelmRelease (with spec.values), the tenant namespace, the cozystack
// ConfigMap (cluster CIDRs), a running gateway pod with a pod IP, and the PSK +
// api-key Secrets the chart creates (D6).
func readyObjects(t *testing.T, name string, values map[string]interface{}, podIP string) []client.Object {
	t.Helper()
	return []client.Object{
		siteRouterHRWithValues(t, name, values),
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "tenant-test"}},
		cozystackConfigMap(),
		gwPod("virt-launcher-"+releasePrefix+name+"-abcde", name, podIP),
		pskSecret(name, "shared-secret"),
		apiKeySecret(name, "api-token-xyz"),
	}
}

// pskSecret builds the per-instance PSK Secret (site-router-<name>-psk, key psk).
func pskSecret(name, psk string) *corev1.Secret {
	return &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: releasePrefix + name + "-psk", Namespace: "tenant-test"},
		Data:       map[string][]byte{"psk": []byte(psk)},
	}
}

// apiKeySecret builds the per-instance management-API key Secret
// (site-router-<name>-api-key, key token) the controller reads (D6).
func apiKeySecret(name, token string) *corev1.Secret {
	return &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: releasePrefix + name + "-api-key", Namespace: "tenant-test"},
		Data:       map[string][]byte{"token": []byte(token)},
	}
}

// gatewayVMI builds the gateway VirtualMachineInstance carrying one pod-network
// NIC with the given MAC + IP in status.interfaces — the MAC the controller joins
// against the guest device table. Its name equals the VM/release name.
func gatewayVMI(name, mac, ipAddress string) *unstructured.Unstructured {
	u := &unstructured.Unstructured{}
	u.SetGroupVersionKind(vmiGVK)
	u.SetName(releasePrefix + name)
	u.SetNamespace("tenant-test")
	_ = unstructured.SetNestedSlice(u.Object, []interface{}{
		map[string]interface{}{"name": "default", "mac": mac, "ipAddress": ipAddress},
	}, "status", "interfaces")
	return u
}

func reconcileInstance(t *testing.T, r *SiteRouterReconciler, name string) ctrl.Result {
	t.Helper()
	res, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Namespace: "tenant-test", Name: releasePrefix + name},
	})
	if err != nil {
		t.Fatalf("reconcile %s: %v", name, err)
	}
	return res
}

func opPath(op vyos.Operation) string { return strings.Join(op.Path, "/") }

// opsHave reports whether ops contains a set at path with the given value (an
// empty value matches any value at that path).
func opsHave(ops []vyos.Operation, path, value string) bool {
	for i := range ops {
		if opPath(ops[i]) == path && (value == "" || ops[i].Value == value) {
			return true
		}
	}
	return false
}

func recordedEvents(rec *record.FakeRecorder) []string {
	var out []string
	for {
		select {
		case e := <-rec.Events:
			out = append(out, e)
		default:
			return out
		}
	}
}

func hasEventReason(rec *record.FakeRecorder, reason string) bool {
	for _, e := range recordedEvents(rec) {
		if strings.Contains(e, reason) {
			return true
		}
	}
	return false
}

func gatewayPortSecurityRelaxed(t *testing.T, r *SiteRouterReconciler, podName string) bool {
	t.Helper()
	pod := &corev1.Pod{}
	if err := r.Get(context.Background(), types.NamespacedName{Namespace: "tenant-test", Name: podName}, pod); err != nil {
		t.Fatalf("get gateway pod: %v", err)
	}
	return pod.Annotations[portSecurityAnnotation] == portSecurityRelaxed
}

// -----------------------------------------------------------------------------
// Tests — each maps to a T06 Acceptance bullet and/or a T12 reconcile case.
// They are written FIRST and fail until pushVyOSConfig / confirmSourceFilterActive
// (and the 30s poll) are implemented in Phase B.
// -----------------------------------------------------------------------------

// TestReconcile_ConfigHashIdempotent encodes T06 Acceptance "config-hash
// idempotency: a no-op reconcile makes no HTTP call to the guest" (T12
// "config-hash idempotency (no Configure when unchanged)"). The first reconcile
// pushes the rendered config once; a second reconcile with identical inputs
// recognises the unchanged hash and makes no further Configure call.
func TestReconcile_ConfigHashIdempotent(t *testing.T) {
	fakeV := &fakeVyOS{
		retrieveResult: json.RawMessage(`{"rule":{"5":{"action":"accept"}}}`),
	}
	r, _ := newVyOSReconciler(t, fakeV, readyObjects(t, "demo", routedValues(), "10.244.0.5")...)

	reconcileInstance(t, r, "demo")
	if fakeV.Configures() != 1 {
		t.Fatalf("expected exactly 1 Configure on first reconcile, got %d", fakeV.Configures())
	}

	reconcileInstance(t, r, "demo")
	if fakeV.Configures() != 1 {
		t.Errorf("expected no second Configure on an unchanged reconcile (hash match), got %d total", fakeV.Configures())
	}
}

// TestReconcile_DegradedOnConfigureError encodes T06 "on failure → Degraded +
// requeue 30s" (T12 "Degraded-on-Configure-error"): a failing POST /configure
// records a ConfigureFailed event, requeues after the runtime-poll interval,
// does NOT record the hash (so the next reconcile retries the push), and does
// NOT relax port security (the guest is not known good).
func TestReconcile_DegradedOnConfigureError(t *testing.T) {
	fakeV := &fakeVyOS{configureErr: errors.New("vyos api timeout")}
	objs := readyObjects(t, "demo", routedValues(), "10.244.0.5")
	r, rec := newVyOSReconciler(t, fakeV, objs...)

	res := reconcileInstance(t, r, "demo")

	if fakeV.Configures() < 1 {
		t.Fatalf("expected a Configure attempt before failing, got %d", fakeV.Configures())
	}
	if res.RequeueAfter != runtimePollInterval {
		t.Errorf("expected requeue after %s on Configure failure, got %s", runtimePollInterval, res.RequeueAfter)
	}
	if !hasEventReason(rec, reasonConfigureFailed) {
		t.Errorf("expected a %s event on Configure failure, events: %v", reasonConfigureFailed, recordedEvents(rec))
	}
	if gatewayPortSecurityRelaxed(t, r, "virt-launcher-"+releasePrefix+"demo-abcde") {
		t.Errorf("port security must NOT be relaxed while the guest is Degraded")
	}

	// Hash not recorded: the next reconcile re-attempts the push.
	reconcileInstance(t, r, "demo")
	if fakeV.Configures() < 2 {
		t.Errorf("expected the failed push to leave the hash unrecorded (retry), got %d Configure calls", fakeV.Configures())
	}
}

// TestReconcile_DriftReApplies encodes T06 Acceptance "editing
// remoteCIDRs/staticRoutes/peer triggers a live POST /configure" (T12 "drift
// re-apply"): after a steady-state apply, changing spec.values re-renders a
// different op slice (new hash) and the next reconcile re-applies exactly once.
func TestReconcile_DriftReApplies(t *testing.T) {
	fakeV := &fakeVyOS{
		retrieveResult: json.RawMessage(`{"rule":{"5":{"action":"accept"}}}`),
	}
	r, _ := newVyOSReconciler(t, fakeV, readyObjects(t, "demo", routedValues(), "10.244.0.5")...)

	reconcileInstance(t, r, "demo")
	if fakeV.Configures() != 1 {
		t.Fatalf("expected 1 Configure after first apply, got %d", fakeV.Configures())
	}

	// Mutate spec.values: add a static route. This changes the rendered ops and
	// therefore the config hash.
	changed := routedValues()
	changed["staticRoutes"] = []interface{}{
		map[string]interface{}{"destination": "192.168.50.0/24", "nextHop": "10.0.0.254"},
	}
	hr := &helmv2.HelmRelease{}
	if err := r.Get(context.Background(), types.NamespacedName{Namespace: "tenant-test", Name: releasePrefix + "demo"}, hr); err != nil {
		t.Fatalf("get HR: %v", err)
	}
	raw, _ := json.Marshal(changed)
	hr.Spec.Values = &apiextensionsv1.JSON{Raw: raw}
	if err := r.Update(context.Background(), hr); err != nil {
		t.Fatalf("update HR values: %v", err)
	}

	reconcileInstance(t, r, "demo")
	if fakeV.Configures() != 2 {
		t.Errorf("expected exactly 1 extra Configure on drift, got %d total", fakeV.Configures())
	}
}

// TestReconcile_ReadyGatedOnSourceFilter encodes T06 reconcile ordering
// "push → confirm source filter active → relax port security" (D8; T12 "Ready
// gated on source-filter-active"). Until the guest reports the tunnel-ingress
// rule set live, port security must stay enforcing and the reconcile requeues.
func TestReconcile_ReadyGatedOnSourceFilter(t *testing.T) {
	podName := "virt-launcher-" + releasePrefix + "demo-abcde"

	t.Run("filter absent — port security stays enforcing", func(t *testing.T) {
		fakeV := &fakeVyOS{retrieveResult: json.RawMessage(`null`)} // rule set not present yet
		r, _ := newVyOSReconciler(t, fakeV, readyObjects(t, "demo", routedValues(), "10.244.0.5")...)

		res := reconcileInstance(t, r, "demo")

		if gatewayPortSecurityRelaxed(t, r, podName) {
			t.Errorf("port security must NOT be relaxed before the guest source filter is confirmed (D8)")
		}
		if res.RequeueAfter == 0 {
			t.Errorf("expected a requeue while waiting for the source filter to come up, got none")
		}
	})

	t.Run("filter present — port security relaxed", func(t *testing.T) {
		fakeV := &fakeVyOS{retrieveResult: json.RawMessage(`{"rule":{"5":{"action":"accept"},"10":{"action":"accept"}}}`)}
		r, _ := newVyOSReconciler(t, fakeV, readyObjects(t, "demo", routedValues(), "10.244.0.5")...)

		reconcileInstance(t, r, "demo")

		if !gatewayPortSecurityRelaxed(t, r, podName) {
			t.Errorf("port security should be relaxed once the guest source filter is confirmed active")
		}
	})
}

// TestReconcile_ReenforcesPortSecurityWhenSourceFilterDropsAfterRelax encodes the
// R2 fix (maintaining, not just establishing, the D8 invariant). Steady state:
// the config is pushed, the source filter is confirmed, port security is relaxed.
// Then the guest wipes the managed config so the filter drops: the next reconcile
// must re-enforce port_security (the compensating guard is down) AND invalidate
// the cached hash so the following reconcile re-pushes and re-stamps the filter,
// after which the port is relaxed again.
func TestReconcile_ReenforcesPortSecurityWhenSourceFilterDropsAfterRelax(t *testing.T) {
	podName := "virt-launcher-" + releasePrefix + "demo-abcde"
	filterUp := json.RawMessage(`{"rule":{"5":{"action":"accept"}}}`)
	fakeV := &fakeVyOS{retrieveResult: filterUp}
	r, _ := newVyOSReconciler(t, fakeV, readyObjects(t, "demo", routedValues(), "10.244.0.5")...)

	// 1) Steady state: push once, confirm filter, relax the port.
	reconcileInstance(t, r, "demo")
	if fakeV.Configures() != 1 {
		t.Fatalf("expected exactly 1 Configure on first reconcile, got %d", fakeV.Configures())
	}
	if !gatewayPortSecurityRelaxed(t, r, podName) {
		t.Fatalf("expected port_security relaxed once the source filter is confirmed")
	}

	// 2) The guest wipes the managed config: the source filter drops.
	fakeV.setRetrieve(json.RawMessage(`null`))
	res := reconcileInstance(t, r, "demo")
	if gatewayPortSecurityRelaxed(t, r, podName) {
		t.Errorf("port_security must be re-enforced while the source filter is down (D8)")
	}
	if res.RequeueAfter == 0 {
		t.Errorf("expected a requeue while the source filter is down, got none")
	}
	// The push is skipped on this reconcile (hash still cached at step start); the
	// re-enforcement is what matters here.
	if fakeV.Configures() != 1 {
		t.Errorf("no re-push expected on the drop-detection reconcile itself, got %d", fakeV.Configures())
	}

	// 3) The filter recovers once re-stamped; the invalidated hash forces a
	// re-push and the port is relaxed again.
	fakeV.setRetrieve(filterUp)
	reconcileInstance(t, r, "demo")
	if fakeV.Configures() != 2 {
		t.Errorf("expected the invalidated hash to force exactly one re-push after the drop, got %d Configure calls", fakeV.Configures())
	}
	if !gatewayPortSecurityRelaxed(t, r, podName) {
		t.Errorf("expected port_security relaxed again once the filter is re-stamped")
	}
}

// TestReconcile_MACDiscoveryBindsToDiscoveredDevice encodes T06 "MAC ↔ device
// discovery" (T12 "MAC→device discovery"): the VMI pod-NIC MAC joins the guest
// `show interfaces detail` table to resolve the tunnel device (here eth1, not
// the positional eth0), and the MSS clamp + tunnel-ingress filter bind to it.
func TestReconcile_MACDiscoveryBindsToDiscoveredDevice(t *testing.T) {
	const mac = "52:54:00:aa:bb:cc"
	fakeV := &fakeVyOS{
		retrieveResult: json.RawMessage(`{"rule":{"5":{"action":"accept"}}}`),
		ethObservations: []vyos.EthernetObservation{
			{Device: "eth0", MAC: "52:54:00:00:00:01"},
			{Device: "eth1", MAC: mac},
		},
	}
	objs := append(readyObjects(t, "demo", routedValues(), "10.244.0.5"),
		gatewayVMI("demo", mac, "10.244.0.5"))
	r, _ := newVyOSReconciler(t, fakeV, objs...)

	reconcileInstance(t, r, "demo")

	ops := fakeV.lastOps()
	if !opsHave(ops, "firewall/options/interface/eth1/adjust-mss", "") {
		t.Errorf("expected MSS clamp bound to the discovered device eth1, ops: %+v", ops)
	}
	if !opsHave(ops, "interfaces/ethernet/eth1/firewall/in/name", render.TunnelIngressRuleSet) {
		t.Errorf("expected tunnel-ingress filter bound to the discovered device eth1, ops: %+v", ops)
	}
}

// TestReconcile_MACDiscoveryFallsBackPositional encodes T12 "MAC→device
// discovery fallback": with no VMI to join (discovery incomplete), the tunnel
// device falls back to the positional eth0 so the render still binds the MSS
// clamp and source filter to a real device.
func TestReconcile_MACDiscoveryFallsBackPositional(t *testing.T) {
	fakeV := &fakeVyOS{
		retrieveResult:  json.RawMessage(`{"rule":{"5":{"action":"accept"}}}`),
		ethObservations: nil, // guest reports nothing to join against
	}
	// No gatewayVMI seeded → the MAC join cannot resolve a device.
	r, _ := newVyOSReconciler(t, fakeV, readyObjects(t, "demo", routedValues(), "10.244.0.5")...)

	reconcileInstance(t, r, "demo")

	ops := fakeV.lastOps()
	if !opsHave(ops, "firewall/options/interface/eth0/adjust-mss", "") {
		t.Errorf("expected MSS clamp to fall back to positional eth0, ops: %+v", ops)
	}
	if !opsHave(ops, "interfaces/ethernet/eth0/firewall/in/name", render.TunnelIngressRuleSet) {
		t.Errorf("expected tunnel-ingress filter to fall back to positional eth0, ops: %+v", ops)
	}
}

// TestReconcile_PSKMissingRequeues encodes T06 risk "PSK secret race: ensure the
// PSK Secret exists before the first Configure; requeue if not yet present"
// (T12 "PSK-missing requeue"). With no PSK Secret the controller must NOT push a
// tunnel with no authentication — it requeues instead.
func TestReconcile_PSKMissingRequeues(t *testing.T) {
	fakeV := &fakeVyOS{}
	// readyObjects minus the PSK Secret.
	objs := []client.Object{
		siteRouterHRWithValues(t, "demo", routedValues()),
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "tenant-test"}},
		cozystackConfigMap(),
		gwPod("virt-launcher-"+releasePrefix+"demo-abcde", "demo", "10.244.0.5"),
		apiKeySecret("demo", "api-token-xyz"),
	}
	r, _ := newVyOSReconciler(t, fakeV, objs...)

	res := reconcileInstance(t, r, "demo")

	if fakeV.Configures() != 0 {
		t.Errorf("must not push a tunnel before the PSK Secret exists, got %d Configure calls", fakeV.Configures())
	}
	if res.RequeueAfter == 0 {
		t.Errorf("expected a requeue while waiting for the PSK Secret, got none")
	}
}

// TestReconcile_ResolveInputsMapping encodes T06 net-new wiring (T12
// "resolveInputs mapping" + the render subset asserted end-to-end through the
// live push): spec.values (peer/remoteCIDRs/staticRoutes/bgp) plus the runtime
// device and management CIDR map into render.Inputs, and the pushed op slice
// carries forced UDP encapsulation, the tunnel-ingress source allow-list, the
// MSS clamp, the re-stamped management firewall, the static route and BGP.
func TestReconcile_ResolveInputsMapping(t *testing.T) {
	values := routedValues()
	values["staticRoutes"] = []interface{}{
		map[string]interface{}{"destination": "192.168.50.0/24", "nextHop": "10.0.0.254"},
	}
	values["bgp"] = map[string]interface{}{
		"enabled":  true,
		"localASN": 65001,
		"neighbors": []interface{}{
			map[string]interface{}{"address": "203.0.113.1", "remoteASN": 65000},
		},
	}

	fakeV := &fakeVyOS{
		retrieveResult:  json.RawMessage(`{"rule":{"5":{"action":"accept"}}}`),
		ethObservations: []vyos.EthernetObservation{{Device: "eth0", MAC: "52:54:00:00:00:01"}},
	}
	r, _ := newVyOSReconciler(t, fakeV, readyObjects(t, "demo", values, "10.244.0.5")...)

	reconcileInstance(t, r, "demo")

	ops := fakeV.lastOps()
	if len(ops) == 0 {
		t.Fatalf("expected the resolved config to be pushed, got no Configure ops")
	}

	// Forced ESP-in-UDP on the peer (always, per render).
	if !opsHave(ops, "vpn/ipsec/site-to-site/peer/203.0.113.10/force-encapsulation", "enable") {
		t.Errorf("expected forced UDP encapsulation on the peer, ops: %+v", ops)
	}
	// Tunnel-ingress source allow-list from remoteCIDRs + default-action drop.
	rs := "firewall/name/" + render.TunnelIngressRuleSet
	if !opsHave(ops, rs+"/rule/10/source/address", "172.31.0.0/16") ||
		!opsHave(ops, rs+"/rule/20/source/address", "10.10.0.0/16") {
		t.Errorf("expected a source-accept rule per remoteCIDR, ops: %+v", ops)
	}
	if !opsHave(ops, rs+"/default-action", "drop") {
		t.Errorf("expected tunnel-ingress default-action drop, ops: %+v", ops)
	}
	// MSS clamp on the resolved device.
	if !opsHave(ops, "firewall/options/interface/eth0/adjust-mss", "") {
		t.Errorf("expected the MSS clamp on the resolved tunnel device, ops: %+v", ops)
	}
	// Management firewall re-stamped every reconcile from --management-cidr.
	if !opsHave(ops, "firewall/input/rule/10/source/address", "10.244.0.0/16") {
		t.Errorf("expected the management firewall re-stamped from --management-cidr, ops: %+v", ops)
	}
	// Static route mapped.
	if !opsHave(ops, "protocols/static/route/192.168.50.0/24/next-hop/10.0.0.254", "") {
		t.Errorf("expected the static route mapped into the render, ops: %+v", ops)
	}
	// BGP mapped (local AS + neighbor remote-as).
	if !opsHave(ops, "protocols/bgp/system-as", "65001") {
		t.Errorf("expected BGP local ASN mapped, ops: %+v", ops)
	}
	if !opsHave(ops, "protocols/bgp/neighbor/203.0.113.1/remote-as", "65000") {
		t.Errorf("expected BGP neighbor remote-as mapped, ops: %+v", ops)
	}
}
