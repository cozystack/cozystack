// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Cozystack Authors.

package siterouter

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	"github.com/cozystack/cozystack/internal/siterouter/denyset"
	"github.com/cozystack/cozystack/internal/vyos"
	"github.com/cozystack/cozystack/internal/vyos/render"
)

// runtimePollInterval is how often a Ready SiteRouter is re-reconciled to
// re-render the VyOS config (re-applying on drift) and refresh the tunnel/BGP
// runtime observations. It also paces the Degraded retry and the transient
// waits (PSK Secret not yet present, gateway pod not scheduled, guest source
// filter not yet confirmed) so those recover without a spec change. Ported from
// the reference implementation's 30s runtime poll.
const runtimePollInterval = 30 * time.Second

// canonicalSchemaVersion stamps every config hash the controller records
// (v<N>:sha256:<hex>). Bump it whenever the canonicalisation of spec.values or
// the shape of the rendered vyos.Operation slice changes: on the first reconcile
// after a bump the controller sees an older v<N-1>: prefix, treats it as drift,
// and force-reapplies. This is what stops a rolling controller update — where
// the in-memory hash cache starts empty — from flapping every gateway when the
// rendered config is byte-for-byte the same as before. Ported from the reference
// implementation.
const canonicalSchemaVersion = 1

// defaultTunnelDevice is the positional fallback for the device carrying tunnel /
// forwarded traffic when MAC discovery cannot resolve it (VMI absent, MAC unknown
// to the guest, discovery query failed). For a single pod-NIC gateway — the
// Phase-1 shape — eth0 is the pod-network device, so the fallback is correct, not
// merely safe.
const defaultTunnelDevice = "eth0"

// Machine-readable reasons the config-push step surfaces. They ride on the
// reconcileError / recorded Events so T09 can project them onto the instance's
// Ready condition without re-deriving intent. Stable strings (part of the D4
// machine-readable contract) — do not rename without updating T09.
const (
	// reasonConfigApplied marks a successful live push of the rendered config.
	reasonConfigApplied = "ConfigApplied"
	// reasonConfigureFailed marks a failed POST /configure — the instance goes
	// Degraded and the reconcile requeues to retry.
	reasonConfigureFailed = "ConfigureFailed"
	// reasonSourceFilterPending marks the guest tunnel-ingress source filter not
	// yet confirmed active; port security stays enforcing until it is (D8).
	reasonSourceFilterPending = "SourceFilterPending"
	// reasonPSKPending marks the per-instance PSK Secret not yet present; the
	// controller requeues rather than push a tunnel with no authentication.
	reasonPSKPending = "PSKSecretPending"
	// reasonAPIKeyPending marks the per-instance management-API key Secret not yet
	// present; the controller cannot authenticate to the guest, so it requeues.
	reasonAPIKeyPending = "APIKeySecretPending"
	// reasonGatewayPending marks the gateway pod not yet scheduled / without a pod
	// IP; there is no management endpoint to dial, so the controller requeues (the
	// pod watch also re-triggers).
	reasonGatewayPending = "GatewayPending"
)

// Secret key/name conventions the chart writes (T04/D6).
const (
	pskSecretKey       = "psk"
	apiTokenSecretKey  = "token"
	pskSecretSuffix    = "-psk"
	apiKeySecretSuffix = "-api-key"
)

// vmiGVK is the KubeVirt VirtualMachineInstance kind. The gateway VMI is read as
// unstructured at this GVK (the repo reads KubeVirt objects by GVK — see
// internal/backupcontroller — never as a typed dependency), for MAC discovery
// only. The read is best-effort: any failure (including a missing RBAC verb)
// falls back to the positional device.
var vmiGVK = schema.GroupVersionKind{Group: "kubevirt.io", Version: "v1", Kind: "VirtualMachineInstance"}

