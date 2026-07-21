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

// Package render maps a fully-resolved SiteRouter routed configuration
// into VyOS configuration operations consumed by the vyos.Client.
//
// The render is pure: it has no I/O and produces a deterministic slice
// of vyos.Operation values from a fully-resolved Inputs struct. The
// SiteRouter controller is responsible for resolving HelmRelease values,
// discovered NIC devices and Secret-backed credentials into Inputs
// before calling Render.
//
// Scope is the Phase-1 routed feature set (DECISIONS.md D3/D12):
// interfaces, management firewall, IPsec site-to-site (forced UDP
// encapsulation), static routes and BGP, plus the two net-new domains
// (MSS clamp, tunnel-ingress source allow-list). NAT/DNAT (Phase 2
// site-gateway) and HA/VRRP (Phase 4) are deliberately absent.
package render

import (
	"strconv"
	"strings"

	"github.com/cozystack/cozystack/internal/vyos"
)

const (
	// ikeGroupName / espGroupName are the constant group names used for
	// every tunnel. Per-tunnel parameters live inside the per-tunnel
	// proposal, so reusing a single group name is fine and keeps the
	// VyOS tree compact.
	ikeGroupName = "ROUTER-IKE"
	espGroupName = "ROUTER-ESP"
)

// IKEVersion is the IKE key-exchange version emitted for the ike-group.
type IKEVersion string

const (
	IKEVersionV1 IKEVersion = "ikev1"
	IKEVersionV2 IKEVersion = "ikev2"
)

// Interface is one desired VyOS ethernet interface. Addressing is
// resolved by the controller before Render: an uplink uses DHCP, a
// routed/LAN interface carries a static Address plus the NetworkCIDR it
// belongs to (the CIDR supplies the mask suffix).
type Interface struct {
	// Name is the human-facing interface name; it is emitted as the VyOS
	// interface description and is the key for the device map.
	Name string

	// Uplink selects DHCP addressing when true (the interface facing the
	// cluster / tunnel underlay).
	Uplink bool

	// Address is the static IPv4 address for a routed interface, e.g.
	// "10.0.0.1" (no mask). Ignored when Uplink is true. Empty means the
	// controller has not resolved an address yet — the address op is
	// skipped and the reconciler requeues.
	Address string

	// NetworkCIDR is the CIDR the Address belongs to, e.g. "10.0.0.0/24".
	// Only its mask suffix ("/24") is used. Empty (unresolved network)
	// skips the address op rather than emitting an invalid maskless set.
	NetworkCIDR string
}

// InterfaceStatus is a controller-discovered name → kernel-device
// binding (from `show interfaces detail` MAC matching). It overrides
// the positional device guess and drives removed-interface cleanup.
type InterfaceStatus struct {
	// Name matches an Interface.Name.
	Name string

	// Device is the discovered kernel device (e.g. "eth0"). Empty means
	// discovery has not resolved this interface yet.
	Device string
}

// StaticRoute is one `protocols static route` entry.
type StaticRoute struct {
	Description string
	Destination string // CIDR, e.g. "192.168.50.0/24"
	NextHop     string // IPv4 next-hop address
}

// IKEParams carries the IKE proposal for a tunnel. Zero-valued fields
// fall back to the design defaults in normaliseIKE.
type IKEParams struct {
	Version    IKEVersion
	Encryption string
	Hash       string
	DhGroup    int
	Lifetime   int
}

// ESPParams carries the ESP proposal for a tunnel. Zero-valued fields
// fall back to the design defaults in normaliseESP.
type ESPParams struct {
	Encryption string
	Hash       string
	PfsGroup   int
	Lifetime   int
}

// IPSecTunnel is one site-to-site IPsec tunnel. The Phase-1 schema keeps
// tunnel.type a single-value enum ("ipsec"); only ipsec is implemented
// here (DECISIONS.md D12), so the type is implicit.
type IPSecTunnel struct {
	Description   string
	PeerAddress   string
	PSK           string
	LocalSubnets  []string
	RemoteSubnets []string
	IKE           *IKEParams
	ESP           *ESPParams
}

