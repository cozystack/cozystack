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

// These tests encode the T08 guest-side security guards, written FIRST (Phase
// A): the render logic that satisfies them lands in Phase B, so until then the
// assertions here fail on the missing / still-old operations.
//
// THE CARRIED REDESIGN (M2 review). The tunnel-ingress source filter used to be
// bound to the pod-NIC inbound hook
// (`interfaces ethernet <dev> firewall in name TUNNEL-INGRESS`). That is wrong:
// on a single-homed routed gateway the pod NIC also carries locally-originated
// tenant→remote egress (cluster-source, plaintext, pre-IPsec). Binding the
// source allow-list to pod-NIC inbound drops that egress before it can be
// encrypted, because a cluster source (e.g. 10.244.x.x) is never a member of
// remoteCIDRs. The redesign filters ONLY IPsec-decrypted ingress by source:
//
//   - the source allow-list stays in the named rule set TUNNEL-INGRESS
//     (`firewall name TUNNEL-INGRESS`), so the controller's
//     confirmSourceFilterActive Retrieve path is unchanged;
//   - it is reached via a `firewall forward` rule that matches IPsec-decrypted
//     packets (`ipsec match-ipsec`) and jumps to the named set — so the
//     source allow-list + default-drop apply to decrypted-from-tunnel traffic
//     ONLY;
//   - locally-originated (non-IPsec) forward traffic matches a separate
//     `ipsec match-none` accept and is NOT subject to the source-filter drop;
//   - the pod-NIC-inbound binding is gone.
//
// TODO(T13): the exact VyOS 1.5 forward-filter + ipsec-match leaf syntax is
// PROVISIONAL — validate `firewall (ipv4) forward (filter) rule N ipsec
// match-ipsec|match-none`, `action jump`, `jump-target`, and the whole
// `firewall input`/`firewall name` family against the shipped image live. The
// render keeps all version-specific paths behind single helpers so the syntax
// swaps in one place.
package render_test

import (
	"strings"
	"testing"

	"github.com/cozystack/cozystack/internal/vyos"
	"github.com/cozystack/cozystack/internal/vyos/render"
)

// forwardFilterBase is the provisional base path of the platform-owned guest
// forward-chain filter (TODO(T13): validate the VyOS 1.5 leaf path).
const forwardFilterBase = "firewall/forward"

// ipsecMatchForwardRule scans ops for a `firewall forward` rule that carries the
// given ipsec match flag (match-ipsec or match-none) as a value-less leaf and
// returns that rule's number, or "" when absent. It lets the assertions locate
// the redesigned binding without pinning a specific rule number.
func ipsecMatchForwardRule(ops []vyos.Operation, matchFlag string) string {
	for _, op := range ops {
		if op.Op != vyos.OpSet {
			continue
		}
		p := op.Path
		// .../forward/rule/<N>/ipsec/<matchFlag>
		if len(p) >= 6 &&
			p[0] == "firewall" && p[1] == "forward" && p[2] == "rule" &&
			p[len(p)-2] == "ipsec" && p[len(p)-1] == matchFlag {
			return p[3]
		}
	}
	return ""
}

// anyInterfaceInboundFirewall reports whether ops bind ANY firewall rule set to a
// per-interface inbound hook (`interfaces ethernet <dev> firewall in name ...`).
// The redesign forbids this — it is the M2 bug being removed.
func anyInterfaceInboundFirewall(ops []vyos.Operation) bool {
	for _, op := range ops {
		p := op.Path
		if len(p) >= 6 &&
			p[0] == "interfaces" && p[1] == "ethernet" &&
			p[3] == "firewall" && p[4] == "in" && p[5] == "name" {
			return true
		}
	}
	return false
}