// VyOSClient is the subset of the vyos.Client the reconciler depends on. Taking
// an interface (rather than the concrete *vyos.Client) keeps the config-push and
// runtime-poll logic unit-testable with a fake, without standing up an HTTPS
// server. *vyos.Client satisfies it.
type VyOSClient interface {
	// Configure applies a batch of set/delete operations in one transaction.
	Configure(ctx context.Context, ops []vyos.Operation) error
	// ShowVPNIPSecSA reports the per-peer IPsec SA state for the runtime poll.
	ShowVPNIPSecSA(ctx context.Context) ([]vyos.IPSecObservation, error)
	// ShowBGPSummary reports the per-neighbor BGP session state.
	ShowBGPSummary(ctx context.Context) ([]vyos.BGPObservation, error)
	// ShowInterfacesDetail reports the kernel device ↔ MAC pairs the MAC-discovery
	// join needs to resolve the tunnel device.
	ShowInterfacesDetail(ctx context.Context) ([]vyos.EthernetObservation, error)
	// Retrieve reads the current config subtree at path, used to confirm the
	// tunnel-ingress source filter is live before port security is relaxed.
	Retrieve(ctx context.Context, path []string) (json.RawMessage, error)
}

// VyOSClientFactory builds a VyOSClient for a resolved gateway endpoint and API
// token. The reconciler calls it once per reconcile. Nil selects
// DefaultVyOSClientFactory; tests override SiteRouterReconciler.VyOSClientFactory
// to inject a fake.
type VyOSClientFactory func(endpoint, token string) VyOSClient

// DefaultVyOSClientFactory wraps vyos.NewClient with the production options: the
// gateway ships a self-signed certificate, so TLS verification is skipped and the
// in-band API token authenticates the channel (D6).
func DefaultVyOSClientFactory(endpoint, token string) VyOSClient {
	return vyos.NewClient(endpoint, token, vyos.WithInsecureSkipVerify())
}

// vyosFactory returns the configured factory or the production default.
func (r *SiteRouterReconciler) vyosFactory() VyOSClientFactory {
	if r.VyOSClientFactory != nil {
		return r.VyOSClientFactory
	}
	return DefaultVyOSClientFactory
}

// tunnelIngressRulesetPath is the config path confirmSourceFilterActive reads to
// verify the guest tunnel-ingress source filter is live. It mirrors the render's
// tunnel-ingress rule set (render.TunnelIngressRuleSet). VyOS 1.5-rolling nests
// named rule sets under `firewall ipv4 name <NAME>` (validated live); it stays in
// lockstep with render.tunnelIngressPath.
func tunnelIngressRulesetPath() []string {
	return []string{"firewall", "ipv4", "name", render.TunnelIngressRuleSet}
}

// tunnelIngressForwardPath is the config path confirmSourceFilterActive reads to
// verify the forward-chain ipsec-match jump INTO the tunnel-ingress source filter
// is live (render.renderForwardFilter emits `firewall forward rule 20 jump-target
// TUNNEL-INGRESS`). The named set is only ACTIVE while this jump exists: deleting
// the jump but leaving the named set silently disables the source filter, so both
// must be confirmed (D8). It mirrors render's forwardFilterPath. VyOS 1.5-rolling
// nests the forward hook under `firewall ipv4 forward filter` (validated live);
// it stays in lockstep with render.forwardFilterPath.
func tunnelIngressForwardPath() []string {
	return []string{"firewall", "ipv4", "forward", "filter"}
}

// configHash returns the canonical, schema-versioned hash of a rendered op slice
// (v<N>:sha256:<hex>). JSON is a stable byte stream here: Op and Path are
// deterministic and Value is a plain string, so an unchanged desired state hashes
// identically and the controller skips the live push. Ported from the reference
// implementation.
func configHash(ops []vyos.Operation) (string, error) {
	payload, err := json.Marshal(ops)
	if err != nil {
		return "", fmt.Errorf("marshal ops: %w", err)
	}
	sum := sha256.Sum256(payload)
	return fmt.Sprintf("v%d:sha256:%s", canonicalSchemaVersion, hex.EncodeToString(sum[:])), nil
}

// --- Config-hash cache ----------------------------------------------------
//
// The last-applied hash is kept in memory, keyed by the instance HelmRelease.
// Leader election guarantees a single writer, so an in-memory cache is safe; on
// a controller restart the cache starts empty and the first reconcile re-applies
// once (idempotent — a byte-identical /configure), which the
// canonicalSchemaVersion prefix keeps from flapping. A status subresource would
// survive restarts but the SiteRouter app-instance has none in Phase 1 (D9: keep
// it simple).