// BGPTimers carries the per-neighbor keepalive/hold timers.
type BGPTimers struct {
	Keepalive int
	Hold      int
}

// BGPPeer is one BGP neighbor.
type BGPPeer struct {
	Description        string
	PeerAddress        string
	PeerAsn            int64
	AdvertisedNetworks []string
	Timers             *BGPTimers
}

// BGPConfig is the router-wide BGP configuration.
type BGPConfig struct {
	Asn      int64
	RouterID string
	Peers    []BGPPeer
}

// Inputs contains everything Render needs to produce VyOS operations.
// It is fully resolved: the controller substitutes discovered devices,
// resolved addresses and Secret-backed credentials before calling Render.
type Inputs struct {
	// Interfaces is the ordered desired-state interface list. Index
	// position seeds the positional device map (eth0, eth1, …).
	Interfaces []Interface

	// DiscoveredInterfaces carries the controller's MAC-discovery result.
	// A non-empty Device overrides the positional guess and drives
	// removed-interface cleanup.
	DiscoveredInterfaces []InterfaceStatus

	// StaticRoutes are rendered under `protocols static route`.
	StaticRoutes []StaticRoute

	// Tunnels are site-to-site IPsec tunnels. Empty disables IPsec (and
	// the IKE/ESP firewall accept rules).
	Tunnels []IPSecTunnel

	// BGP is the optional BGP configuration (nil disables BGP).
	BGP *BGPConfig

	// BGPPasswords resolves BGPPeer.PeerAddress → its MD5 password. Kept
	// out of the peer struct so the credential source can change without
	// touching the render contract.
	BGPPasswords map[string]string

	// ExternalIP is the IPv4 address the router advertises on its uplink;
	// used as the local-address of IPsec tunnels. Empty leaves the field
	// unset and lets VyOS auto-detect.
	ExternalIP string

	// ManagementCIDR is the CIDR the controller reaches VyOS from. The
	// renderer emits firewall input rules accepting SSH/HTTPS only from
	// this CIDR; everything else is dropped. Empty disables the firewall
	// block — only safe in test environments (fail-closed at the flag).
	ManagementCIDR string

	// --- Net-new Phase-1 fields (render implemented in Phase B) ---

	// OverlayMTU is the tunnel-path overlay MTU that the TCP MSS clamp is
	// derived from. Zero selects DefaultOverlayMTU.
	OverlayMTU int

	// TunnelDevice is the resolved VyOS kernel device carrying tunnel /
	// forwarded traffic (discovered at runtime — never hardcoded). Both
	// the MSS clamp and the tunnel-ingress source filter attach to it.
	TunnelDevice string

	// RemoteCIDRs is the set of declared remote subnets. The
	// tunnel-ingress source filter accepts traffic sourced from these and
	// drops everything else arriving on TunnelDevice.
	RemoteCIDRs []string
}

const (
	// DefaultOverlayMTU is the design default overlay MTU for the IPsec
	// tunnel path used when Inputs.OverlayMTU is unset.
	DefaultOverlayMTU = 1320

	// MSSClampOverhead is the IPv4 + TCP header overhead (20 + 20)
	// subtracted from the overlay MTU to derive the TCP MSS clamp value.
	// 1320 - 40 = 1280 (the design default clamp).
	MSSClampOverhead = 40

	// TunnelIngressRuleSet is the name of the platform-owned firewall
	// rule set that guards traffic arriving on the tunnel device.
	TunnelIngressRuleSet = "TUNNEL-INGRESS"
)

