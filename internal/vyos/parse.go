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

import (
	"regexp"
	"strings"
)

// ParseIPSecSA extracts per-peer state from the text body of
// `show vpn ipsec sa`. The implementation targets the strongSwan
// output shipped with VyOS 1.4 and is intentionally permissive: any
// line it does not recognise is silently skipped. Real-world VyOS
// versions emit minor textual variations, so callers should treat
// missing entries as "state unknown" rather than failures.
//
// Recognised line shapes (the leading whitespace varies):
//
//	peer-name:  203.0.113.15...203.0.113.10  IKEv2
//	peer-name[1]: ESTABLISHED 5 seconds ago, ...
//	peer-name{1}:  INSTALLED, TUNNEL, ...
func ParseIPSecSA(text string) []IPSecObservation {
	type entry struct {
		obs   IPSecObservation
		known bool
	}

	order := []string{}
	entries := map[string]*entry{}

	for line := range strings.SplitSeq(text, "\n") {
		if m := ipsecHeaderRe.FindStringSubmatch(line); m != nil {
			name := m[1]
			if _, exists := entries[name]; !exists {
				entries[name] = &entry{
					obs: IPSecObservation{
						PeerName:    name,
						PeerAddress: m[3],
						State:       IPSecTunnelStateDown,
					},
				}
				order = append(order, name)
			}

			continue
		}

		if m := ipsecStateRe.FindStringSubmatch(line); m != nil {
			name := m[1]
			state := normaliseIPSecState(m[2])

			ent, ok := entries[name]
			if !ok {
				ent = &entry{obs: IPSecObservation{PeerName: name, State: state}}
				entries[name] = ent
				order = append(order, name)
			}

			// "Up" wins over "Connecting"/"Down" because INSTALLED can
			// appear after ESTABLISHED for routed connections.
			if state == IPSecTunnelStateUp || !ent.known {
				ent.obs.State = state
				ent.known = true
			}
		}
	}

	out := make([]IPSecObservation, 0, len(order))
	for _, name := range order {
		out = append(out, entries[name].obs)
	}

	return out
}

// ipsecHeaderRe matches the connection summary line:
//
//	name:  local-ip...remote-ip  IKEv*
var ipsecHeaderRe = regexp.MustCompile(`^\s*([A-Za-z0-9._-]+):\s+([\d.]+)\.{3}([\d.]+)`)

// ipsecStateRe matches per-SA state lines:
//
//	name[1]: ESTABLISHED ...
//	name{1}: INSTALLED ...
var ipsecStateRe = regexp.MustCompile(`^\s*([A-Za-z0-9._-]+)[\[\{]\d+[\]\}]:\s*([A-Z_]+)`)

func normaliseIPSecState(raw string) IPSecTunnelState {
	switch strings.ToUpper(raw) {
	case "ESTABLISHED", "INSTALLED":
		return IPSecTunnelStateUp
	case "CONNECTING", "CREATED", "REKEYING":
		return IPSecTunnelStateConnecting
	default:
		return IPSecTunnelStateDown
	}
}

// ParseInterfacesDetail extracts physical-ethernet MAC addresses from
// the text body of `show interfaces detail`. The output follows the
// `ip addr`-style layout (interface header line, then an indented
// `link/ether <mac>` line); some builds prefix headers with the
// kernel ifindex (`2: eth0: <...>`) — both shapes are accepted.
//
// Only plain `ethN` devices are reported: loopback, VLAN sub-interfaces
// (`eth0.10`), tunnels and bridges are skipped — the reconciler only
// ever needs the physical NIC ↔ MAC mapping. Like the other parsers in
// this package the implementation is permissive: lines it does not
// recognise are silently ignored.
func ParseInterfacesDetail(text string) []EthernetObservation {
	var out []EthernetObservation

	current := ""

	for line := range strings.SplitSeq(text, "\n") {
		if m := ethHeaderRe.FindStringSubmatch(line); m != nil {
			current = m[1]

			continue
		}

		if anyHeaderRe.MatchString(line) {
			// A non-ethernet header (lo, VLAN sub-interface, tunnel,
			// bridge) closes the current ethernet section so a stray
			// link/ether line under it cannot be misattributed.
			current = ""

			continue
		}

		m := linkEtherRe.FindStringSubmatch(line)
		if m == nil || current == "" {
			continue
		}

		out = append(out, EthernetObservation{
			Device: current,
			MAC:    strings.ToLower(m[1]),
		})
		current = ""
	}

	return out
}