func (r *SiteRouterReconciler) lastAppliedHash(key types.NamespacedName) string {
	r.hashMu.Lock()
	defer r.hashMu.Unlock()
	return r.appliedHashes[key]
}

func (r *SiteRouterReconciler) recordAppliedHash(key types.NamespacedName, hash string) {
	r.hashMu.Lock()
	defer r.hashMu.Unlock()
	if r.appliedHashes == nil {
		r.appliedHashes = map[types.NamespacedName]string{}
	}
	r.appliedHashes[key] = hash
}

func (r *SiteRouterReconciler) forgetAppliedHash(key types.NamespacedName) {
	r.hashMu.Lock()
	defer r.hashMu.Unlock()
	delete(r.appliedHashes, key)
}

// --- Config push ----------------------------------------------------------

// pushVyOSConfig renders the routed VyOS configuration from the instance inputs
// and applies it atomically over the VyOS HTTPS API, skipping the call when the
// config hash is unchanged.
//
// Order: wait for the gateway pod IP (the management endpoint) → read the PSK
// (never push a tunnel with no authentication) → read the API token and build the
// client → discover the tunnel device (MAC ↔ ethN, so the render binds the MSS
// clamp and source filter to the right device) → resolve inputs → render → hash.
// If the hash matches the last applied one the live push is skipped (idempotency:
// a no-op reconcile makes no HTTP call). Otherwise Configure is called; on success
// the hash is recorded, on failure the instance goes Degraded (ConfigureFailed
// event + 30s requeue) and the hash is deliberately NOT recorded so the next
// reconcile retries. The client is stashed on the instance for the confirm and
// runtime-poll steps to reuse without a second token read.
//
// The client is built (and the token read) on every reconcile that reaches this
// point, even when the push is skipped, because MAC discovery must run before the
// hash is computed (a device change is drift). This is the reference
// implementation's reconcileReady order — not its reconcileConfiguring order —
// merged into the single pipeline.
func (r *SiteRouterReconciler) pushVyOSConfig(ctx context.Context, inst *instance) error {
	if inst.gatewayPod == nil || inst.gatewayPod.Status.PodIP == "" {
		return &reconcileError{
			reason:       reasonGatewayPending,
			message:      "gateway pod not scheduled or without a pod IP yet",
			requeueAfter: runtimePollInterval,
		}
	}

	psk, ok, err := r.readPSK(ctx, inst)
	if err != nil {
		return err
	}
	if !ok {
		return &reconcileError{
			reason:       reasonPSKPending,
			message:      "PSK Secret not present yet; not pushing an unauthenticated tunnel",
			requeueAfter: runtimePollInterval,
		}
	}

	token, ok, err := r.readAPIToken(ctx, inst)
	if err != nil {
		return err
	}
	if !ok {
		return &reconcileError{
			reason:       reasonAPIKeyPending,
			message:      "management-API key Secret not present yet",
			requeueAfter: runtimePollInterval,
		}
	}

	inst.vc = r.vyosFactory()("https://"+inst.gatewayPod.Status.PodIP, token)

	device := r.discoverInterfaceDevices(ctx, inst)
	if device == "" {
		device = defaultTunnelDevice
	}

	inputs := r.resolveInputs(ctx, inst, psk, device)
	ops := render.Render(inputs)

	hash, err := configHash(ops)
	if err != nil {
		return err
	}

	key := client.ObjectKeyFromObject(inst.hr)
	if r.lastAppliedHash(key) == hash {
		// Unchanged desired state: skip the live push (idempotency). The client is
		// already built so the confirm/poll steps can reuse it.
		return nil
	}

	if err := inst.vc.Configure(ctx, ops); err != nil {
		// A VyOS /configure failure can echo the offending set-command — which
		// includes the PSK and the api-key token — back in its error body. Redact
		// both before the error reaches an Event, a condition message or a log,
		// so a tenant-readable surface never leaks the secret. The pushed op batch
		// itself is never included (only the client's error string is).
		//
		// Redact the FULL error string BEFORE truncating: a secret straddling the
		// 256-byte truncation boundary would otherwise have its prefix survive the
		// cut (redaction can no longer match the whole value) and leak into the
		// tenant-readable Event. Redact first, then cap the length.
		msg := truncMsg(redactSecrets("VyOS Configure failed: "+err.Error(), psk, token))
		r.recordConfigApplyError(inst) // T10: advance site_router_config_apply_errors_total
		if r.Recorder != nil {
			r.Recorder.Event(inst.hr, corev1.EventTypeWarning, reasonConfigureFailed, msg)
		}
		// Hash NOT recorded — the next reconcile retries the push.
		return &reconcileError{
			reason:       reasonConfigureFailed,
			message:      msg,
			requeueAfter: runtimePollInterval,
		}
	}

	r.recordAppliedHash(key, hash)
	if r.Recorder != nil {
		r.Recorder.Event(inst.hr, corev1.EventTypeNormal, reasonConfigApplied, "VyOS configuration applied")
	}
	return nil
}

