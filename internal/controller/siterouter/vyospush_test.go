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
// fakeVyOS — the injectable VyOS client stub, ported from the reference
// implementation's router_controller_test.go and extended with Retrieve (the source-filter
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
	// ipsecErr injects a transient runtime-poll failure (ShowVPNIPSecSA) so a test
	// can prove a failed poll does not erase the tunnel/BGP metric series.
	ipsecErr error

	// retrieveResult / retrieveErr back confirmSourceFilterActive's guest query.
	// A nil/`null` result means the tunnel-ingress rule set is not present yet.
	// retrieveResult answers the named-set path (firewall/name/TUNNEL-INGRESS).
	retrieveResult json.RawMessage
	retrieveErr    error

	// forwardResult answers the forward-filter path (firewall/forward), the second
	// query confirmSourceFilterActive now makes (F3: the named set only enforces
	// while the forward-chain jump into it exists). When a test leaves it nil the
	// fake synthesises a subtree that MIRRORS the named set's presence — a real
	// push writes both in one batch, so absence tracks together unless a test
	// deliberately diverges them (named set present, jump gone).
	forwardResult json.RawMessage
}

func (f *fakeVyOS) Configure(_ context.Context, ops []vyos.Operation) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.configureCalls = append(f.configureCalls, ops)
	return f.configureErr
}

func (f *fakeVyOS) ShowVPNIPSecSA(_ context.Context) ([]vyos.IPSecObservation, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.ipsecObservations, f.ipsecErr
}

// setIPSecErr swaps the canned ShowVPNIPSecSA error mid-test, standing in for a
// transient runtime-poll failure between polls.
func (f *fakeVyOS) setIPSecErr(err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.ipsecErr = err
}

func (f *fakeVyOS) ShowBGPSummary(_ context.Context) ([]vyos.BGPObservation, error) {
	return f.bgpObservations, nil
}

func (f *fakeVyOS) ShowInterfacesDetail(_ context.Context) ([]vyos.EthernetObservation, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.ethObservations, f.ethErr
}

