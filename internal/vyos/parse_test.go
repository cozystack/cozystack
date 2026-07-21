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

package vyos_test

import (
	"testing"

	"github.com/cozystack/cozystack/internal/vyos"
)

const sampleIPSecEstablished = `
Connections:
       aws-prod:  203.0.113.15...203.0.113.10  IKEv2, dpddelay=15s, dpdtimeout=30s, dpdaction=clear
       aws-prod:   local:  [203.0.113.15] uses pre-shared key authentication
       aws-prod:   remote: [203.0.113.10] uses pre-shared key authentication
       aws-prod:   child:  10.0.0.0/24 === 172.31.0.0/16 TUNNEL, dpdaction=clear
Routed Connections:
     aws-prod-tunnel-1:  10.0.0.0/24|<=>|172.31.0.0/16
Security Associations (1 up, 0 connecting):
       aws-prod[42]: ESTABLISHED 5 seconds ago, 203.0.113.15[router]...203.0.113.10[peer]
       aws-prod[42]: IKEv2 SPIs: 9ab... reauth in 53 minutes
       aws-prod{17}:  INSTALLED, TUNNEL, reqid 1, ESP in UDP SPIs: c4fc8e60_i be9b8866_o
       aws-prod{17}:   10.0.0.0/24 === 172.31.0.0/16
`

const sampleIPSecConnecting = `
Connections:
       partner:  203.0.113.15...198.51.100.20  IKEv2
Security Associations (0 up, 1 connecting):
       partner[3]: CONNECTING, IKE_SA_INIT
`

const sampleIPSecDown = `
Connections:
       lonely:  203.0.113.15...198.51.100.99  IKEv2
Security Associations (0 up, 0 connecting):
`

const sampleBGPSummary = `
BGP router identifier 203.0.113.15, local AS number 65001 vrf-id 0
BGP table version 1
RIB entries 1, using 192 bytes of memory
Peers 2, using 21 KiB of memory

Neighbor        V         AS   MsgRcvd   MsgSent   TblVer  InQ OutQ  Up/Down State/PfxRcd   PfxSnt Desc
203.0.113.1     4      65000        12        12        0    0    0 00:05:03 Established        2 ISP peer
203.0.113.2     4      65000         0         0        0    0    0    never Idle               0
`

func TestParseIPSecSA_Established(t *testing.T) {
	t.Parallel()

	obs := vyos.ParseIPSecSA(sampleIPSecEstablished)

	if len(obs) != 1 {
		t.Fatalf("expected 1 peer, got %d: %+v", len(obs), obs)
	}

	if obs[0].PeerName != "aws-prod" {
		t.Errorf("expected PeerName=aws-prod, got %q", obs[0].PeerName)
	}

	if obs[0].PeerAddress != "203.0.113.10" {
		t.Errorf("expected PeerAddress=203.0.113.10, got %q", obs[0].PeerAddress)
	}

	if obs[0].State != vyos.IPSecTunnelStateUp {
		t.Errorf("expected State=Up, got %q", obs[0].State)
	}
}

func TestParseIPSecSA_Connecting(t *testing.T) {
	t.Parallel()

	obs := vyos.ParseIPSecSA(sampleIPSecConnecting)

	if len(obs) != 1 {
		t.Fatalf("expected 1 peer, got %d", len(obs))
	}

	if obs[0].State != vyos.IPSecTunnelStateConnecting {
		t.Errorf("expected State=Connecting, got %q", obs[0].State)
	}
}

func TestParseIPSecSA_Down(t *testing.T) {
	t.Parallel()

	obs := vyos.ParseIPSecSA(sampleIPSecDown)

	if len(obs) != 1 {
		t.Fatalf("expected 1 peer, got %d", len(obs))
	}

	if obs[0].State != vyos.IPSecTunnelStateDown {
		t.Errorf("expected State=Down when no SA, got %q", obs[0].State)
	}
}

func TestParseIPSecSA_Empty(t *testing.T) {
	t.Parallel()

	if obs := vyos.ParseIPSecSA(""); len(obs) != 0 {
		t.Errorf("expected no peers, got %d", len(obs))
	}
}