// confirmSourceFilterActive verifies the guest tunnel-ingress source filter is
// live before any port-security relaxation, so traffic sourced outside
// remoteCIDRs is dropped by the router first (D8). It reads the rule set back over
// the management API; a null/empty subtree means the filter has not committed yet
// and the reconcile requeues (port security stays enforcing).
//
// Post-M2 redesign (T08) the source filter is applied via a `firewall forward`
// ipsec-match jump (render.renderForwardFilter), NOT the old pod-NIC-inbound
// binding, so it no longer catches locally-originated cluster→remote egress. The
// filter is therefore only ACTIVE when TWO things hold: the named rule set
// (TUNNEL-INGRESS) exists AND the forward-chain ipsec-match jump into it exists.
// Confirming only the named set is too shallow — deleting the jump while leaving
// the named set would report the filter active with the spoofing guard down, so
// port_security would stay relaxed with nothing enforcing the source constraint.
// This confirm reads back BOTH: the named set path (unchanged) and the
// forward-filter path; if EITHER is absent the filter is treated as down.
// TODO(T13): the VyOS 1.5 forward-filter + ipsec-match syntax is provisional for
// BOTH the named set and the jump check (see render.forwardFilterPath /
// tunnelIngressForwardPath) and is live-validated by the T13 negative suite,
// which must also prove a valid-source / world-destination packet is dropped
// (see the destination-constraint TODO on render.renderTunnelIngressFilter).
//
// The D8 invariant must be MAINTAINED, not merely established once: if the filter
// is found absent AFTER it was already up (a guest-side wipe/drift, including a
// deleted forward jump), the port must not be left relaxed while the guard is
// down, and the managed config must be re-stamped. sourceFilterDown handles both
// before requeueing.
func (r *SiteRouterReconciler) confirmSourceFilterActive(ctx context.Context, inst *instance) error {
	if inst.vc == nil {
		// Reached only if the push step did not build a client — treat as a wait.
		return r.sourceFilterDown(ctx, inst, "gateway management client not initialised")
	}

	raw, err := inst.vc.Retrieve(ctx, tunnelIngressRulesetPath())
	if err != nil {
		return r.sourceFilterDown(ctx, inst, "querying the guest tunnel-ingress source filter failed: "+truncErr(err))
	}
	if !ruleSetPresent(raw) {
		return r.sourceFilterDown(ctx, inst, "guest tunnel-ingress source filter not active yet")
	}

	// The named set exists, but it only ENFORCES anything while the forward-chain
	// jump routes decrypted traffic into it. Confirm the jump too (D8).
	fwd, err := inst.vc.Retrieve(ctx, tunnelIngressForwardPath())
	if err != nil {
		return r.sourceFilterDown(ctx, inst, "querying the guest forward-chain source-filter jump failed: "+truncErr(err))
	}
	if !forwardJumpPresent(fwd) {
		return r.sourceFilterDown(ctx, inst, "guest forward-chain jump into the tunnel-ingress source filter not active yet")
	}
	return nil
}