// tunnelIngressAccepts inspects the TUNNEL-INGRESS named rule set and returns the
// set of source addresses across its source-accept rules, the set of destination
// addresses those rules carry, and whether EVERY source-accept is
// destination-constrained. A source-accept without a destination is the R1
// world-egress bug: a jumped-chain accept is a terminal verdict, so a source-only
// accept forwards a valid-source packet to any destination — bypassing the
// forward-chain default-drop. It scans by rule number so it does not pin a
// specific numbering.
func tunnelIngressAccepts(ops []vyos.Operation, ruleset string) (sources, dests map[string]bool, allConstrained bool) {
	prefix := "firewall/name/" + ruleset + "/rule/"
	srcByRule := map[string]string{}
	dstByRule := map[string]string{}
	for _, op := range ops {
		if op.Op != vyos.OpSet {
			continue
		}
		p := strings.Join(op.Path, "/")
		if !strings.HasPrefix(p, prefix) {
			continue
		}
		parts := strings.SplitN(strings.TrimPrefix(p, prefix), "/", 2)
		if len(parts) != 2 {
			continue
		}
		switch parts[1] {
		case "source/address":
			srcByRule[parts[0]] = op.Value
		case "destination/address":
			dstByRule[parts[0]] = op.Value
		}
	}
	sources, dests = map[string]bool{}, map[string]bool{}
	allConstrained = true
	for n, s := range srcByRule {
		sources[s] = true
		if d, ok := dstByRule[n]; ok {
			dests[d] = true
		} else {
			allConstrained = false
		}
	}
	return sources, dests, allConstrained
}

// TestRenderTunnelIngress_FiltersDecryptedBySourceViaForwardFilter encodes the
// T08 redesign + Acceptance (a)+(b)+(d): decrypted-from-tunnel traffic is
// filtered by source against remoteCIDRs — a source outside the list is dropped
// (the named-set default-action), a source inside is accepted — and the guard
// binds via a `firewall forward` ipsec-match jump, NOT a pod-NIC-inbound hook.
func TestRenderTunnelIngress_FiltersDecryptedBySourceViaForwardFilter(t *testing.T) {
	t.Parallel()

	in := baseInputs()
	in.TunnelDevice = "eth0" // single-homed pod NIC
	in.RemoteCIDRs = []string{"172.31.0.0/16", "10.10.0.0/16"}
	in.TenantNetworkCIDRs = []string{"10.244.0.0/16", "10.96.0.0/16"} // cluster pod + service
	in.Tunnels = []render.IPSecTunnel{routedTunnel()}

	ops := render.Render(in)

	rs := "firewall/name/" + render.TunnelIngressRuleSet

	// (a) A source NOT in remoteCIDRs falls through to the named-set default
	// drop. The allow-list lives in the named set (unchanged Retrieve path).
	if !containsSet(ops, rs+"/default-action", "drop") {
		t.Errorf("expected TUNNEL-INGRESS default-action=drop (decrypted, source ∉ remoteCIDRs → dropped), ops: %+v", ops)
	}

	// (b) Each declared remote CIDR is accepted by source address AND every
	// source-accept is destination-constrained to a tenant network on the SAME
	// rule (the R1 world-egress fix): a source-only accept would forward a
	// valid-source packet to any destination, bypassing the forward default-drop.
	sources, dests, allConstrained := tunnelIngressAccepts(ops, render.TunnelIngressRuleSet)
	if !sources["172.31.0.0/16"] || !sources["10.10.0.0/16"] {
		t.Errorf("expected a source-accept per declared remote CIDR, sources %v", sources)
	}
	if !allConstrained {
		t.Errorf("every TUNNEL-INGRESS source-accept must carry a tenant-network destination (R1), ops: %+v", ops)
	}
	if !dests["10.244.0.0/16"] || !dests["10.96.0.0/16"] {
		t.Errorf("expected the source-accepts destination-constrained to the tenant networks, got dests %v", dests)
	}

	// Established/related return traffic is accepted before any source match.
	if !containsSet(ops, rs+"/rule/5/action", "accept") ||
		!containsSet(ops, rs+"/rule/5/state/established", "enable") {
		t.Errorf("expected established/related accept as the first named-set rule, ops: %+v", ops)
	}

	// (d) The guard reaches the named set via a forward-filter rule that matches
	// IPsec-decrypted traffic (`ipsec match-ipsec`) and jumps to it.
	n := ipsecMatchForwardRule(ops, "match-ipsec")
	if n == "" {
		t.Fatalf("expected a `firewall forward` rule matching IPsec-decrypted traffic (ipsec match-ipsec), ops: %+v", ops)
	}
	rule := forwardFilterBase + "/rule/" + n
	if !containsSet(ops, rule+"/action", "jump") {
		t.Errorf("expected the ipsec-match forward rule action=jump, ops: %+v", ops)
	}
	if !containsSet(ops, rule+"/jump-target", render.TunnelIngressRuleSet) {
		t.Errorf("expected the ipsec-match forward rule to jump-target TUNNEL-INGRESS, ops: %+v", ops)
	}

	// (d) The pod-NIC-inbound binding (the M2 bug) must be gone entirely.
	if anyInterfaceInboundFirewall(ops) {
		t.Errorf("expected NO per-interface inbound firewall binding after the redesign, ops: %+v", ops)
	}
}