// Render returns the full set of VyOS operations needed to realise the
// routed SiteRouter configuration. The slice has two halves: idempotent
// `delete` ops on controller-managed subtrees first, then `set` ops that
// recreate the desired configuration. VyOS applies the batch in a single
// transaction, so the net effect is "replace the subtree".
//
// This is the only way to make spec-shrinks (removed IPSec tunnel,
// removed BGP peer, removed static route) take effect — without the
// leading deletes the old VyOS rules would linger as zombies.
//
// Interface-level removals are handled via DiscoveredInterfaces: entries
// whose name no longer appears in Interfaces but that carry a discovered
// device get their address and description deleted
// (renderRemovedInterfaceCleanup).
func Render(in Inputs) []vyos.Operation {
	ops := make([]vyos.Operation, 0, 64)
	ops = append(ops, deleteManagedSubtrees(in)...)
	ops = append(ops, renderRemovedInterfaceCleanup(in)...)
	ops = append(ops, renderInterfaces(in)...)
	ops = append(ops, renderManagementFirewall(in)...)
	ops = append(ops, renderMSSClamp(in)...)
	ops = append(ops, renderTunnelIngressFilter(in)...)
	ops = append(ops, renderStaticRoutes(in)...)
	ops = append(ops, renderIPSec(in)...)
	ops = append(ops, renderBGP(in)...)

	return ops
}

// deleteManagedSubtrees emits idempotent `delete` ops for every subtree
// this renderer fully owns. VyOS treats `delete` on a non-existent path
// as a no-op, so emitting these unconditionally is safe on a fresh
// router and a no-op once the controller has applied the same operations
// before.
//
// Subtree boundary rationale:
//
//   - "firewall input" — fully controller-managed (management ACL).
//   - "protocols static route" — every route comes from StaticRoutes.
//   - "protocols bgp" — entire subtree, including system-as, neighbors,
//     and address-family entries.
//   - "vpn ipsec" — entire subtree (ike-group ROUTER-IKE, esp-group
//     ROUTER-ESP, every site-to-site peer).
//
// NAT is out of scope for the routed Phase 1 (DECISIONS.md D3): no
// `nat source`/`nat destination` op is ever emitted — neither set nor
// delete — so the managed-subtree deletes carry no nat paths.
//
// We do NOT delete "interfaces ethernet": the VM NICs are created
// elsewhere and removing the ethernet subtree would orphan the addresses
// VyOS pulled in via cloud-init.
func deleteManagedSubtrees(in Inputs) []vyos.Operation {
	ops := []vyos.Operation{
		{Op: vyos.OpDelete, Path: []string{"protocols", "static", "route"}},
		{Op: vyos.OpDelete, Path: []string{"protocols", "bgp"}},
		{Op: vyos.OpDelete, Path: []string{"vpn", "ipsec"}},
	}

	// firewall/input is only owned by the controller when ManagementCIDR
	// is set (renderManagementFirewall emits the chain). With an empty
	// ManagementCIDR (`--allow-open-management` test path), the renderer
	// emits no firewall rules — deleting the chain would silently wipe
	// any rules an operator added manually. Skip the delete in that case.
	if in.ManagementCIDR != "" {
		ops = append(ops,
			vyos.Operation{Op: vyos.OpDelete, Path: []string{"firewall", "input"}},
		)
	}

	// Net-new domain: clear the platform-owned tunnel-ingress rule set and
	// its interface binding before re-rendering, so a shrunk RemoteCIDRs
	// list does not leave stale accept rules behind (delete-then-set). Only
	// fired when a tunnel device is resolved (the feature is otherwise off).
	//
	// TODO(T06): validate against VyOS 1.5-rolling nftables — firewall ipv4
	// name / forward filter inbound-interface jump binding; paths provisional
	// until live push.
	if in.TunnelDevice != "" {
		ops = append(ops,
			vyos.Operation{Op: vyos.OpDelete, Path: []string{"firewall", "name", TunnelIngressRuleSet}},
			vyos.Operation{Op: vyos.OpDelete, Path: []string{"interfaces", "ethernet", in.TunnelDevice, "firewall", "in", "name"}},
		)
	}

	return ops
}