// sourceFilterDown maintains the D8 invariant while the guest tunnel-ingress
// source filter is not confirmed active. It (1) re-enforces port_security if a
// prior cycle relaxed it — the compensating guard is down, so the port must not
// stay open — and (2) invalidates the cached config hash when a prior push had
// recorded one, so the next reconcile re-pushes and re-stamps the managed
// subtrees (restoring the guest-side source filter after a wipe and re-stamping
// the management firewall). It then returns the SourceFilterPending requeue.
//
// A failed re-enforcement is returned as a hard error rather than masked by the
// soft requeue: leaving the port relaxed while reporting only "pending" would
// silently violate D8.
func (r *SiteRouterReconciler) sourceFilterDown(ctx context.Context, inst *instance, msg string) error {
	if inst.gatewayPod != nil && inst.gatewayPod.Annotations[portSecurityAnnotation] == portSecurityRelaxed {
		if err := r.restorePortSecurity(ctx, inst); err != nil {
			return err
		}
		log.FromContext(ctx).Info("re-enforced gateway port_security: tunnel-ingress source filter is down",
			"instance", inst.name, "namespace", inst.namespace)
	}

	key := client.ObjectKeyFromObject(inst.hr)
	if r.lastAppliedHash(key) != "" {
		// Force a re-push on the next reconcile so the managed subtrees are
		// re-stamped and the source filter is re-established.
		r.forgetAppliedHash(key)
	}

	return &reconcileError{
		reason:       reasonSourceFilterPending,
		message:      msg,
		requeueAfter: runtimePollInterval,
	}
}

// ruleSetPresent reports whether a /retrieve response carries a live rule set: a
// non-null, non-empty JSON object/array. VyOS returns `null` for an absent
// subtree and `{}` for an empty one.
func ruleSetPresent(raw json.RawMessage) bool {
	switch strings.TrimSpace(string(raw)) {
	case "", "null", "{}", "[]":
		return false
	default:
		return true
	}
}

// forwardJumpPresent reports whether a /retrieve of the guest forward-chain
// filter carries the ipsec-match jump INTO the tunnel-ingress source allow-list.
// render.renderForwardFilter emits a `firewall forward` rule whose jump-target is
// render.TunnelIngressRuleSet ("TUNNEL-INGRESS"); the named set only enforces
// while that jump routes decrypted traffic into it (D8). It scans the retrieved
// subtree for the jump-target name as a string leaf, so it is robust to the exact
// rule numbering and to the version-sensitive leaf key (see the TODO on
// tunnelIngressForwardPath). An absent/null/empty subtree reports not-present.
func forwardJumpPresent(raw json.RawMessage) bool {
	switch strings.TrimSpace(string(raw)) {
	case "", "null", "{}", "[]":
		return false
	}
	var v interface{}
	if err := json.Unmarshal(raw, &v); err != nil {
		return false
	}
	return jsonHasStringValue(v, render.TunnelIngressRuleSet)
}

// jsonHasStringValue reports whether target appears as a string leaf value
// anywhere in a decoded JSON value tree.
func jsonHasStringValue(v interface{}, target string) bool {
	switch t := v.(type) {
	case string:
		return t == target
	case []interface{}:
		for i := range t {
			if jsonHasStringValue(t[i], target) {
				return true
			}
		}
	case map[string]interface{}:
		for k := range t {
			if jsonHasStringValue(t[k], target) {
				return true
			}
		}
	}
	return false
}

// pollRuntimeState queries the guest for IPsec SA and BGP session state and
// stashes the observations on the instance for T09/T10 to project onto status /
// metrics. It produces data only — it builds no conditions and no metrics here
// (D9). A query failure is non-fatal (the caller keeps the previous observations).
func (r *SiteRouterReconciler) pollRuntimeState(ctx context.Context, inst *instance) error {
	if inst.vc == nil {
		return nil
	}
	ipsecObs, err := inst.vc.ShowVPNIPSecSA(ctx)
	if err != nil {
		return fmt.Errorf("show vpn ipsec sa: %w", err)
	}
	bgpObs, err := inst.vc.ShowBGPSummary(ctx)
	if err != nil {
		return fmt.Errorf("show bgp summary: %w", err)
	}
	inst.ipsecObservations = ipsecObs
	inst.bgpObservations = bgpObs
	return nil
}

// --- Input resolution -----------------------------------------------------