// TestRenderTunnelIngress_NonTenantDestinationNotAccepted proves the R1 fix: a
// decrypted packet with a VALID remote source but a WORLD / non-tenant
// destination is NOT accepted by the source rule. Every source-accept is
// destination-constrained to a tenant network, so such a packet matches no accept
// and falls through to the default-action drop — the gateway cannot be turned into
// unintended internet egress by a good-source packet.
func TestRenderTunnelIngress_NonTenantDestinationNotAccepted(t *testing.T) {
	t.Parallel()

	in := baseInputs()
	in.TunnelDevice = "eth0"
	in.RemoteCIDRs = []string{"172.31.0.0/16"}
	in.TenantNetworkCIDRs = []string{"10.244.0.0/16", "10.96.0.0/16"}
	in.Tunnels = []render.IPSecTunnel{routedTunnel()}

	ops := render.Render(in)
	rs := "firewall/name/" + render.TunnelIngressRuleSet

	sources, dests, allConstrained := tunnelIngressAccepts(ops, render.TunnelIngressRuleSet)
	if !sources["172.31.0.0/16"] {
		t.Fatalf("expected a source-accept for the declared remote CIDR, sources %v", sources)
	}
	if !allConstrained {
		t.Errorf("a source-accept without a destination constraint would forward a valid-source packet to the world (R1), ops: %+v", ops)
	}
	// The only accepted destinations are the tenant networks — never the world.
	for d := range dests {
		if d != "10.244.0.0/16" && d != "10.96.0.0/16" {
			t.Errorf("unexpected non-tenant destination %q accepted by a source rule, ops: %+v", d, ops)
		}
	}
	if dests["0.0.0.0/0"] {
		t.Errorf("a world destination must never be accepted by a source rule, ops: %+v", ops)
	}
	// Fail-closed backstop: everything not matched (including a world destination)
	// is dropped by the named-set default-action.
	if !containsSet(ops, rs+"/default-action", "drop") {
		t.Errorf("expected default-action drop, ops: %+v", ops)
	}
}

// TestRenderTunnelIngress_LocalEgressNotCaught encodes Acceptance (c):
// locally-originated tenant→remote egress (non-IPsec forward, cluster-source)
// must NOT be caught by the source-filter drop. The redesign scopes the source
// allow-list to IPsec-decrypted traffic only; non-IPsec forward traffic is
// admitted by a separate `ipsec match-none` accept, so a cluster-source packet
// (never a member of remoteCIDRs) is no longer black-holed before IPsec.
func TestRenderTunnelIngress_LocalEgressNotCaught(t *testing.T) {
	t.Parallel()

	in := baseInputs()
	in.TunnelDevice = "eth0"
	in.RemoteCIDRs = []string{"172.31.0.0/16"}
	in.Tunnels = []render.IPSecTunnel{routedTunnel()}

	ops := render.Render(in)

	// Non-IPsec forward traffic (the plaintext, pre-IPsec local egress) is
	// explicitly accepted, so it is not subject to the source-filter drop.
	n := ipsecMatchForwardRule(ops, "match-none")
	if n == "" {
		t.Fatalf("expected a `firewall forward` rule matching non-IPsec traffic (ipsec match-none) to admit local egress, ops: %+v", ops)
	}
	if !containsSet(ops, forwardFilterBase+"/rule/"+n+"/action", "accept") {
		t.Errorf("expected the non-IPsec (match-none) forward rule action=accept, ops: %+v", ops)
	}

	// The source allow-list must NOT be applied to all inbound pod-NIC traffic
	// (the M2 bug): that path dropped cluster-source egress before IPsec.
	if anyInterfaceInboundFirewall(ops) {
		t.Errorf("expected NO pod-NIC-inbound source-filter binding (it dropped local egress), ops: %+v", ops)
	}

	// Defensively: no forward rule drops by cluster source address — the guard
	// filters decrypted ingress by remote source, never local egress by cluster
	// source.
	for _, op := range ops {
		p := strings.Join(op.Path, "/")
		if op.Op == vyos.OpSet && strings.HasPrefix(p, forwardFilterBase) &&
			strings.HasSuffix(p, "/action") && op.Value == "drop" &&
			strings.Contains(p, "/rule/") {
			t.Errorf("unexpected explicit per-rule drop in the forward filter (must rely on scoped default-action, not a broad drop rule): %s", p)
		}
	}
}

