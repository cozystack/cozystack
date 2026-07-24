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

package vyos

// IPSecTunnelState mirrors the values surfaced on the SiteRouter status:
// Up / Down / Connecting. "" is reserved for "not observed yet".
type IPSecTunnelState string

const (
	IPSecTunnelStateUp         IPSecTunnelState = "Up"
	IPSecTunnelStateDown       IPSecTunnelState = "Down"
	IPSecTunnelStateConnecting IPSecTunnelState = "Connecting"
)

// IPSecObservation reports the operational state of one IPSec
// site-to-site peer extracted from `show vpn ipsec sa`.
type IPSecObservation struct {
	// PeerName is the strongSwan connection name. The reconciler maps
	// it back to the user-facing tunnel description.
	PeerName string

	// PeerAddress is the remote VPN gateway IP. May be empty if the
	// upstream output is partial.
	PeerAddress string

	// State is the rolled-up tunnel state.
	State IPSecTunnelState
}

// EthernetObservation reports one physical ethernet device extracted
// from `show interfaces detail` — the kernel-assigned device name and
// its MAC address. The reconciler joins MAC against the gateway VM's
// NIC MACs to derive the normative interface-name ↔ device mapping.
type EthernetObservation struct {
	// Device is the kernel device name (e.g. "eth0").
	Device string

	// MAC is the device MAC address, normalised to lowercase
	// colon-separated form (e.g. "52:54:00:11:22:33").
	MAC string
}

// BGPSessionState mirrors the values surfaced on the SiteRouter status
// for the BGP session.
type BGPSessionState string

const (
	BGPSessionStateIdle        BGPSessionState = "Idle"
	BGPSessionStateConnect     BGPSessionState = "Connect"
	BGPSessionStateActive      BGPSessionState = "Active"
	BGPSessionStateOpenSent    BGPSessionState = "OpenSent"
	BGPSessionStateOpenConfirm BGPSessionState = "OpenConfirm"
	BGPSessionStateEstablished BGPSessionState = "Established"
)

// BGPObservation reports the operational state of one BGP neighbor
// extracted from `show bgp summary`.
type BGPObservation struct {
	// PeerAddress is the IPv4 address of the BGP neighbor.
	PeerAddress string

	// Session is the rolled-up FSM state. "Established" is the only
	// healthy steady-state value.
	Session BGPSessionState

	// Uptime is the human-readable Up/Down string emitted by FRR
	// (e.g. "00:05:03" or "never"). Empty if absent in the input.
	Uptime string
}