// resolveInputs maps the authoritative spec.values plus the resolved runtime
// (tunnel device, PSK, management CIDR) into render.Inputs. It intentionally
// leaves Inputs.Interfaces empty: the gateway VM's NIC addressing is owned by the
// chart / cloud-init / DHCP, not the controller (see render.deleteManagedSubtrees,
// which never deletes `interfaces ethernet`). The management firewall is re-fed
// from r.ManagementCIDR on every call, so it is part of every rendered config
// (re-stamped whenever a push happens). OverlayMTU is left 0 so the render applies
// its design default (1320 → clamp 1280); ExternalIP is left empty so VyOS
// auto-detects the IPsec local-address (Phase-1 responder model — the LB tunnel
// address wiring is a documented follow-up).
func (r *SiteRouterReconciler) resolveInputs(ctx context.Context, inst *instance, psk, tunnelDevice string) render.Inputs {
	vals := inst.values
	remoteCIDRs := stringSlice(vals[remoteCIDRsValueKey])

	// Tenant-reachable cluster networks constrain each tunnel-ingress source-accept
	// so a decrypted packet with a valid remote source but a non-tenant / world
	// destination is dropped (render.renderTunnelIngressFilter). Sourced from the
	// same cozy-system/cozystack ConfigMap the deny-set validation reads; any read
	// error still yields the platform defaults (clusterNetworks returns the
	// all-defaults set alongside the error), so the constraint is never silently
	// dropped — a bad ConfigMap read has already surfaced at validateRemoteCIDRs.
	nets, _ := r.clusterNetworks(ctx)

	in := render.Inputs{
		ManagementCIDR:     r.ManagementCIDR,
		TunnelDevice:       tunnelDevice,
		RemoteCIDRs:        remoteCIDRs,
		TenantNetworkCIDRs: tenantNetworkCIDRs(nets),
	}

	// Single ipsec peer per instance (schema keeps tunnel.type single-value). A
	// peer with no PSK is never rendered (renderIPSec skips it defensively), so
	// only build the tunnel when both are present.
	peerAddr := stringField(vals["peer"], "address")
	if peerAddr != "" && psk != "" {
		// Routed forwarding with source preservation: the remote traffic selector
		// is each declared remoteCIDR, the local selector is any (0.0.0.0/0) — the
		// cluster workload's real source IP is preserved across the tunnel, so the
		// local side is not a fixed subnet. Live-validated in T13.
		locals := make([]string, len(remoteCIDRs))
		for i := range locals {
			locals[i] = "0.0.0.0/0"
		}
		in.Tunnels = []render.IPSecTunnel{{
			Description:   inst.name,
			PeerAddress:   peerAddr,
			PSK:           psk,
			LocalSubnets:  locals,
			RemoteSubnets: remoteCIDRs,
		}}
	}

	for _, e := range sliceOf(vals["staticRoutes"]) {
		dest := stringField(e, "destination")
		nh := stringField(e, "nextHop")
		if dest != "" && nh != "" {
			in.StaticRoutes = append(in.StaticRoutes, render.StaticRoute{Destination: dest, NextHop: nh})
		}
	}

	if boolField(vals["bgp"], "enabled") {
		localASN := intField(vals["bgp"], "localASN")
		if validASN(localASN) {
			cfg := &render.BGPConfig{Asn: localASN}
			for _, n := range sliceOf(mapGet(vals["bgp"], "neighbors")) {
				addr := stringField(n, "address")
				remoteASN := intField(n, "remoteASN")
				// Skip a neighbor with no address or an out-of-range ASN rather than
				// emit `neighbor <addr> remote-as 0`.
				if addr == "" || !validASN(remoteASN) {
					continue
				}
				cfg.Peers = append(cfg.Peers, render.BGPPeer{
					PeerAddress: addr,
					PeerAsn:     remoteASN,
				})
			}
			in.BGP = cfg
		} else {
			// bgp.enabled but no valid local ASN: the schema (minimum/maximum)
			// rejects this at admission; guard the reconcile path against a stale or
			// hand-crafted HelmRelease so the render never emits `system-as 0`.
			log.FromContext(ctx).Info("skipping BGP: bgp.enabled but localASN is missing or outside the valid range (1..4294967295)",
				"instance", inst.name, "namespace", inst.namespace, "localASN", localASN)
		}
	}

	return in
}