// ethHeaderRe matches an interface header line for a plain ethernet
// device, with an optional `ip addr`-style numeric ifindex prefix:
//
//	eth0: <BROADCAST,MULTICAST,UP,LOWER_UP> mtu 1500 ...
//	2: eth0: <BROADCAST,MULTICAST,UP,LOWER_UP> mtu 1500 ...
//
// VLAN sub-interfaces ("eth1.100@eth1:") do not match because the
// device name is anchored to digits followed immediately by ":".
var ethHeaderRe = regexp.MustCompile(`^\s*(?:\d+:\s+)?(eth\d+):\s+<`)

// anyHeaderRe matches any interface header line (`name: <FLAGS>`),
// used to close the current ethernet section when a non-ethernet
// interface follows. Header lines start at column 0 (optionally after
// an `ip addr` ifindex), unlike the indented attribute lines.
var anyHeaderRe = regexp.MustCompile(`^(?:\d+:\s+)?\S+:\s+<`)

// linkEtherRe matches the MAC line under an interface header:
//
//	link/ether 52:54:00:11:22:33 brd ff:ff:ff:ff:ff:ff
var linkEtherRe = regexp.MustCompile(`^\s*link/ether\s+([0-9A-Fa-f]{2}(?::[0-9A-Fa-f]{2}){5})\b`)

// ParseBGPSummary extracts per-peer state from the text body of
// `show bgp summary`. Targets the FRR output shipped with VyOS 1.4.
//
// The relevant table looks like:
//
//	Neighbor        V         AS   MsgRcvd   MsgSent   TblVer  InQ OutQ  Up/Down State/PfxRcd   ...
//	203.0.113.1     4      65000        12        12        0    0    0 00:05:03 Established        2 ISP peer
//	203.0.113.2     4      65000        12        12        0    0    0     never Idle              0
func ParseBGPSummary(text string) []BGPObservation {
	var out []BGPObservation

	for line := range strings.SplitSeq(text, "\n") {
		m := bgpRowRe.FindStringSubmatch(line)
		if m == nil {
			continue
		}

		out = append(out, BGPObservation{
			PeerAddress: m[1],
			Session:     normaliseBGPState(m[3]),
			Uptime:      strings.TrimSpace(m[2]),
		})
	}

	return out
}

// bgpRowRe matches one neighbour line:
//
//	neighbour, version, asn, msgrcvd, msgsent, tblver, inq, outq, up/down, state[/pfx]
//
// We only care about columns 1 (neighbour), 9 (up/down) and 10 (state),
// but anchor the regex on the wider shape to avoid matching the table
// header. The state column is either an alphabetic session state
// ("Established", "Idle", …) or — for an up neighbour in raw FRR output —
// a pure number: the received-prefix count that FRR prints in place of the
// word. The alternation matches both so an ESTABLISHED peer whose
// State/PfxRcd cell is numeric is not dropped.
var bgpRowRe = regexp.MustCompile(
	`^([\d.]+)\s+\d+\s+\d+\s+\d+\s+\d+\s+\d+\s+\d+\s+\d+\s+(\S+)\s+([A-Za-z][A-Za-z0-9/]*|\d+)`,
)

func normaliseBGPState(raw string) BGPSessionState {
	// FRR sometimes appends "/<pfx-rcvd>" to the state column when
	// established; strip it before mapping.
	state := raw
	if idx := strings.Index(state, "/"); idx > 0 {
		state = state[:idx]
	}

	// An all-numeric State/PfxRcd column is FRR's shorthand for an up
	// session: the value is the received-prefix count, which only appears
	// once the neighbour is Established. Map it accordingly.
	if isAllDigits(state) {
		return BGPSessionStateEstablished
	}

	switch strings.ToLower(state) {
	case "established":
		return BGPSessionStateEstablished
	case "openconfirm":
		return BGPSessionStateOpenConfirm
	case "opensent":
		return BGPSessionStateOpenSent
	case "active":
		return BGPSessionStateActive
	case "connect":
		return BGPSessionStateConnect
	case "idle":
		return BGPSessionStateIdle
	default:
		return BGPSessionStateIdle
	}
}

// isAllDigits reports whether s is non-empty and consists solely of ASCII
// digits. Used to recognise the numeric received-prefix count FRR prints in
// the State/PfxRcd column for an established neighbour.
func isAllDigits(s string) bool {
	if s == "" {
		return false
	}

	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}

	return true
}