func (f *fakeVyOS) Retrieve(_ context.Context, path []string) (json.RawMessage, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.retrieveErr != nil {
		return nil, f.retrieveErr
	}
	// Path-aware: confirmSourceFilterActive reads the named set
	// (firewall/ipv4/name/TUNNEL-INGRESS) and the forward-chain jump
	// (firewall/ipv4/forward/filter) separately (F3). The named-set query returns
	// retrieveResult; the forward query returns forwardResult, or — when unset — a
	// subtree mirroring the named set's presence so tests that only wire
	// retrieveResult keep the jump present.
	if len(path) >= 3 && path[0] == "firewall" && path[1] == "ipv4" && path[2] == "forward" {
		if f.forwardResult != nil {
			return f.forwardResult, nil
		}
		if ruleSetPresent(f.retrieveResult) {
			return json.RawMessage(`{"rule":{"20":{"ipsec":{"match-ipsec-in":{}},"action":"jump","jump-target":"` + render.TunnelIngressRuleSet + `"}}}`), nil
		}
		return f.retrieveResult, nil
	}
	return f.retrieveResult, nil
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

// tunnelIngressSources collects, from a pushed op batch, the source addresses
// across the TUNNEL-INGRESS named set's source-accept rules and whether every one
// is destination-constrained (the R1 world-egress fix: a source-only accept would
// forward a valid-source packet to any destination). It scans by rule number so
// it does not pin a specific numbering.
func tunnelIngressSources(ops []vyos.Operation) (sources map[string]bool, allConstrained bool) {
	prefix := "firewall/ipv4/name/" + render.TunnelIngressRuleSet + "/rule/"
	srcByRule := map[string]string{}
	dstByRule := map[string]bool{}
	for i := range ops {
		p := opPath(ops[i])
		if !strings.HasPrefix(p, prefix) {
			continue
		}
		parts := strings.SplitN(strings.TrimPrefix(p, prefix), "/", 2)
		if len(parts) != 2 {
			continue
		}
		switch parts[1] {
		case "source/address":
			srcByRule[parts[0]] = ops[i].Value
		case "destination/address":
			dstByRule[parts[0]] = true
		}
	}
	sources = map[string]bool{}
	allConstrained = true
	for n, s := range srcByRule {
		sources[s] = true
		if !dstByRule[n] {
			allConstrained = false
		}
	}
	return sources, allConstrained
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
// records a ConfigureFailed event, requeues after the runtime-poll interval, and
// does NOT record the hash (so the next reconcile retries the push).
//
// R3: port_security is baked off at pod CREATE (templates/vm.yaml), so the port
// is relaxed regardless of the Degraded state — that is the accepted Phase-1
// boot-window posture (see docs/security-model.md); the guest source filter is
// the compensating control and simply is not confirmed here. The Degraded signal
// is the requeue + event + un-recorded hash, asserted below.
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

	// Hash not recorded: the next reconcile re-attempts the push.
	reconcileInstance(t, r, "demo")
	if fakeV.Configures() < 2 {
		t.Errorf("expected the failed push to leave the hash unrecorded (retry), got %d Configure calls", fakeV.Configures())
	}
}

// TestReconcile_ConfigureErrorRedactsSecrets encodes the R8 fix: a VyOS
// /configure failure that echoes the failing set-command (PSK and api-key token
// included) must not leak either secret into the tenant-readable ConfigureFailed
// Event — they are replaced with a placeholder.
func TestReconcile_ConfigureErrorRedactsSecrets(t *testing.T) {
	const psk = "shared-secret"   // matches readyObjects' pskSecret
	const token = "api-token-xyz" // matches readyObjects' apiKeySecret
	fakeV := &fakeVyOS{configureErr: errors.New(
		"vyos error: Configuration error near 'set vpn ipsec site-to-site peer 203.0.113.10 " +
			"authentication pre-shared-secret " + psk + "' with key " + token)}
	r, rec := newVyOSReconciler(t, fakeV, readyObjects(t, "demo", routedValues(), "10.244.0.5")...)

	res := reconcileInstance(t, r, "demo")
	if res.RequeueAfter != runtimePollInterval {
		t.Fatalf("expected a Degraded requeue on Configure failure, got %s", res.RequeueAfter)
	}

	events := recordedEvents(rec)
	if len(events) == 0 {
		t.Fatalf("expected a ConfigureFailed event")
	}
	foundRedacted := false
	for _, e := range events {
		if strings.Contains(e, psk) {
			t.Errorf("ConfigureFailed event must not echo the PSK, got %q", e)
		}
		if strings.Contains(e, token) {
			t.Errorf("ConfigureFailed event must not echo the api-key token, got %q", e)
		}
		if strings.Contains(e, "[redacted]") {
			foundRedacted = true
		}
	}
	if !foundRedacted {
		t.Errorf("expected the redaction placeholder in the ConfigureFailed event, events: %v", events)
	}
}

// TestReconcile_ConfigureErrorRedactsSecretStraddlingTruncateBoundary encodes the
// F2 fix: redaction must run on the FULL error string BEFORE truncation. A PSK
// positioned so it straddles the 256-byte truncation boundary would, under the
// old truncate-then-redact order, have its cut fall mid-secret — redaction could
// no longer match the whole value, so the surviving prefix leaked into the
// tenant-readable ConfigureFailed Event. Redact-first replaces the whole secret
// before the length cap, so nothing leaks.
func TestReconcile_ConfigureErrorRedactsSecretStraddlingTruncateBoundary(t *testing.T) {
	// A distinctive PSK so a leaked fragment is unambiguous.
	const psk = "PSK-STRADDLE-0123456789abcdef"
	const token = "api-token-xyz"
	const podName = "virt-launcher-" + releasePrefix + "demo-abcde"

	// The message redacted is "VyOS Configure failed: " (23 bytes) + err. 220
	// bytes of padding place the PSK across byte 256 of that string, so a cut at
	// 256 falls inside the PSK.
	errBody := strings.Repeat("x", 220) + psk + " (near set command)"
	fakeV := &fakeVyOS{configureErr: errors.New(errBody)}
	objs := []client.Object{
		siteRouterHRWithValues(t, "demo", routedValues()),
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "tenant-test"}},
		cozystackConfigMap(),
		gwPod(podName, "demo", "10.244.0.5"),
		pskSecret("demo", psk),
		apiKeySecret("demo", token),
	}
	r, rec := newVyOSReconciler(t, fakeV, objs...)

	res := reconcileInstance(t, r, "demo")
	if res.RequeueAfter != runtimePollInterval {
		t.Fatalf("expected a Degraded requeue on Configure failure, got %s", res.RequeueAfter)
	}

	events := recordedEvents(rec)
	if len(events) == 0 {
		t.Fatalf("expected a ConfigureFailed event")
	}
	foundRedacted := false
	for _, e := range events {
		if strings.Contains(e, psk) {
			t.Errorf("ConfigureFailed event must not echo the full PSK, got %q", e)
		}
		// The exact leak the old truncate-then-redact order produced: the PSK
		// prefix that survived the cut. Not even a fragment may appear.
		if strings.Contains(e, "PSK-STRADDLE-") {
			t.Errorf("ConfigureFailed event leaked a PSK prefix straddling the truncation boundary, got %q", e)
		}
		if strings.Contains(e, "[redacted]") {
			foundRedacted = true
		}
	}
	if !foundRedacted {
		t.Errorf("expected the redaction placeholder in the ConfigureFailed event, events: %v", events)
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
// "push → confirm source filter active → verify port_security relaxed" (D8; T12
// "Ready gated on source-filter-active"). Until the guest reports the
// tunnel-ingress rule set live, the controller does not treat the relaxation as
// effective: it requeues (does not progress to Ready).
//
// R3: the OVN port is relaxed from boot (baked on the pod template), so the gate
// is on the controller's Ready progression, not on physically holding the port
// enforcing — the guest source filter is the compensating control. "filter
// absent" therefore asserts the requeue, not an enforcing port.
func TestReconcile_ReadyGatedOnSourceFilter(t *testing.T) {
	podName := "virt-launcher-" + releasePrefix + "demo-abcde"

	t.Run("filter absent — reconcile requeues (not treated as effective)", func(t *testing.T) {
		fakeV := &fakeVyOS{retrieveResult: json.RawMessage(`null`)} // rule set not present yet
		r, _ := newVyOSReconciler(t, fakeV, readyObjects(t, "demo", routedValues(), "10.244.0.5")...)

		res := reconcileInstance(t, r, "demo")

		if res.RequeueAfter == 0 {
			t.Errorf("expected a requeue while waiting for the source filter to come up, got none")
		}
	})

	t.Run("filter present — chart-baked relaxation verified", func(t *testing.T) {
		fakeV := &fakeVyOS{retrieveResult: json.RawMessage(`{"rule":{"5":{"action":"accept"},"10":{"action":"accept"}}}`)}
		r, _ := newVyOSReconciler(t, fakeV, readyObjects(t, "demo", routedValues(), "10.244.0.5")...)

		reconcileInstance(t, r, "demo")

		if !gatewayPortSecurityRelaxed(t, r, podName) {
			t.Errorf("gateway pod should carry the chart-baked port_security relaxation once the source filter is confirmed active")
		}
	})
}

// TestReconcile_ReestablishesSourceFilterWhenItDropsAfterRelax encodes the R3
// maintenance behavior (maintaining, not just establishing, the D8 invariant).
// Steady state: the config is pushed and the source filter is confirmed. Then the
// guest wipes the managed config so the filter drops: the next reconcile must
// invalidate the cached hash so the following reconcile re-pushes and RE-STAMPS
// the filter — the sole compensating control.
//
// R3: the controller no longer flips port_security on a drop. The OVN port is
// relaxed from boot (baked at pod CREATE) and kube-ovn v1.15.10 does not
// reconcile the port from a live annotation flip, so "re-enforcing" it there
// would have no OVN effect and could not be undone on recovery (verify no longer
// re-adds the annotation). The maintenance action is to re-establish the guest
// source filter via a re-push. The port stays baked-relaxed throughout — the
// accepted Phase-1 posture (see docs/security-model.md).
func TestReconcile_ReestablishesSourceFilterWhenItDropsAfterRelax(t *testing.T) {
	podName := "virt-launcher-" + releasePrefix + "demo-abcde"
	filterUp := json.RawMessage(`{"rule":{"5":{"action":"accept"}}}`)
	fakeV := &fakeVyOS{retrieveResult: filterUp}
	r, _ := newVyOSReconciler(t, fakeV, readyObjects(t, "demo", routedValues(), "10.244.0.5")...)

	// 1) Steady state: push once, confirm filter; the baked relaxation verifies.
	reconcileInstance(t, r, "demo")
	if fakeV.Configures() != 1 {
		t.Fatalf("expected exactly 1 Configure on first reconcile, got %d", fakeV.Configures())
	}
	if !gatewayPortSecurityRelaxed(t, r, podName) {
		t.Fatalf("expected the chart-baked port_security relaxation present once the source filter is confirmed")
	}

	// 2) The guest wipes the managed config: the source filter drops.
	fakeV.setRetrieve(json.RawMessage(`null`))
	res := reconcileInstance(t, r, "demo")
	if !gatewayPortSecurityRelaxed(t, r, podName) {
		t.Errorf("port_security stays baked-relaxed when the filter drops (R3): a live flip has no OVN effect; re-establishing the guest filter is the maintenance action")
	}
	if res.RequeueAfter == 0 {
		t.Errorf("expected a requeue while the source filter is down, got none")
	}
	// The push is skipped on this reconcile (hash still cached at step start); the
	// hash invalidation for the NEXT reconcile is what matters here.
	if fakeV.Configures() != 1 {
		t.Errorf("no re-push expected on the drop-detection reconcile itself, got %d", fakeV.Configures())
	}

	// 3) The filter recovers once re-stamped; the invalidated hash forces a
	// re-push and the relaxation verifies again.
	fakeV.setRetrieve(filterUp)
	reconcileInstance(t, r, "demo")
	if fakeV.Configures() != 2 {
		t.Errorf("expected the invalidated hash to force exactly one re-push after the drop, got %d Configure calls", fakeV.Configures())
	}
	if !gatewayPortSecurityRelaxed(t, r, podName) {
		t.Errorf("expected the chart-baked port_security relaxation still present once the filter is re-stamped")
	}
}

// TestReconcile_SourceFilterDownWhenForwardJumpAbsent encodes the F3/D8 fix: the
// tunnel-ingress named set being present is NOT sufficient — the source filter
// only ENFORCES while the forward-chain ipsec-match jump into it also exists. A
// guest that keeps the named set but drops the forward jump silently disables the
// spoofing guard; confirmSourceFilterActive must report the filter DOWN, so the
// reconcile requeues (not treated as effective) and the cached hash is
// invalidated to force a re-push that re-stamps the jump.
//
// R3: the OVN port stays baked-relaxed throughout (a live flip has no effect);
// the compensating control is the re-established guest filter, driven by the
// hash invalidation and re-push asserted below.
func TestReconcile_SourceFilterDownWhenForwardJumpAbsent(t *testing.T) {
	fakeV := &fakeVyOS{
		// Named set present ...
		retrieveResult: json.RawMessage(`{"rule":{"5":{"action":"accept"}}}`),
		// ... but the forward-chain subtree carries NO jump to TUNNEL-INGRESS.
		forwardResult: json.RawMessage(`{"default-action":"drop","rule":{"5":{"action":"accept"}}}`),
	}
	r, _ := newVyOSReconciler(t, fakeV, readyObjects(t, "demo", routedValues(), "10.244.0.5")...)

	// First reconcile: pushes once, then confirm finds the jump absent → down.
	res := reconcileInstance(t, r, "demo")
	if res.RequeueAfter == 0 {
		t.Errorf("expected a requeue while the source filter is down, got none")
	}
	if fakeV.Configures() != 1 {
		t.Fatalf("expected exactly the initial push, got %d Configure calls", fakeV.Configures())
	}

	// The down path invalidated the cached hash: the next reconcile re-pushes to
	// re-stamp the missing forward jump.
	reconcileInstance(t, r, "demo")
	if fakeV.Configures() != 2 {
		t.Errorf("expected the invalidated hash to force a re-push (re-stamping the jump), got %d Configure calls", fakeV.Configures())
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
	if !opsHave(ops, "interfaces/ethernet/eth1/ip/adjust-mss", "") {
		t.Errorf("expected MSS clamp bound to the discovered device eth1, ops: %+v", ops)
	}
	if !opsHave(ops, "firewall/ipv4/forward/filter/rule/20/jump-target", render.TunnelIngressRuleSet) {
		t.Errorf("expected tunnel-ingress source filter reached via the forward ipsec-match jump, ops: %+v", ops)
	}
}

// TestReconcile_MACDiscoveryFallsBackPositional encodes T12 "MAC→device
// discovery fallback": with no VMI to join (discovery incomplete), the tunnel
// device falls back to the positional eth0 so the render still binds the MSS
// clamp to a real device (eth0). The tunnel-ingress source filter is now
// device-independent (a `firewall forward` ipsec-match jump), so its assertion
// checks the jump, not a per-device binding.
func TestReconcile_MACDiscoveryFallsBackPositional(t *testing.T) {
	fakeV := &fakeVyOS{
		retrieveResult:  json.RawMessage(`{"rule":{"5":{"action":"accept"}}}`),
		ethObservations: nil, // guest reports nothing to join against
	}
	// No gatewayVMI seeded → the MAC join cannot resolve a device.
	r, _ := newVyOSReconciler(t, fakeV, readyObjects(t, "demo", routedValues(), "10.244.0.5")...)

	reconcileInstance(t, r, "demo")

	ops := fakeV.lastOps()
	if !opsHave(ops, "interfaces/ethernet/eth0/ip/adjust-mss", "") {
		t.Errorf("expected MSS clamp to fall back to positional eth0, ops: %+v", ops)
	}
	if !opsHave(ops, "firewall/ipv4/forward/filter/rule/20/jump-target", render.TunnelIngressRuleSet) {
		t.Errorf("expected tunnel-ingress source filter reached via the forward ipsec-match jump, ops: %+v", ops)
	}
}

// TestReconcile_BGPEnabledWithoutValidASN_SkipsBGP encodes the R5 defensive
// guard: bgp.enabled with a missing/zero localASN must NOT render
// `protocols bgp system-as 0`; the whole BGP subtree is skipped. A neighbor with
// an out-of-range remoteASN is likewise dropped rather than emitting
// `remote-as 0`.
func TestReconcile_BGPEnabledWithoutValidASN_SkipsBGP(t *testing.T) {
	values := routedValues()
	values["bgp"] = map[string]interface{}{
		"enabled": true, // no localASN -> intField yields 0
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
	if opsHave(ops, "protocols/bgp/system-as", "") {
		t.Errorf("BGP must be skipped (no system-as) when enabled without a valid localASN, ops: %+v", ops)
	}
	if opsHave(ops, "protocols/bgp/neighbor/203.0.113.1/remote-as", "") {
		t.Errorf("no BGP neighbor should be rendered when localASN is invalid, ops: %+v", ops)
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

	// VyOS 1.5 site-to-site peer model: the peer key is the (sanitised) instance
	// name, the remote IP is in remote-address, and forced ESP-in-UDP is the
	// value-less `force-udp-encapsulation` leaf (all validated live).
	if !opsHave(ops, "vpn/ipsec/site-to-site/peer/demo/remote-address", "203.0.113.10") {
		t.Errorf("expected the remote peer IP in remote-address, ops: %+v", ops)
	}
	if !opsHave(ops, "vpn/ipsec/site-to-site/peer/demo/force-udp-encapsulation", "") {
		t.Errorf("expected forced UDP encapsulation on the peer, ops: %+v", ops)
	}
	// PSK moved out of the peer into the global authentication subtree.
	if !opsHave(ops, "vpn/ipsec/authentication/psk/demo/id", "203.0.113.10") {
		t.Errorf("expected the global PSK matched to the peer by id, ops: %+v", ops)
	}
	// Tunnel-ingress source allow-list from remoteCIDRs, each source-accept
	// destination-constrained to a tenant network (R1 world-egress fix), plus the
	// default-action drop.
	rs := "firewall/ipv4/name/" + render.TunnelIngressRuleSet
	srcs, allConstrained := tunnelIngressSources(ops)
	if !srcs["172.31.0.0/16"] || !srcs["10.10.0.0/16"] {
		t.Errorf("expected a source-accept rule per remoteCIDR, ops: %+v", ops)
	}
	if !allConstrained {
		t.Errorf("every tunnel-ingress source-accept must be destination-constrained to a tenant network (R1), ops: %+v", ops)
	}
	if !opsHave(ops, rs+"/default-action", "drop") {
		t.Errorf("expected tunnel-ingress default-action drop, ops: %+v", ops)
	}
	// MSS clamp on the resolved device.
	if !opsHave(ops, "interfaces/ethernet/eth0/ip/adjust-mss", "") {
		t.Errorf("expected the MSS clamp on the resolved tunnel device, ops: %+v", ops)
	}
	// Management firewall re-stamped every reconcile from --management-cidr.
	if !opsHave(ops, "firewall/ipv4/input/filter/rule/10/source/address", "10.244.0.0/16") {
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