func TestParseBGPSummary_EstablishedAndIdle(t *testing.T) {
	t.Parallel()

	obs := vyos.ParseBGPSummary(sampleBGPSummary)

	if len(obs) != 2 {
		t.Fatalf("expected 2 neighbours, got %d: %+v", len(obs), obs)
	}

	if obs[0].PeerAddress != "203.0.113.1" || obs[0].Session != vyos.BGPSessionStateEstablished {
		t.Errorf("first neighbour wrong: %+v", obs[0])
	}

	if obs[0].Uptime != "00:05:03" {
		t.Errorf("expected uptime=00:05:03, got %q", obs[0].Uptime)
	}

	if obs[1].PeerAddress != "203.0.113.2" || obs[1].Session != vyos.BGPSessionStateIdle {
		t.Errorf("second neighbour wrong: %+v", obs[1])
	}

	if obs[1].Uptime != "never" {
		t.Errorf("expected uptime=never, got %q", obs[1].Uptime)
	}
}

func TestParseBGPSummary_IgnoresHeaderLines(t *testing.T) {
	t.Parallel()

	// Only the header is present; no neighbour rows.
	header := `Neighbor        V         AS   MsgRcvd   MsgSent   TblVer  InQ OutQ  Up/Down State/PfxRcd`
	if obs := vyos.ParseBGPSummary(header); len(obs) != 0 {
		t.Errorf("expected 0 neighbours from header-only output, got %d", len(obs))
	}
}

const sampleInterfacesDetail = `
eth0: <BROADCAST,MULTICAST,UP,LOWER_UP> mtu 1500 qdisc fq_codel state UP group default qlen 1000
    link/ether 52:54:00:11:22:33 brd ff:ff:ff:ff:ff:ff
    inet 203.0.113.10/24 brd 203.0.113.255 scope global dynamic eth0
       valid_lft 85994sec preferred_lft 85994sec
    RX:  bytes  packets  errors  dropped  overrun       mcast
      56735451   179841       0        0        0      142380
    TX:  bytes  packets  errors  dropped  carrier  collisions
       5601460    62595       0        0        0           0
eth1: <BROADCAST,MULTICAST,UP,LOWER_UP> mtu 1500 qdisc fq_codel state UP group default qlen 1000
    link/ether 52:54:00:AA:BB:CC brd ff:ff:ff:ff:ff:ff
    inet 10.0.0.1/24 brd 10.0.0.255 scope global eth1
eth1.100@eth1: <BROADCAST,MULTICAST,UP,LOWER_UP> mtu 1500 qdisc noqueue state UP group default qlen 1000
    link/ether 52:54:00:aa:bb:cc brd ff:ff:ff:ff:ff:ff
lo: <LOOPBACK,UP,LOWER_UP> mtu 65536 qdisc noqueue state UNKNOWN group default qlen 1000
    link/loopback 00:00:00:00:00:00 brd 00:00:00:00:00:00
    inet 127.0.0.1/8 scope host lo
`

func TestParseInterfacesDetail_ExtractsEthernetMACs(t *testing.T) {
	t.Parallel()

	obs := vyos.ParseInterfacesDetail(sampleInterfacesDetail)

	if len(obs) != 2 {
		t.Fatalf("expected 2 ethernet observations, got %d: %+v", len(obs), obs)
	}

	if obs[0].Device != "eth0" || obs[0].MAC != "52:54:00:11:22:33" {
		t.Errorf("first observation wrong: %+v", obs[0])
	}

	// MACs are normalised to lowercase regardless of kernel output case.
	if obs[1].Device != "eth1" || obs[1].MAC != "52:54:00:aa:bb:cc" {
		t.Errorf("second observation wrong: %+v", obs[1])
	}
}

func TestParseInterfacesDetail_NumberedIPAddrStyle(t *testing.T) {
	t.Parallel()

	// Some VyOS builds emit raw `ip addr`-style numbered headers.
	numbered := `
2: eth0: <BROADCAST,MULTICAST,UP,LOWER_UP> mtu 1500 qdisc fq_codel state UP group default qlen 1000
    link/ether 02:00:c0:a8:01:01 brd ff:ff:ff:ff:ff:ff
`

	obs := vyos.ParseInterfacesDetail(numbered)

	if len(obs) != 1 {
		t.Fatalf("expected 1 observation, got %d: %+v", len(obs), obs)
	}

	if obs[0].Device != "eth0" || obs[0].MAC != "02:00:c0:a8:01:01" {
		t.Errorf("observation wrong: %+v", obs[0])
	}
}

func TestParseInterfacesDetail_Empty(t *testing.T) {
	t.Parallel()

	if obs := vyos.ParseInterfacesDetail(""); len(obs) != 0 {
		t.Errorf("expected 0 observations from empty output, got %d", len(obs))
	}
}