// interfaceDeviceMap maps an interface name to its bound ethN device. The
// base mapping is positional (eth0 is the first interface, and so on) —
// correct at first boot, where NICs are attached in spec order and the
// kernel names them sequentially. Once the controller's MAC discovery has
// populated DiscoveredInterfaces, the discovered binding overrides the
// positional guess: it is normative and survives reordering and kernel
// device-name reuse after hot-plug.
func interfaceDeviceMap(in Inputs) map[string]string {
	m := make(map[string]string, len(in.Interfaces))

	for i := range in.Interfaces {
		m[in.Interfaces[i].Name] = "eth" + strconv.Itoa(i)
	}

	for i := range in.DiscoveredInterfaces {
		st := &in.DiscoveredInterfaces[i]
		if st.Device == "" {
			continue
		}

		if _, ok := m[st.Name]; ok {
			m[st.Name] = st.Device
		}
	}

	return m
}

// renderRemovedInterfaceCleanup emits delete ops for interfaces that were
// removed from the desired list but whose discovered device binding is
// still recorded in DiscoveredInterfaces. Without the cleanup the freed
// device keeps its address/description, and a future hot-plugged NIC that
// reuses the kernel name would silently inherit them.
//
// Two guards keep this safe:
//
//   - entries without a discovered device are skipped — guessing
//     positionally could delete a live interface;
//   - devices that a current interface has been re-bound to are skipped —
//     the live interface's own set ops would fight the delete inside the
//     same transaction.
func renderRemovedInterfaceCleanup(in Inputs) []vyos.Operation {
	if len(in.DiscoveredInterfaces) == 0 {
		return nil
	}

	live := interfaceDeviceMap(in)

	claimed := make(map[string]bool, len(live))
	for _, dev := range live {
		claimed[dev] = true
	}

	var ops []vyos.Operation

	for i := range in.DiscoveredInterfaces {
		st := &in.DiscoveredInterfaces[i]
		if st.Device == "" || claimed[st.Device] {
			continue
		}

		if _, ok := live[st.Name]; ok {
			continue
		}

		ops = append(ops,
			vyos.Operation{Op: vyos.OpDelete, Path: []string{"interfaces", "ethernet", st.Device, "address"}},
			vyos.Operation{Op: vyos.OpDelete, Path: []string{"interfaces", "ethernet", st.Device, "description"}},
		)
	}

	return ops
}

func renderInterfaces(in Inputs) []vyos.Operation {
	ops := make([]vyos.Operation, 0, len(in.Interfaces)*2)

	devs := interfaceDeviceMap(in)

	for i := range in.Interfaces {
		iface := &in.Interfaces[i]
		dev := devs[iface.Name]

		ops = append(ops, set([]string{"interfaces", "ethernet", dev, "description"}, iface.Name))

		if iface.Uplink {
			ops = append(ops, set([]string{"interfaces", "ethernet", dev, "address"}, "dhcp"))

			continue
		}

		// Routed/LAN interface: emit the static address only when the
		// controller has resolved both the address and the network mask.
		// A missing value surfaces as drift (the reconciler requeues),
		// not as an invalid maskless `set` VyOS would reject.
		if iface.Address == "" {
			continue
		}

		mask := cidrMaskSuffix(iface.NetworkCIDR)
		if mask == "" {
			continue
		}

		ops = append(ops, set(
			[]string{"interfaces", "ethernet", dev, "address"},
			iface.Address+mask,
		))
	}

	return ops
}

// cidrMaskSuffix turns "10.0.0.0/24" into "/24" (including the slash).
// Returns "" when the input does not contain a slash.
func cidrMaskSuffix(cidr string) string {
	if idx := strings.Index(cidr, "/"); idx >= 0 {
		return cidr[idx:]
	}

	return ""
}