// validASN reports whether n is a valid, nonzero autonomous-system number
// (1..4294967295, the 32-bit ASN range).
func validASN(n int64) bool { return n >= 1 && n <= 4294967295 }

// tenantNetworkCIDRs is the set of tenant-reachable cluster networks a decrypted
// tunnel packet may be destined for — the cluster pod and service CIDRs. It feeds
// render.Inputs.TenantNetworkCIDRs so each tunnel-ingress source-accept is
// destination-constrained (no world egress). Empty CIDRs are skipped.
func tenantNetworkCIDRs(nets denyset.ClusterNetworks) []string {
	out := make([]string, 0, 2)
	if nets.PodCIDR != "" {
		out = append(out, nets.PodCIDR)
	}
	if nets.ServiceCIDR != "" {
		out = append(out, nets.ServiceCIDR)
	}
	return out
}

// discoverInterfaceDevices resolves the kernel device carrying tunnel / forwarded
// traffic by joining the gateway VMI's NIC MACs (status.interfaces[].mac) against
// the guest `show interfaces detail` table (device ↔ MAC). It returns the resolved
// device, or "" when discovery is incomplete — VMI absent, its MACs unknown to the
// guest, or either query failing — in which case the caller falls back to the
// positional device. The VMI read is best-effort (any error, including a missing
// RBAC verb, yields no MACs), so a cluster without VMI read access simply keeps
// the positional mapping.
func (r *SiteRouterReconciler) discoverInterfaceDevices(ctx context.Context, inst *instance) string {
	macs := r.gatewayNICMACs(ctx, inst)
	if len(macs) == 0 {
		return ""
	}

	obs, err := inst.vc.ShowInterfacesDetail(ctx)
	if err != nil || len(obs) == 0 {
		if err != nil {
			log.FromContext(ctx).V(1).Info("guest interface discovery failed; falling back to positional device",
				"instance", inst.name, "error", err.Error())
		}
		return ""
	}

	macToDevice := make(map[string]string, len(obs))
	for i := range obs {
		macToDevice[strings.ToLower(obs[i].MAC)] = obs[i].Device
	}

	// The gateway's pod-network NIC carries tunnel / forwarded traffic; take the
	// first NIC MAC the guest recognises.
	for _, mac := range macs {
		if dev, ok := macToDevice[strings.ToLower(mac)]; ok {
			return dev
		}
	}
	return ""
}

// gatewayNICMACs reads the gateway VirtualMachineInstance (unstructured) and
// returns its NIC MACs from status.interfaces[], in order. The VMI name is the
// VM/release name (the gateway pod's vm.kubevirt.io/name label, falling back to
// site-router-<instance>). Best-effort: any read/parse failure returns nil so the
// caller falls back to the positional device.
func (r *SiteRouterReconciler) gatewayNICMACs(ctx context.Context, inst *instance) []string {
	vmName := inst.gatewayPod.Labels[vmNameLabel]
	if vmName == "" {
		vmName = releasePrefix + inst.name
	}

	vmi := &unstructured.Unstructured{}
	vmi.SetGroupVersionKind(vmiGVK)
	if err := r.reader().Get(ctx, types.NamespacedName{Namespace: inst.namespace, Name: vmName}, vmi); err != nil {
		if !apierrors.IsNotFound(err) {
			log.FromContext(ctx).V(1).Info("reading gateway VMI for MAC discovery failed; falling back to positional device",
				"vmi", vmName, "namespace", inst.namespace, "error", err.Error())
		}
		return nil
	}

	ifaces, found, err := unstructured.NestedSlice(vmi.Object, "status", "interfaces")
	if err != nil || !found {
		return nil
	}

	var macs []string
	for _, raw := range ifaces {
		m, ok := raw.(map[string]interface{})
		if !ok {
			continue
		}
		if mac, ok := m["mac"].(string); ok && mac != "" {
			macs = append(macs, mac)
		}
	}
	return macs
}

// --- Secret reads ---------------------------------------------------------

