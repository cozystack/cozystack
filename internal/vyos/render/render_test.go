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

package render_test

import (
	"strings"
	"testing"

	"github.com/cozystack/cozystack/internal/vyos"
	"github.com/cozystack/cozystack/internal/vyos/render"
)

// containsSet returns true if ops contains an OpSet whose path equals
// the joined `wantPath` and (when wantValue != "*") whose value matches.
func containsSet(ops []vyos.Operation, wantPath, wantValue string) bool {
	for _, op := range ops {
		if op.Op != vyos.OpSet {
			continue
		}

		if strings.Join(op.Path, "/") != wantPath {
			continue
		}

		if wantValue == "*" || op.Value == wantValue {
			return true
		}
	}

	return false
}

// containsOp returns true if ops contains any op of the given kind whose
// path joined with '/' equals wantPath.
func containsOp(ops []vyos.Operation, op string, wantPath string) bool {
	for _, o := range ops {
		if o.Op != op {
			continue
		}

		if strings.Join(o.Path, "/") == wantPath {
			return true
		}
	}

	return false
}

// baseInputs returns a minimal routed configuration: a DHCP uplink
// ("wan" → eth0) and a static LAN interface ("lan" → eth1).
func baseInputs() render.Inputs {
	return render.Inputs{
		Interfaces: []render.Interface{
			{Name: "wan", Uplink: true},
			{Name: "lan", Address: "10.0.0.1", NetworkCIDR: "10.0.0.0/24"},
		},
	}
}

func TestRenderInterfaces_UplinkUsesDHCP(t *testing.T) {
	t.Parallel()

	ops := render.Render(baseInputs())

	if !containsSet(ops, "interfaces/ethernet/eth0/address", "dhcp") {
		t.Errorf("expected eth0 to use dhcp")
	}

	if !containsSet(ops, "interfaces/ethernet/eth0/description", "wan") {
		t.Errorf("expected eth0 description=wan")
	}
}

func TestRenderInterfaces_LANGetsCIDRMask(t *testing.T) {
	t.Parallel()

	ops := render.Render(baseInputs())

	if !containsSet(ops, "interfaces/ethernet/eth1/address", "10.0.0.1/24") {
		t.Errorf("expected eth1 address=10.0.0.1/24")
	}

	if !containsSet(ops, "interfaces/ethernet/eth1/description", "lan") {
		t.Errorf("expected eth1 description=lan")
	}
}

func TestRenderInterfaces_SkipsLANWithUnresolvedNetwork(t *testing.T) {
	t.Parallel()

	in := baseInputs()
	in.Interfaces[1].NetworkCIDR = "" // controller has not resolved the mask yet

	ops := render.Render(in)

	if containsSet(ops, "interfaces/ethernet/eth1/address", "*") {
		t.Errorf("expected eth1 address to be omitted when the network is unresolved")
	}
}

func TestRender_EmitsManagedSubtreeDeletesBeforeSets(t *testing.T) {
	t.Parallel()

	// firewall/input is only deleted when ManagementCIDR is set —
	// otherwise the renderer leaves the chain alone so operator-added
	// rules survive a no-op Configure (see deleteManagedSubtrees godoc).
	in := baseInputs()
	in.ManagementCIDR = "10.244.0.0/16"

	ops := render.Render(in)

	// All deletes must appear before any sets in the slice — the
	// reconciler relies on this ordering when batching the /configure
	// transaction.
	firstSet := -1

	for i, op := range ops {
		if op.Op == vyos.OpSet {
			firstSet = i

			break
		}
	}

	for i, op := range ops {
		if op.Op == vyos.OpDelete && firstSet >= 0 && i > firstSet {
			t.Errorf("delete op at index %d appears after first set at %d", i, firstSet)
		}
	}

	// Spot-check the documented managed subtrees (NAT is out of scope).
	for _, path := range []string{
		"firewall/ipv4/input/filter",
		"protocols/static/route",
		"protocols/bgp",
		"vpn/ipsec",
	} {
		if !containsOp(ops, vyos.OpDelete, path) {
			t.Errorf("expected delete on managed subtree %q", path)
		}
	}
}

