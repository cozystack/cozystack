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
// `show vpn ipsec sa`. It recognises two output shapes and is
// intentionally permissive: any line it does not recognise is silently
// skipped, so callers should treat missing entries as "state unknown"
// rather than failures.
//
// Shape 1 — the VyOS 1.4 swanctl text (the leading whitespace varies):
//
//	peer-name:  203.0.113.15...203.0.113.10  IKEv2
//	peer-name[1]: ESTABLISHED 5 seconds ago, ...
//	peer-name{1}:  INSTALLED, TUNNEL, ...
//
// Shape 2 — the VyOS 1.5-rolling op-mode table (validated live against
// the shipped image). Column widths are content-adaptive, so rows are
// parsed by splitting on runs of whitespace, never by byte offset:
//
//	Connection        State  Uptime  Bytes In/Out  ...  Remote address  ...
//	----------------  -----  ------  ------------  ...
//	router1-tunnel-1  up     44m41s  0B/0B         ...  10.244.2.123     ...
//
// The connection name is <peer>-tunnel-<n>; the -tunnel-<n> child suffix
// is stripped so PeerName is the peer name (== render.PeerName, which the
// site_router_tunnel_up metric keys on).
func ParseIPSecSA(text string) []IPSecObservation {
	type entry struct {
		obs   IPSecObservation
		known bool
	}

	order := []string{}
	entries := map[string]*entry{}

	// upsert records a state (and optional remote address) for a peer, creating
	// the entry on first sight. "Up" wins over "Connecting"/"Down" because
	// INSTALLED can appear after ESTABLISHED for routed connections; otherwise the
	// first known state sticks.
	upsert := func(name string, state IPSecTunnelState, addr string) {
		ent, ok := entries[name]
		if !ok {
			ent = &entry{obs: IPSecObservation{PeerName: name}}
			entries[name] = ent
			order = append(order, name)
		}
		if addr != "" && ent.obs.PeerAddress == "" {
			ent.obs.PeerAddress = addr
		}
		if state == IPSecTunnelStateUp || !ent.known {
			ent.obs.State = state
			ent.known = true
		}
	}

	for line := range strings.SplitSeq(text, "\n") {
		// Shape 1 (swanctl): summary line — create the peer at Down until a state
		// line upgrades it; carries the remote address.
		if m := ipsecHeaderRe.FindStringSubmatch(line); m != nil {
			name := stripTunnelChildSuffix(m[1])
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

		// Shape 1 (swanctl): per-SA state line.
		if m := ipsecStateRe.FindStringSubmatch(line); m != nil {
			upsert(stripTunnelChildSuffix(m[1]), normaliseIPSecState(m[2]), "")
			continue
		}

		// Shape 2 (VyOS 1.5-rolling op-mode table): a data row.
		if name, state, addr, ok := parseIPSecTableRow(line); ok {
			upsert(name, state, addr)
			continue
		}
	}

	out := make([]IPSecObservation, 0, len(order))
	for _, name := range order {
		out = append(out, entries[name].obs)
	}

	return out
}

// parseIPSecTableRow parses one VyOS 1.5-rolling `show vpn ipsec sa` table data
// row. The table is whitespace-aligned with content-adaptive column widths, so it
// is split on whitespace runs rather than fixed byte offsets. A data row carries
// the state word in the second field; the header row (field 1 == "State") and the
// dashed separator (field 1 is not a state word) return ok=false and are skipped.
// Fields: 0=Connection, 1=State, 2=Uptime, 3=Bytes, 4=Packets, 5=Remote address.
func parseIPSecTableRow(line string) (name string, state IPSecTunnelState, addr string, ok bool) {
	fields := strings.Fields(line)
	if len(fields) < 2 || !isIPSecStateWord(fields[1]) {
		return "", IPSecTunnelStateDown, "", false
	}
	name = stripTunnelChildSuffix(fields[0])
	state = normaliseIPSecState(fields[1])
	if len(fields) >= 6 {
		addr = fields[5]
	}
	return name, state, addr, true
}

// stripTunnelChildSuffix maps a strongSwan connection name to the peer name by
// removing the trailing child-SA suffix VyOS appends: peer "router1" tunnel 1 is
// reported as connection "router1-tunnel-1". The result matches render.PeerName /
// the configured tunnel description, which the site_router_tunnel_up metric keys
// on — without stripping, an observed SA would land on a different series than the
// seeded 0 gauge. Names without the suffix pass through unchanged.
func stripTunnelChildSuffix(name string) string {
	return tunnelChildSuffixRe.ReplaceAllString(name, "")
}

var tunnelChildSuffixRe = regexp.MustCompile(`-tunnel-\d+$`)

// isIPSecStateWord reports whether s is a recognised SA state token — used to tell
// a table data row from the header ("State") and dashed separator lines.
func isIPSecStateWord(s string) bool {
	switch strings.ToUpper(s) {
	case "UP", "DOWN", "CONNECTING", "ESTABLISHED", "INSTALLED", "CREATED", "REKEYING":
		return true
	default:
		return false
	}
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
	case "UP", "ESTABLISHED", "INSTALLED":
		return IPSecTunnelStateUp
	case "CONNECTING", "CREATED", "REKEYING":
		return IPSecTunnelStateConnecting
	case "DOWN":
		return IPSecTunnelStateDown
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