// readPSK reads the tunnel pre-shared key. The Secret is peer.auth.existingSecret
// when the tenant supplied one, else the chart-generated site-router-<name>-psk
// (key psk, D6). Returns ok=false when the Secret or key is absent so the caller
// can requeue rather than push an unauthenticated tunnel.
func (r *SiteRouterReconciler) readPSK(ctx context.Context, inst *instance) (string, bool, error) {
	name := stringField(mapGet(inst.values["peer"], "auth"), "existingSecret")
	if name == "" {
		name = releasePrefix + inst.name + pskSecretSuffix
	}
	return r.readSecretKey(ctx, inst.namespace, name, pskSecretKey)
}

// readAPIToken reads the management-API token from the chart-generated
// site-router-<name>-api-key Secret (key token, D6). The controller reads it; the
// tenant's RBAC cannot.
func (r *SiteRouterReconciler) readAPIToken(ctx context.Context, inst *instance) (string, bool, error) {
	name := releasePrefix + inst.name + apiKeySecretSuffix
	return r.readSecretKey(ctx, inst.namespace, name, apiTokenSecretKey)
}

// readSecretKey reads a single key from a namespaced Secret through the uncached
// reader (Secrets are deliberately not cached — CacheByObject). A missing Secret
// or missing/empty key yields ok=false, not an error, so a not-yet-created Secret
// is a requeue rather than a hard failure.
func (r *SiteRouterReconciler) readSecretKey(ctx context.Context, namespace, name, key string) (string, bool, error) {
	secret := &corev1.Secret{}
	if err := r.reader().Get(ctx, types.NamespacedName{Namespace: namespace, Name: name}, secret); err != nil {
		if apierrors.IsNotFound(err) {
			return "", false, nil
		}
		return "", false, fmt.Errorf("get secret %s/%s: %w", namespace, name, err)
	}
	v, ok := secret.Data[key]
	if !ok || len(v) == 0 {
		return "", false, nil
	}
	return string(v), true, nil
}

// --- spec.values decoding helpers -----------------------------------------
//
// spec.values decodes (decodeValues → json.Unmarshal) into map[string]interface{}
// with float64 numbers, []interface{} arrays and map[string]interface{} objects.
// These helpers read a single field defensively: a wrong-typed or absent value
// yields the zero value rather than panicking.

func asMap(v interface{}) map[string]interface{} {
	m, _ := v.(map[string]interface{})
	return m
}

func mapGet(v interface{}, key string) interface{} {
	return asMap(v)[key]
}

func stringField(v interface{}, key string) string {
	s, _ := mapGet(v, key).(string)
	return s
}

func boolField(v interface{}, key string) bool {
	b, _ := mapGet(v, key).(bool)
	return b
}

func intField(v interface{}, key string) int64 {
	switch n := mapGet(v, key).(type) {
	case float64:
		return int64(n)
	case int64:
		return n
	case int:
		return int64(n)
	default:
		return 0
	}
}

func sliceOf(v interface{}) []interface{} {
	s, _ := v.([]interface{})
	return s
}

// redactSecrets replaces every non-empty secret occurrence in s with a fixed
// placeholder, so a message derived from a VyOS API error (which can echo the
// failing set-command, PSK and api-key token included) can be safely recorded to
// an Event, a condition or a log. Empty secrets are ignored (nothing to redact),
// so a call with an unresolved PSK/token is a harmless no-op.
func redactSecrets(s string, secrets ...string) string {
	const placeholder = "[redacted]"
	for _, secret := range secrets {
		if secret == "" {
			continue
		}
		s = strings.ReplaceAll(s, secret, placeholder)
	}
	return s
}

// truncErr collapses an error message to a single, rune-safe, length-capped line
// suitable for a Kubernetes Event/condition. VyOS API errors can be long
// multi-line dumps. Ported from the reference implementation.
func truncErr(err error) string {
	return truncMsg(err.Error())
}

// truncMsg is truncErr for an already-assembled message string. It exists so a
// message can be REDACTED before it is truncated: truncating first would let a
// secret straddling the length boundary keep its prefix past the cut (redaction
// can no longer match the whole value), leaking it into a tenant-readable Event.
func truncMsg(s string) string {
	const maxLen = 256
	msg := strings.ReplaceAll(s, "\n", " ")
	if len(msg) <= maxLen {
		return msg
	}
	cut := maxLen
	for cut > 0 && !utf8.RuneStart(msg[cut]) {
		cut--
	}
	return msg[:cut] + "…"
}