func TestRenderManagementFirewall_AppliedWhenCIDRSet(t *testing.T) {
	t.Parallel()

	in := baseInputs()
	in.ManagementCIDR = "10.244.0.0/16"

	ops := render.Render(in)

	if !containsSet(ops, "firewall/ipv4/input/filter/rule/10/action", "accept") {
		t.Errorf("expected accept rule 10")
	}

	if !containsSet(ops, "firewall/ipv4/input/filter/rule/10/source/address", "10.244.0.0/16") {
		t.Errorf("expected source/address to be the management CIDR")
	}

	// S2: only the HTTPS API (443) is opened from managementCIDR. SSH (22) is
	// NOT opened — the appliance disables SSH and locks the baked login, so 22
	// must stay behind the default-action drop.
	if !containsSet(ops, "firewall/ipv4/input/filter/rule/10/destination/port", "443") {
		t.Errorf("expected port 443 (HTTPS API only)")
	}
	for _, op := range ops {
		if op.Op == vyos.OpSet &&
			strings.Join(op.Path, "/") == "firewall/ipv4/input/filter/rule/10/destination/port" &&
			strings.Contains(op.Value, "22") {
			t.Errorf("management ACL rule 10 must not open SSH (22), got port %q", op.Value)
		}
	}

	if !containsSet(ops, "firewall/ipv4/input/filter/default-action", "drop") {
		t.Errorf("expected default-action=drop")
	}
}

func TestRenderManagementFirewall_NotAppliedWhenEmpty(t *testing.T) {
	t.Parallel()

	in := baseInputs() // ManagementCIDR stays empty

	ops := render.Render(in)

	// Empty CIDR must not emit any firewall SET ops.
	for _, op := range ops {
		if op.Op != vyos.OpSet {
			continue
		}

		if len(op.Path) > 0 && op.Path[0] == "firewall" {
			t.Errorf("expected no firewall SET ops when ManagementCIDR is empty, got %v", op)
		}
	}
}

func TestRenderStaticRoutes(t *testing.T) {
	t.Parallel()

	in := baseInputs()
	in.StaticRoutes = []render.StaticRoute{
		{Description: "Partner", Destination: "192.168.50.0/24", NextHop: "10.0.0.254"},
	}

	ops := render.Render(in)

	// Path components are joined with '/', so a CIDR component (which
	// itself contains '/') still appears as a single path element.
	if !containsSet(ops, "protocols/static/route/192.168.50.0/24/next-hop/10.0.0.254", "") {
		t.Errorf("expected static route next-hop entry")
	}

	if !containsSet(ops, "protocols/static/route/192.168.50.0/24/description", "Partner") {
		t.Errorf("expected static route description")
	}
}

func TestRenderIPSec_SkipsTunnelWithoutResolvedPSK(t *testing.T) {
	t.Parallel()

	in := baseInputs()
	in.Tunnels = []render.IPSecTunnel{{
		Description:   "aws",
		PeerAddress:   "203.0.113.10",
		LocalSubnets:  []string{"10.0.0.0/24"},
		RemoteSubnets: []string{"172.31.0.0/16"},
	}}

	ops := render.Render(in)

	for _, op := range ops {
		if strings.HasPrefix(strings.Join(op.Path, "/"), "vpn/ipsec/site-to-site") {
			t.Errorf("expected no IPSec peer ops when PSK is unresolved, got %v", op)
		}
	}
}

func TestRenderIPSec_EmitsGroupsAndPeerWithDefaults(t *testing.T) {
	t.Parallel()

	in := baseInputs()
	in.ExternalIP = "203.0.113.15"
	in.Tunnels = []render.IPSecTunnel{{
		Description:   "aws",
		PeerAddress:   "203.0.113.10",
		PSK:           "secretpsk",
		LocalSubnets:  []string{"10.0.0.0/24"},
		RemoteSubnets: []string{"172.31.0.0/16"},
	}}

	ops := render.Render(in)

	if !containsSet(ops, "vpn/ipsec/ike-group/ROUTER-IKE/proposal/1/encryption", "aes256") {
		t.Errorf("expected IKE encryption default aes256")
	}

	// VyOS 1.5 site-to-site peer model (validated live): the peer key is the
	// sanitised description ("aws"), the remote IP is in remote-address, and the
	// PSK lives in the global authentication subtree (not inline under the peer).
	if !containsSet(ops, "vpn/ipsec/site-to-site/peer/aws/remote-address", "203.0.113.10") {
		t.Errorf("expected the remote peer IP in remote-address")
	}

	if !containsSet(ops, "vpn/ipsec/authentication/psk/aws/secret", "secretpsk") {
		t.Errorf("expected the PSK in the global authentication psk subtree")
	}

	if !containsSet(ops, "vpn/ipsec/authentication/psk/aws/id", "203.0.113.10") {
		t.Errorf("expected the PSK matched to the peer by remote-address id")
	}

	if !containsSet(ops, "vpn/ipsec/site-to-site/peer/aws/local-address", "203.0.113.15") {
		t.Errorf("expected peer local-address from Inputs.ExternalIP")
	}

	if !containsSet(ops, "vpn/ipsec/site-to-site/peer/aws/tunnel/1/local/prefix", "10.0.0.0/24") {
		t.Errorf("expected tunnel 1 local prefix")
	}

	if !containsSet(ops, "vpn/ipsec/site-to-site/peer/aws/tunnel/1/remote/prefix", "172.31.0.0/16") {
		t.Errorf("expected tunnel 1 remote prefix")
	}
}