// renderManagementFirewall emits VyOS firewall rules that:
//
//  1. Accept established/related return traffic (rule 5).
//  2. Accept SSH (22) and HTTPS API (443) from Inputs.ManagementCIDR
//     (rule 10).
//  3. Accept IKE (UDP 500, NAT-T UDP 4500) and ESP (IP protocol 50)
//     when Tunnels is non-empty (rules 20/21/22).
//  4. Accept BGP (TCP 179) from every configured BGP peer
//     (one rule per peer starting at 30).
//  5. Drop everything else.
//
// Without the protocol-specific accept rules in (3) and (4) the
// controller would render `vpn ipsec site-to-site peer …` and
// `protocols bgp …` configuration but the firewall it itself stamps
// would block the underlying packets — tunnels would never come up and
// BGP sessions would sit in Idle forever.
func renderManagementFirewall(in Inputs) []vyos.Operation {
	if in.ManagementCIDR == "" {
		return nil
	}

	// TODO(T06): validate against VyOS 1.5-rolling nftables — the ported
	// `firewall input` global-input hook became `firewall ipv4 input filter`
	// in 1.5; the whole management-firewall chain (rules 5/10/20-22/30+ and
	// default-action) is provisional until the live push.
	ops := make([]vyos.Operation, 0, 16)

	// Rule 5: accept established/related sessions BEFORE any other accept
	// rule. Without it every Configure (which always replays the input
	// chain) tears down the controller's own in-flight HTTPS session. The
	// cloud-init seed must agree on this part of the chain.
	stateBase := []string{"firewall", "input", "rule", "5"}
	ops = append(ops,
		set(append(stateBase, "action"), "accept"),
		set(append(stateBase, "state", "established"), "enable"),
		set(append(stateBase, "state", "related"), "enable"),
	)

	// Rule 10: management-CIDR allow rule for SSH and HTTPS API.
	mgmtBase := []string{"firewall", "input", "rule", "10"}
	ops = append(ops,
		set(append(mgmtBase, "action"), "accept"),
		set(append(mgmtBase, "source", "address"), in.ManagementCIDR),
		set(append(mgmtBase, "protocol"), "tcp"),
		set(append(mgmtBase, "destination", "port"), "22,443"),
	)

	ops = append(ops, renderIPSecFirewallAccept(in)...)
	ops = append(ops, renderBGPFirewallAccept(in)...)

	ops = append(ops,
		set([]string{"firewall", "input", "default-action"}, "drop"),
	)

	return ops
}

// renderIPSecFirewallAccept emits IKE (UDP 500), NAT-T (UDP 4500) and ESP
// (IP protocol 50) accept rules when Tunnels is non-empty. A router
// without IPsec keeps the tighter firewall.
func renderIPSecFirewallAccept(in Inputs) []vyos.Operation {
	if len(in.Tunnels) == 0 {
		return nil
	}

	ikeBase := []string{"firewall", "input", "rule", "20"}
	natTBase := []string{"firewall", "input", "rule", "21"}
	espBase := []string{"firewall", "input", "rule", "22"}

	return []vyos.Operation{
		set(append(ikeBase, "action"), "accept"),
		set(append(ikeBase, "protocol"), "udp"),
		set(append(ikeBase, "destination", "port"), "500"),
		set(append(natTBase, "action"), "accept"),
		set(append(natTBase, "protocol"), "udp"),
		set(append(natTBase, "destination", "port"), "4500"),
		set(append(espBase, "action"), "accept"),
		set(append(espBase, "protocol"), "esp"),
	}
}

// renderBGPFirewallAccept emits a per-peer TCP 179 accept rule for every
// configured BGP peer, source-restricted to the peer's IPv4 address. A
// peer that was deleted from spec has its firewall hole closed on the
// next reconcile via deleteManagedSubtrees.
func renderBGPFirewallAccept(in Inputs) []vyos.Operation {
	if in.BGP == nil {
		return nil
	}

	ops := make([]vyos.Operation, 0, len(in.BGP.Peers)*4)
	ruleNum := 30

	for i := range in.BGP.Peers {
		peer := &in.BGP.Peers[i]
		if peer.PeerAddress == "" {
			continue
		}

		base := []string{"firewall", "input", "rule", strconv.Itoa(ruleNum)}
		ops = append(ops,
			set(append(base, "action"), "accept"),
			set(append(base, "source", "address"), peer.PeerAddress),
			set(append(base, "protocol"), "tcp"),
			set(append(base, "destination", "port"), "179"),
		)
		ruleNum += 10
	}

	return ops
}

