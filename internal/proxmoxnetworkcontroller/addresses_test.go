/*
Copyright 2025 The Cozystack Authors.

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

package proxmoxnetworkcontroller

import (
	"net/netip"
	"testing"
)

func TestParseAddrRange(t *testing.T) {
	cases := map[string][2]string{
		"10.0.0.0/30":            {"10.0.0.0", "10.0.0.3"},
		"10.0.0.7/30":            {"10.0.0.4", "10.0.0.7"},
		"10.0.0.5":               {"10.0.0.5", "10.0.0.5"},
		"10.0.0.10..10.0.0.20":   {"10.0.0.10", "10.0.0.20"},
		"10.0.0.10-10.0.0.20":    {"10.0.0.10", "10.0.0.20"},
		"fd00::1-fd00::ff":       {"fd00::1", "fd00::ff"},
		" 10.0.0.1 .. 10.0.0.2 ": {"10.0.0.1", "10.0.0.2"},
	}
	for in, want := range cases {
		r, err := parseAddrRange(in)
		if err != nil {
			t.Fatalf("%q: %v", in, err)
		}
		if r.from != netip.MustParseAddr(want[0]) || r.to != netip.MustParseAddr(want[1]) {
			t.Fatalf("%q: got %s-%s, want %s-%s", in, r.from, r.to, want[0], want[1])
		}
	}
	for _, bad := range []string{"", "10.0.0.20-10.0.0.10", "10.0.0.1-fd00::1", "nope", "10.0.0.1..x"} {
		if _, err := parseAddrRange(bad); err == nil {
			t.Fatalf("%q: want error", bad)
		}
	}
}

func TestCoveredBy(t *testing.T) {
	r := func(s string) addrRange {
		x, err := parseAddrRange(s)
		if err != nil {
			t.Fatal(err)
		}
		return x
	}
	if !coveredBy(r("10.0.0.100-10.0.0.199"), []addrRange{r("10.0.0.100-10.0.0.199")}) {
		t.Fatal("exact range must be covered")
	}
	if !coveredBy(r("10.0.0.100-10.0.0.199"), []addrRange{r("10.0.0.150..10.0.0.250"), r("10.0.0.90..10.0.0.149")}) {
		t.Fatal("two adjacent ranges in any order must cover")
	}
	if coveredBy(r("10.0.0.100-10.0.0.199"), []addrRange{r("10.0.0.100..10.0.0.150"), r("10.0.0.152..10.0.0.199")}) {
		t.Fatal("a one-address gap must not be covered")
	}
	if coveredBy(r("10.0.0.100-10.0.0.199"), nil) {
		t.Fatal("nothing covers nothing")
	}
	if !coveredBy(r("255.255.255.250-255.255.255.255"), []addrRange{r("255.255.255.0/24")}) {
		t.Fatal("a range ending at the last address must be covered without overflowing")
	}
}

func TestCheckPoolSplit(t *testing.T) {
	pool := func(addrs ...string) InClusterIPPoolSpec {
		return InClusterIPPoolSpec{Addresses: addrs, Prefix: 24, Gateway: "10.208.64.1"}
	}
	excl := []string{"10.208.64.100..10.208.64.199"}

	if reason, msg := checkPoolSplit("10.208.64.0/24", "10.208.64.1", excl, pool("10.208.64.100-10.208.64.199")); reason != "" {
		t.Fatalf("valid split refused: %s %s", reason, msg)
	}
	cases := map[string]struct {
		pool InClusterIPPoolSpec
		excl []string
	}{
		"PoolOverlapsSubnet":  {pool: pool("10.208.64.100-10.208.64.200"), excl: excl},
		"PoolOutsideSubnet":   {pool: pool("10.208.65.10-10.208.65.20"), excl: excl},
		"PoolGatewayMismatch": {pool: InClusterIPPoolSpec{Addresses: []string{"10.208.64.100"}, Prefix: 24, Gateway: "10.208.64.254"}, excl: excl},
		"PoolPrefixMismatch":  {pool: InClusterIPPoolSpec{Addresses: []string{"10.208.64.100"}, Prefix: 25, Gateway: "10.208.64.1"}, excl: excl},
		"PoolAddressInvalid":  {pool: pool("ten"), excl: excl},
	}
	for want, tc := range cases {
		if reason, _ := checkPoolSplit("10.208.64.0/24", "10.208.64.1", tc.excl, tc.pool); reason != want {
			t.Fatalf("got reason %q, want %q", reason, want)
		}
	}
}