func TestRenderBGP_EmitsAsnPeersAndAdvertisedNetworks(t *testing.T) {
	t.Parallel()

	in := baseInputs()
	in.BGP = &render.BGPConfig{
		Asn:      65001,
		RouterID: "203.0.113.15",
		Peers: []render.BGPPeer{{
			PeerAddress:        "203.0.113.1",
			PeerAsn:            65000,
			AdvertisedNetworks: []string{"10.0.0.0/24"},
		}},
	}

	ops := render.Render(in)

	if !containsSet(ops, "protocols/bgp/system-as", "65001") {
		t.Errorf("expected system-as=65001")
	}

	if !containsSet(ops, "protocols/bgp/parameters/router-id", "203.0.113.15") {
		t.Errorf("expected router-id")
	}

	if !containsSet(ops, "protocols/bgp/neighbor/203.0.113.1/remote-as", "65000") {
		t.Errorf("expected neighbor remote-as=65000")
	}

	if !containsSet(ops, "protocols/bgp/address-family/ipv4-unicast/network/10.0.0.0/24", "") {
		t.Errorf("expected advertised network entry")
	}
}

func TestRenderBGP_EmitsPasswordWhenResolved(t *testing.T) {
	t.Parallel()

	in := baseInputs()
	in.BGP = &render.BGPConfig{
		Asn: 65001,
		Peers: []render.BGPPeer{{
			PeerAddress: "203.0.113.1",
			PeerAsn:     65000,
		}},
	}
	in.BGPPasswords = map[string]string{"203.0.113.1": "md5pass"}

	ops := render.Render(in)

	if !containsSet(ops, "protocols/bgp/neighbor/203.0.113.1/password", "md5pass") {
		t.Errorf("expected BGP peer password to be set")
	}
}

func TestRenderManagementFirewall_OpensIKEAndESPWhenIPSecConfigured(t *testing.T) {
	t.Parallel()

	in := baseInputs()
	in.ManagementCIDR = "10.244.0.0/16"
	in.ExternalIP = "203.0.113.15"
	in.Tunnels = []render.IPSecTunnel{{
		Description:   "aws",
		PeerAddress:   "203.0.113.10",
		PSK:           "secret",
		LocalSubnets:  []string{"10.0.0.0/24"},
		RemoteSubnets: []string{"172.31.0.0/16"},
	}}

	ops := render.Render(in)

	if !containsSet(ops, "firewall/ipv4/input/filter/rule/20/protocol", "udp") {
		t.Errorf("expected IKE accept rule (UDP 500) when IPSec is configured")
	}

	if !containsSet(ops, "firewall/ipv4/input/filter/rule/20/destination/port", "500") {
		t.Errorf("expected IKE rule destination port 500")
	}

	if !containsSet(ops, "firewall/ipv4/input/filter/rule/21/destination/port", "4500") {
		t.Errorf("expected NAT-T rule destination port 4500")
	}

	if !containsSet(ops, "firewall/ipv4/input/filter/rule/22/protocol", "esp") {
		t.Errorf("expected ESP accept rule (IP protocol 50)")
	}
}

func TestRenderManagementFirewall_DoesNotOpenIPSecPortsWhenNoIPSecConfigured(t *testing.T) {
	t.Parallel()

	in := baseInputs()
	in.ManagementCIDR = "10.244.0.0/16"

	ops := render.Render(in)

	if containsSet(ops, "firewall/ipv4/input/filter/rule/20/protocol", "udp") {
		t.Errorf("expected NO IKE rule on a router without tunnels")
	}
}

func TestRenderManagementFirewall_OpensBGPPortPerPeer(t *testing.T) {
	t.Parallel()

	in := baseInputs()
	in.ManagementCIDR = "10.244.0.0/16"
	in.BGP = &render.BGPConfig{
		Asn: 65001,
		Peers: []render.BGPPeer{
			{PeerAddress: "203.0.113.1", PeerAsn: 65000},
			{PeerAddress: "198.51.100.1", PeerAsn: 65010},
		},
	}

	ops := render.Render(in)

	// First peer at rule 30, second at rule 40.
	if !containsSet(ops, "firewall/ipv4/input/filter/rule/30/source/address", "203.0.113.1") {
		t.Errorf("expected BGP peer 1 firewall rule (TCP 179, source-restricted)")
	}

	if !containsSet(ops, "firewall/ipv4/input/filter/rule/30/destination/port", "179") {
		t.Errorf("expected BGP rule destination port 179")
	}

	if !containsSet(ops, "firewall/ipv4/input/filter/rule/40/source/address", "198.51.100.1") {
		t.Errorf("expected BGP peer 2 firewall rule at the next slot")
	}
}