// TestRenderTunnelIngress_EmptyRemoteCIDRsFailsClosed proves the fail-closed
// guarantee survives the redesign: a resolved tunnel device with no declared
// remotes still stamps the named-set default-drop, the established/related
// accept and the ipsec-match forward jump, so decrypted traffic is dropped
// (fail CLOSED) rather than left unfiltered. No per-CIDR accept exists.
func TestRenderTunnelIngress_EmptyRemoteCIDRsFailsClosed(t *testing.T) {
	t.Parallel()

	in := baseInputs()
	in.TunnelDevice = "eth0"
	in.RemoteCIDRs = nil
	in.Tunnels = []render.IPSecTunnel{routedTunnel()}

	ops := render.Render(in)

	rs := "firewall/name/" + render.TunnelIngressRuleSet

	if !containsSet(ops, rs+"/default-action", "drop") {
		t.Errorf("expected fail-closed default-action=drop with empty RemoteCIDRs, ops: %+v", ops)
	}
	if !containsSet(ops, rs+"/rule/5/action", "accept") ||
		!containsSet(ops, rs+"/rule/5/state/established", "enable") {
		t.Errorf("expected established/related accept even with empty RemoteCIDRs, ops: %+v", ops)
	}
	if containsSet(ops, rs+"/rule/10/source/address", "*") {
		t.Errorf("expected no source-accept rule with empty RemoteCIDRs, ops: %+v", ops)
	}
	// The forward-filter ipsec-match jump is still emitted so the drop takes
	// effect on decrypted traffic.
	if ipsecMatchForwardRule(ops, "match-ipsec") == "" {
		t.Errorf("expected the ipsec-match forward jump even with empty RemoteCIDRs (fail-closed), ops: %+v", ops)
	}
	if anyInterfaceInboundFirewall(ops) {
		t.Errorf("expected NO per-interface inbound firewall binding, ops: %+v", ops)
	}
}

// TestRenderForwardFilter_DefaultDenyTunnelToWorld encodes T08 §3: routed mode
// advertises specific remote networks and never a default route out the tunnel,
// so the guest forward chain denies by default (default-action drop). Only the
// recognised flows — established/related, non-IPsec local egress, and
// IPsec-decrypted ingress (source-filtered) — are admitted; everything else,
// including tunnel→world, is dropped by the default-action.
func TestRenderForwardFilter_DefaultDenyTunnelToWorld(t *testing.T) {
	t.Parallel()

	in := baseInputs()
	in.TunnelDevice = "eth0"
	in.RemoteCIDRs = []string{"172.31.0.0/16"}
	in.Tunnels = []render.IPSecTunnel{routedTunnel()}

	ops := render.Render(in)

	if !containsSet(ops, forwardFilterBase+"/default-action", "drop") {
		t.Errorf("expected `firewall forward` default-action=drop (default-deny tunnel→world), ops: %+v", ops)
	}

	// Routed mode never installs a blanket forward accept to the world / default
	// route — that would defeat the default-deny.
	if containsSet(ops, forwardFilterBase+"/rule/10/destination/address", "0.0.0.0/0") ||
		containsSet(ops, forwardFilterBase+"/rule/20/destination/address", "0.0.0.0/0") {
		t.Errorf("expected NO blanket 0.0.0.0/0 destination accept in the forward filter, ops: %+v", ops)
	}
}

