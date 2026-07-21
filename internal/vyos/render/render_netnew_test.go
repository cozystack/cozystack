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

// These tests encode the Phase-1 net-new render requirements that do not
// exist in the cozyportal reference (T02 Acceptance). The render logic
// for MSS clamp, the tunnel-ingress source filter and forced UDP
// encapsulation is implemented in Phase B — until then those three tests
// fail on the missing operations. TestRender_NoNATOperations is a
// standing guard: it validates the routed subset (DECISIONS.md D3) that
// is already in place in Phase A and therefore passes.
package render_test

import (
	"strconv"
	"strings"
	"testing"

	"github.com/cozystack/cozystack/internal/vyos/render"
)

// routedTunnel is a resolved single-peer IPsec tunnel with no NAT/public
// hints — used to prove behaviour is unconditional for routed inputs.
func routedTunnel() render.IPSecTunnel {
	return render.IPSecTunnel{
		Description:   "site-a",
		PeerAddress:   "203.0.113.10",
		PSK:           "secretpsk",
		LocalSubnets:  []string{"10.0.0.0/24"},
		RemoteSubnets: []string{"172.31.0.0/16"},
	}
}

// TestRenderMSSClamp_DerivedFromInputMTU encodes T02 Acceptance:
// "MSS clamp emitted with the derived value". The clamp value must be
// computed from Inputs.OverlayMTU (not hardcoded) and attached to the
// caller-resolved TunnelDevice (not a hardcoded eth0).
func TestRenderMSSClamp_DerivedFromInputMTU(t *testing.T) {
	t.Parallel()

	in := baseInputs()
	in.TunnelDevice = "eth1"
	in.OverlayMTU = 1400
	in.Tunnels = []render.IPSecTunnel{routedTunnel()}

	ops := render.Render(in)

	want := strconv.Itoa(1400 - render.MSSClampOverhead) // 1360
	if !containsSet(ops, "firewall/options/interface/eth1/adjust-mss", want) {
		t.Errorf("expected adjust-mss=%s on the resolved tunnel device eth1, ops: %+v", want, ops)
	}
}

// TestRenderMSSClamp_DefaultsToDesignClamp encodes the design default:
// tunnel MTU 1320 → clamp 1280 when Inputs.OverlayMTU is unset.
func TestRenderMSSClamp_DefaultsToDesignClamp(t *testing.T) {
	t.Parallel()

	in := baseInputs()
	in.TunnelDevice = "eth1"
	in.Tunnels = []render.IPSecTunnel{routedTunnel()}

	ops := render.Render(in)

	want := strconv.Itoa(render.DefaultOverlayMTU - render.MSSClampOverhead) // 1280
	if !containsSet(ops, "firewall/options/interface/eth1/adjust-mss", want) {
		t.Errorf("expected default adjust-mss=%s (1320-40), ops: %+v", want, ops)
	}
}

// TestRenderTunnelIngressFilter_AllowsRemoteCIDRsAndDropsRest encodes T02
// Acceptance: "tunnel-ingress source filter accepts declared remoteCIDRs
// and drops others". Established/related is accepted, each remote CIDR is
// accepted by source address, everything else is dropped by the ruleset
// default-action, and the ruleset attaches to the resolved TunnelDevice.
func TestRenderTunnelIngressFilter_AllowsRemoteCIDRsAndDropsRest(t *testing.T) {
	t.Parallel()

	in := baseInputs()
	in.TunnelDevice = "eth1"
	in.RemoteCIDRs = []string{"172.31.0.0/16", "10.10.0.0/16"}
	in.Tunnels = []render.IPSecTunnel{routedTunnel()}

	ops := render.Render(in)

	rs := "firewall/name/" + render.TunnelIngressRuleSet

	// Everything outside the allow-list is dropped.
	if !containsSet(ops, rs+"/default-action", "drop") {
		t.Errorf("expected tunnel-ingress default-action=drop, ops: %+v", ops)
	}

	// Established/related return traffic is accepted first.
	if !containsSet(ops, rs+"/rule/5/action", "accept") ||
		!containsSet(ops, rs+"/rule/5/state/established", "enable") {
		t.Errorf("expected established/related accept as the first tunnel-ingress rule, ops: %+v", ops)
	}

	// Each declared remote CIDR is accepted by source address.
	if !containsSet(ops, rs+"/rule/10/action", "accept") ||
		!containsSet(ops, rs+"/rule/10/source/address", "172.31.0.0/16") {
		t.Errorf("expected accept for first remote CIDR 172.31.0.0/16, ops: %+v", ops)
	}

	if !containsSet(ops, rs+"/rule/20/source/address", "10.10.0.0/16") {
		t.Errorf("expected accept for second remote CIDR 10.10.0.0/16, ops: %+v", ops)
	}

	// The ruleset is bound to the caller-resolved tunnel device inbound.
	if !containsSet(ops, "interfaces/ethernet/eth1/firewall/in/name", render.TunnelIngressRuleSet) {
		t.Errorf("expected tunnel-ingress ruleset bound to eth1 inbound, ops: %+v", ops)
	}
}

// TestRenderIPSec_ForcesUDPEncapsulation encodes T02 Acceptance: "IPsec
// render forces UDP encapsulation". Native ESP is dropped pod-to-pod by
// Cilium conntrack, so NAT-T (ESP-in-UDP) is forced unconditionally on
// every peer regardless of any detected NAT.
func TestRenderIPSec_ForcesUDPEncapsulation(t *testing.T) {
	t.Parallel()

	in := baseInputs()
	in.ExternalIP = "203.0.113.15"
	in.Tunnels = []render.IPSecTunnel{routedTunnel()}

	ops := render.Render(in)

	if !containsSet(ops, "vpn/ipsec/site-to-site/peer/203.0.113.10/force-encapsulation", "enable") {
		t.Errorf("expected forced ESP-in-UDP (force-encapsulation enable) on the peer, ops: %+v", ops)
	}
}

// TestRender_NoNATOperations encodes T02 Acceptance: "NAT domains absent
// from routed inputs". A fully-populated routed configuration must never
// emit any `nat` operation — set or delete (DECISIONS.md D3: NAT is
// Phase-2 site-gateway). This guard already holds in Phase A.
func TestRender_NoNATOperations(t *testing.T) {
	t.Parallel()

	in := baseInputs()
	in.ManagementCIDR = "10.244.0.0/16"
	in.ExternalIP = "203.0.113.15"
	in.TunnelDevice = "eth1"
	in.RemoteCIDRs = []string{"172.31.0.0/16"}
	in.StaticRoutes = []render.StaticRoute{
		{Destination: "192.168.50.0/24", NextHop: "10.0.0.254"},
	}
	in.Tunnels = []render.IPSecTunnel{routedTunnel()}
	in.BGP = &render.BGPConfig{
		Asn:   65001,
		Peers: []render.BGPPeer{{PeerAddress: "203.0.113.1", PeerAsn: 65000}},
	}

	ops := render.Render(in)

	for _, op := range ops {
		if len(op.Path) > 0 && op.Path[0] == "nat" {
			t.Errorf("expected no nat operations in routed render, got %s %s",
				op.Op, strings.Join(op.Path, "/"))
		}
	}
}