func TestRenderInterfaces_PrefersDiscoveredDeviceOverPosition(t *testing.T) {
	t.Parallel()

	// Discovery bound the interfaces opposite to their positional order
	// (e.g. the list was reordered after the VM booted). The renderer
	// must follow DiscoveredInterfaces, not the slice index.
	in := baseInputs()
	in.DiscoveredInterfaces = []render.InterfaceStatus{
		{Name: "wan", Device: "eth1"},
		{Name: "lan", Device: "eth0"},
	}

	ops := render.Render(in)

	if !containsSet(ops, "interfaces/ethernet/eth1/address", "dhcp") {
		t.Errorf("expected uplink rendered on discovered eth1, ops: %+v", ops)
	}

	if !containsSet(ops, "interfaces/ethernet/eth0/address", "10.0.0.1/24") {
		t.Errorf("expected LAN rendered on discovered eth0, ops: %+v", ops)
	}
}

func TestRenderInterfaces_PartialDiscoveryFallsBackToPosition(t *testing.T) {
	t.Parallel()

	// Only the uplink has been discovered so far; the LAN entry exists
	// without a device. Undiscovered interfaces keep positional mapping.
	in := baseInputs()
	in.DiscoveredInterfaces = []render.InterfaceStatus{
		{Name: "wan", Device: "eth0"},
		{Name: "lan"},
	}

	ops := render.Render(in)

	if !containsSet(ops, "interfaces/ethernet/eth0/address", "dhcp") {
		t.Errorf("expected uplink on eth0, ops: %+v", ops)
	}

	if !containsSet(ops, "interfaces/ethernet/eth1/address", "10.0.0.1/24") {
		t.Errorf("expected LAN positionally on eth1, ops: %+v", ops)
	}
}

func TestRender_EmitsCleanupForRemovedInterface(t *testing.T) {
	t.Parallel()

	// DiscoveredInterfaces still carries a binding for an interface that
	// has been removed from the desired list — the renderer must delete
	// its address/description so the freed device cannot leak stale
	// config onto a future NIC that reuses the name.
	in := baseInputs()
	in.DiscoveredInterfaces = []render.InterfaceStatus{
		{Name: "wan", Device: "eth0"},
		{Name: "lan", Device: "eth1"},
		{Name: "old-lan", Device: "eth2"},
	}

	ops := render.Render(in)

	if !containsOp(ops, vyos.OpDelete, "interfaces/ethernet/eth2/address") {
		t.Errorf("expected delete of removed interface address, ops: %+v", ops)
	}

	if !containsOp(ops, vyos.OpDelete, "interfaces/ethernet/eth2/description") {
		t.Errorf("expected delete of removed interface description, ops: %+v", ops)
	}
}

func TestRender_NoCleanupForRemovedInterfaceWithoutDevice(t *testing.T) {
	t.Parallel()

	// A removed interface that was never discovered has no device to
	// clean up — guessing positionally could delete a live interface.
	in := baseInputs()
	in.DiscoveredInterfaces = []render.InterfaceStatus{
		{Name: "old-lan"},
	}

	ops := render.Render(in)

	for _, op := range ops {
		if op.Op == vyos.OpDelete && strings.HasPrefix(strings.Join(op.Path, "/"), "interfaces/ethernet") {
			t.Errorf("expected no ethernet delete ops, got %+v", op)
		}
	}
}

func TestRender_NoCleanupForDeviceClaimedByLiveInterface(t *testing.T) {
	t.Parallel()

	// The removed interface's old device has already been re-bound to a
	// live interface (kernel reused the name for a newer hot-plug).
	// Deleting it would fight the live interface's own set ops.
	in := baseInputs()
	in.DiscoveredInterfaces = []render.InterfaceStatus{
		{Name: "wan", Device: "eth0"},
		{Name: "lan", Device: "eth2"},
		{Name: "old-lan", Device: "eth2"},
	}

	ops := render.Render(in)

	if containsOp(ops, vyos.OpDelete, "interfaces/ethernet/eth2/address") {
		t.Errorf("expected no delete for device claimed by live interface, ops: %+v", ops)
	}
}