// --- Net-new Phase-1 render domains -------------------------------------
//
// The renderers below implement Phase-1 requirements absent from the
// cozyportal reference (T02). Each isolates the VyOS-version-specific leaf
// path or binding in a single helper (mssClampOp; tunnelIngressPath /
// tunnelIngressBindingOp; the force-encapsulation leaf lives in
// renderIPSec) so the syntax can be swapped in one place once the live
// push validates it against the shipped image.

// renderMSSClamp emits a TCP MSS clamp on the resolved tunnel device,
// derived from the overlay MTU (OverlayMTU, defaulting to
// DefaultOverlayMTU) minus the IPv4+TCP header overhead. The design
// default is MTU 1320 → clamp 1280. Without a resolved TunnelDevice there
// is nothing to clamp.
func renderMSSClamp(in Inputs) []vyos.Operation {
	if in.TunnelDevice == "" {
		return nil
	}

	mtu := in.OverlayMTU
	if mtu == 0 {
		mtu = DefaultOverlayMTU
	}

	clamp := mtu - MSSClampOverhead

	return []vyos.Operation{mssClampOp(in.TunnelDevice, strconv.Itoa(clamp))}
}

// mssClampOp is the single place that emits the version-specific MSS-clamp
// leaf path. The device is caller-resolved (never hardcoded).
//
// TODO(T06): validate against VyOS 1.5-rolling nftables — `firewall ipv4
// options interface <dev> adjust-mss`; path provisional until live push.
func mssClampOp(dev, clamp string) vyos.Operation {
	return set([]string{"firewall", "options", "interface", dev, "adjust-mss"}, clamp)
}

// renderTunnelIngressFilter renders the platform-owned guest
// tunnel-ingress source allow-list: established/related return traffic is
// accepted (rule 5), each declared remote CIDR is accepted by source
// address (rules 10, 20, …), and everything else arriving on the tunnel
// device is dropped by the rule-set default-action. The controller must
// confirm this guard active before flipping the instance Ready (T08).
//
// Requires a resolved TunnelDevice; without one there is nothing to bind to,
// so the filter is skipped. An empty RemoteCIDRs list is deliberately NOT an
// early return: deleteManagedSubtrees has already removed the old ruleset and
// its interface binding, so returning nothing here would leave forwarded
// traffic unfiltered (fail-OPEN). Instead the established/related accept, the
// default-action drop and the interface binding are still emitted — a resolved
// tunnel device with no declared remote subnets fails CLOSED (drop everything
// except return traffic) rather than open.
func renderTunnelIngressFilter(in Inputs) []vyos.Operation {
	if in.TunnelDevice == "" {
		return nil
	}

	ops := make([]vyos.Operation, 0, len(in.RemoteCIDRs)*2+5)

	// Rule 5: accept established/related return traffic before any source
	// match (mirrors the management-firewall chain).
	ops = append(ops,
		set(tunnelIngressPath("rule", "5", "action"), "accept"),
		set(tunnelIngressPath("rule", "5", "state", "established"), "enable"),
		set(tunnelIngressPath("rule", "5", "state", "related"), "enable"),
	)

	// One source-restricted accept per declared remote CIDR, numbered from
	// 10 with step 10 (gaps leave room for manual debugging interventions).
	rule := 10
	for _, cidr := range in.RemoteCIDRs {
		n := strconv.Itoa(rule)
		ops = append(ops,
			set(tunnelIngressPath("rule", n, "action"), "accept"),
			set(tunnelIngressPath("rule", n, "source", "address"), cidr),
		)
		rule += 10
	}

	// Drop everything sourced outside the declared remote subnets.
	ops = append(ops, set(tunnelIngressPath("default-action"), "drop"))

	// Bind the rule set to inbound traffic on the resolved tunnel device.
	ops = append(ops, tunnelIngressBindingOp(in.TunnelDevice))

	return ops
}

// tunnelIngressPath is the single place that emits the version-specific
// base path for the tunnel-ingress rule set.
//
// TODO(T06): validate against VyOS 1.5-rolling nftables — `firewall ipv4
// name <NAME> rule ...`; path provisional until live push.
func tunnelIngressPath(sub ...string) []string {
	return append([]string{"firewall", "name", TunnelIngressRuleSet}, sub...)
}