// TestRenderForwardFilter_AbsentWithoutTunnelDevice guards that the forward
// filter (and the whole guest guard family) is only emitted once a tunnel
// device is resolved — an unconfigured render carries no forward-chain rules.
func TestRenderForwardFilter_AbsentWithoutTunnelDevice(t *testing.T) {
	t.Parallel()

	in := baseInputs() // no TunnelDevice
	in.ManagementCIDR = "10.244.0.0/16"

	ops := render.Render(in)

	for _, op := range ops {
		if op.Op == vyos.OpSet && len(op.Path) >= 2 &&
			op.Path[0] == "firewall" && op.Path[1] == "forward" {
			t.Errorf("expected no forward-filter ops without a resolved tunnel device, got %s", strings.Join(op.Path, "/"))
		}
	}
}

// TestRenderManagementAPIDrop_OnIPsecDecryptedInput encodes T08 §4 Boundary A:
// a packet decrypted by VyOS and addressed to the guest's own management API
// (SSH 22 / HTTPS 443) does NOT cross the pod veth where Cilium enforces, so
// the guest must drop it. The render emits a local-input (`firewall input`)
// drop that matches IPsec-decrypted traffic to the management ports — matching
// the decrypted property covers every tunnel interface, including any created
// dynamically, without enumerating devices.
func TestRenderManagementAPIDrop_OnIPsecDecryptedInput(t *testing.T) {
	t.Parallel()

	in := baseInputs()
	in.TunnelDevice = "eth0"
	in.ManagementCIDR = "10.244.0.0/16"
	in.RemoteCIDRs = []string{"172.31.0.0/16"}
	in.Tunnels = []render.IPSecTunnel{routedTunnel()}

	ops := render.Render(in)

	// Locate the input-filter rule that matches IPsec-decrypted traffic.
	var ruleNum string
	for _, op := range ops {
		p := op.Path
		if op.Op == vyos.OpSet && len(p) >= 6 &&
			p[0] == "firewall" && p[1] == "input" && p[2] == "rule" &&
			p[len(p)-2] == "ipsec" && p[len(p)-1] == "match-ipsec" {
			ruleNum = p[3]
			break
		}
	}
	if ruleNum == "" {
		t.Fatalf("expected a `firewall input` rule matching IPsec-decrypted traffic (Boundary A), ops: %+v", ops)
	}

	base := "firewall/input/rule/" + ruleNum
	if !containsSet(ops, base+"/action", "drop") {
		t.Errorf("expected the IPsec-decrypted management-API rule to drop, ops: %+v", ops)
	}
	if !containsSet(ops, base+"/destination/port", "22,443") {
		t.Errorf("expected the drop to cover the management ports 22,443, ops: %+v", ops)
	}
}

// TestRenderManagementAPIDrop_IndependentOfManagementCIDR proves Boundary A is
// tied to the tunnel existing, NOT to the management firewall: even with an
// empty ManagementCIDR (the --allow-open-management path, no management ACL),
// a resolved tunnel device still stamps the IPsec-decrypted management-API
// drop, because a decrypted packet to the API must be dropped regardless of
// whether the management ACL is present.
func TestRenderManagementAPIDrop_IndependentOfManagementCIDR(t *testing.T) {
	t.Parallel()

	in := baseInputs()
	in.TunnelDevice = "eth0"
	in.ManagementCIDR = "" // open-management path: no management ACL rendered
	in.RemoteCIDRs = []string{"172.31.0.0/16"}
	in.Tunnels = []render.IPSecTunnel{routedTunnel()}

	ops := render.Render(in)

	found := false
	for _, op := range ops {
		p := op.Path
		if op.Op == vyos.OpSet && len(p) >= 6 &&
			p[0] == "firewall" && p[1] == "input" && p[2] == "rule" &&
			p[len(p)-2] == "ipsec" && p[len(p)-1] == "match-ipsec" {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected the Boundary A IPsec-decrypted management-API drop even with empty ManagementCIDR, ops: %+v", ops)
	}
}