// tunnelIngressBindingOp is the single place that emits the
// version-specific binding of the rule set to the tunnel device.
//
// TODO(T06): validate against VyOS 1.5-rolling nftables — the per-interface
// `firewall in name` binding was removed; 1.5 filters forwarded traffic via
// `firewall ipv4 forward filter rule N inbound-interface <dev> action jump
// jump-target <NAME>`. Binding provisional until live push.
func tunnelIngressBindingOp(dev string) vyos.Operation {
	return set([]string{"interfaces", "ethernet", dev, "firewall", "in", "name"}, TunnelIngressRuleSet)
}

func renderStaticRoutes(in Inputs) []vyos.Operation {
	ops := make([]vyos.Operation, 0, len(in.StaticRoutes)*2)

	for i := range in.StaticRoutes {
		r := &in.StaticRoutes[i]

		base := []string{"protocols", "static", "route", r.Destination}

		ops = append(ops, set(append(base, "next-hop", r.NextHop), ""))

		if r.Description != "" {
			ops = append(ops, set(append(base, "description"), r.Description))
		}
	}

	return ops
}

func renderIPSec(in Inputs) []vyos.Operation {
	if len(in.Tunnels) == 0 {
		return nil
	}

	ops := make([]vyos.Operation, 0, len(in.Tunnels)*10)

	for i := range in.Tunnels {
		t := &in.Tunnels[i]

		// PSK is a plain field on the tunnel. An optional Description
		// cannot serve as a stable map key because admission permits
		// empty and duplicate descriptions; the resulting map overwrite
		// would silently swap PSKs between tunnels.
		if t.PSK == "" {
			// Should be unreachable: validation requires PSK non-empty.
			// Skip defensively to avoid emitting a tunnel without auth,
			// which would silently widen the surface.
			continue
		}

		ike := normaliseIKE(t.IKE)
		esp := normaliseESP(t.ESP)

		ops = append(ops,
			set([]string{"vpn", "ipsec", "ike-group", ikeGroupName, "proposal", "1", "dh-group"}, strconv.Itoa(ike.DhGroup)),
			set([]string{"vpn", "ipsec", "ike-group", ikeGroupName, "proposal", "1", "encryption"}, ike.Encryption),
			set([]string{"vpn", "ipsec", "ike-group", ikeGroupName, "proposal", "1", "hash"}, ike.Hash),
			set([]string{"vpn", "ipsec", "ike-group", ikeGroupName, "lifetime"}, strconv.Itoa(ike.Lifetime)),
			set([]string{"vpn", "ipsec", "ike-group", ikeGroupName, "key-exchange"}, string(ike.Version)),

			set([]string{"vpn", "ipsec", "esp-group", espGroupName, "proposal", "1", "encryption"}, esp.Encryption),
			set([]string{"vpn", "ipsec", "esp-group", espGroupName, "proposal", "1", "hash"}, esp.Hash),
			set([]string{"vpn", "ipsec", "esp-group", espGroupName, "pfs"}, "dh-group"+strconv.Itoa(esp.PfsGroup)),
			set([]string{"vpn", "ipsec", "esp-group", espGroupName, "lifetime"}, strconv.Itoa(esp.Lifetime)),
		)

		peer := []string{"vpn", "ipsec", "site-to-site", "peer", t.PeerAddress}

		ops = append(ops,
			set(append(peer, "authentication", "mode"), "pre-shared-secret"),
			set(append(peer, "authentication", "pre-shared-secret"), t.PSK),
			set(append(peer, "ike-group"), ikeGroupName),
			set(append(peer, "default-esp-group"), espGroupName),
		)

		// Forced ESP-in-UDP (NAT-T) on every peer, unconditionally: native
		// ESP is dropped pod-to-pod by Cilium conntrack, so the tunnel must
		// always encapsulate in UDP regardless of whether a NAT is detected.
		//
		// TODO(T06): validate against VyOS 1.5-rolling — force-encapsulation
		// leaf placement under `vpn ipsec site-to-site peer <p>`; path
		// provisional until live push.
		ops = append(ops, set(append(peer, "force-encapsulation"), "enable"))

		// local-address reflects the router's uplink IP. Empty leaves the
		// field unset and VyOS auto-detects.
		if in.ExternalIP != "" {
			ops = append(ops, set(append(peer, "local-address"), in.ExternalIP))
		}

		// Tunnels are numbered starting at 1; pair local and remote
		// subnets 1-to-1. If counts differ, surplus entries are ignored.
		n := min(len(t.LocalSubnets), len(t.RemoteSubnets))
		for j := range n {
			tun := strconv.Itoa(j + 1)
			ops = append(ops,
				set(append(peer, "tunnel", tun, "local", "prefix"), t.LocalSubnets[j]),
				set(append(peer, "tunnel", tun, "remote", "prefix"), t.RemoteSubnets[j]),
			)
		}
	}

	return ops
}

func renderBGP(in Inputs) []vyos.Operation {
	bgp := in.BGP
	if bgp == nil {
		return nil
	}

	asn := strconv.FormatInt(bgp.Asn, 10)
	ops := make([]vyos.Operation, 0, len(bgp.Peers)*4)

	if bgp.RouterID != "" {
		ops = append(ops, set([]string{"protocols", "bgp", "parameters", "router-id"}, bgp.RouterID))
	}

	for i := range bgp.Peers {
		p := &bgp.Peers[i]
		neigh := []string{"protocols", "bgp", "neighbor", p.PeerAddress}

		ops = append(ops, set(append(neigh, "remote-as"), strconv.FormatInt(p.PeerAsn, 10)))

		if pw, ok := in.BGPPasswords[p.PeerAddress]; ok {
			ops = append(ops, set(append(neigh, "password"), pw))
		}

		if p.Timers != nil {
			ops = append(ops,
				set(append(neigh, "timers", "keepalive"), strconv.Itoa(p.Timers.Keepalive)),
				set(append(neigh, "timers", "holdtime"), strconv.Itoa(p.Timers.Hold)),
			)
		}

		for _, cidr := range p.AdvertisedNetworks {
			ops = append(ops, set([]string{
				"protocols", "bgp", "address-family", "ipv4-unicast", "network", cidr,
			}, ""))
		}
	}

	// Local AS lives on the BGP root in VyOS 1.4+ (no per-neighbor
	// local-as). Emitting at the end keeps the configure order
	// parent → children.
	ops = append(ops, set([]string{"protocols", "bgp", "system-as"}, asn))

	return ops
}

// normaliseIKE returns IKE parameters with the design defaults
// substituted for any zero-valued fields.
func normaliseIKE(p *IKEParams) IKEParams {
	out := IKEParams{}
	if p != nil {
		out = *p
	}

	if out.Version == "" {
		out.Version = IKEVersionV2
	}

	if out.Encryption == "" {
		out.Encryption = "aes256"
	}

	if out.Hash == "" {
		out.Hash = "sha256"
	}

	if out.DhGroup == 0 {
		out.DhGroup = 14
	}

	if out.Lifetime == 0 {
		out.Lifetime = 28800
	}

	return out
}

// normaliseESP returns ESP parameters with the design defaults
// substituted for any zero-valued fields.
func normaliseESP(p *ESPParams) ESPParams {
	out := ESPParams{}
	if p != nil {
		out = *p
	}

	if out.Encryption == "" {
		out.Encryption = "aes256"
	}

	if out.Hash == "" {
		out.Hash = "sha256"
	}

	if out.PfsGroup == 0 {
		out.PfsGroup = 14
	}

	if out.Lifetime == 0 {
		out.Lifetime = 3600
	}

	return out
}

// set is the canonical way to construct an OpSet operation. Keeps the
// renderer free of repetitive struct literals.
func set(path []string, value string) vyos.Operation {
	return vyos.Operation{Op: vyos.OpSet, Path: path, Value: value}
}
