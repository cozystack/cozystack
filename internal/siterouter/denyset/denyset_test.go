// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Cozystack Authors.

package denyset

import (
	"strings"
	"testing"
)

// defaultClusters mirrors a plausible runtime ClusterNetworks: the
// platform-values default pod/service/join CIDRs, a single node network and one
// LoadBalancer pool. Tests that only need cluster-network overlap feed this;
// individual cases override fields where the collision under test lives.
func defaultClusters() ClusterNetworks {
	return ClusterNetworks{
		PodCIDR:     "10.244.0.0/16",
		ServiceCIDR: "10.96.0.0/16",
		JoinCIDR:    "100.64.0.0/16",
		NodeCIDRs:   []string{"192.168.100.0/24"},
		LBPools:     []string{"203.0.113.0/24"},
	}
}

// TestValidate is the deny-set truth table (T07 Acceptance "an overlapping
// remoteCIDR is rejected with InvalidRemoteCIDR"; T12 deny-set case). Each row
// declares a single remoteCIDR and asserts whether Validate rejects it and, when
// it does, which network it names.
func TestValidate(t *testing.T) {
	tests := []struct {
		name        string
		remoteCIDR  string
		clusters    ClusterNetworks
		wantReject  bool
		wantNetwork string // colliding-network label when wantReject
	}{
		{
			name:        "overlaps pod CIDR",
			remoteCIDR:  "10.244.5.0/24",
			clusters:    defaultClusters(),
			wantReject:  true,
			wantNetwork: NetworkPod,
		},
		{
			name:        "overlaps service CIDR",
			remoteCIDR:  "10.96.1.0/24",
			clusters:    defaultClusters(),
			wantReject:  true,
			wantNetwork: NetworkService,
		},
		{
			name:        "overlaps join CIDR",
			remoteCIDR:  "100.64.0.0/20",
			clusters:    defaultClusters(),
			wantReject:  true,
			wantNetwork: NetworkJoin,
		},
		{
			name:        "overlaps node network",
			remoteCIDR:  "192.168.100.0/25",
			clusters:    defaultClusters(),
			wantReject:  true,
			wantNetwork: NetworkNode,
		},
		{
			name:        "overlaps link-local (metadata)",
			remoteCIDR:  "169.254.169.254/32",
			clusters:    defaultClusters(),
			wantReject:  true,
			wantNetwork: NetworkLinkLocal,
		},
		{
			name:        "equals the whole link-local block",
			remoteCIDR:  "169.254.0.0/16",
			clusters:    defaultClusters(),
			wantReject:  true,
			wantNetwork: NetworkLinkLocal,
		},
		{
			name:        "overlaps the LoadBalancer pool",
			remoteCIDR:  "203.0.113.128/25",
			clusters:    defaultClusters(),
			wantReject:  true,
			wantNetwork: NetworkLBPool,
		},
		{
			name:        "overlaps loopback",
			remoteCIDR:  "127.0.0.0/8",
			clusters:    defaultClusters(),
			wantReject:  true,
			wantNetwork: NetworkLoopback,
		},
		{
			name:        "default route blackholes everything",
			remoteCIDR:  "0.0.0.0/0",
			clusters:    defaultClusters(),
			wantReject:  true,
			wantNetwork: NetworkDefaultRoute,
		},
		{
			name:        "prefix broad enough to swallow the pod CIDR",
			remoteCIDR:  "10.0.0.0/8", // contains pod 10.244.0.0/16
			clusters:    defaultClusters(),
			wantReject:  true,
			wantNetwork: NetworkPod,
		},
		{
			name:       "disjoint CIDR passes",
			remoteCIDR: "172.31.0.0/16",
			clusters:   defaultClusters(),
			wantReject: false,
		},
		{
			name:       "another disjoint CIDR passes",
			remoteCIDR: "198.51.100.0/24",
			clusters:   defaultClusters(),
			wantReject: false,
		},
		{
			name:        "malformed CIDR rejected",
			remoteCIDR:  "not-a-cidr",
			clusters:    defaultClusters(),
			wantReject:  true,
			wantNetwork: NetworkMalformed,
		},
		{
			name:        "out-of-range mask rejected as malformed",
			remoteCIDR:  "10.0.0.0/33",
			clusters:    defaultClusters(),
			wantReject:  true,
			wantNetwork: NetworkMalformed,
		},
		{
			name:        "bare IP without mask rejected as malformed",
			remoteCIDR:  "172.31.0.5",
			clusters:    defaultClusters(),
			wantReject:  true,
			wantNetwork: NetworkMalformed,
		},
		{
			// A cluster network the caller could not discover is simply not
			// enforced: with no node/LB info an otherwise-node-overlapping CIDR
			// passes. This documents the "empty field skipped" contract.
			name:       "unset cluster field is not enforced",
			remoteCIDR: "192.168.100.0/25",
			clusters: ClusterNetworks{
				PodCIDR:     "10.244.0.0/16",
				ServiceCIDR: "10.96.0.0/16",
				JoinCIDR:    "100.64.0.0/16",
			},
			wantReject: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := Validate([]string{tt.remoteCIDR}, tt.clusters)
			if !tt.wantReject {
				if len(got) != 0 {
					t.Fatalf("Validate(%q) = %+v, want no rejections", tt.remoteCIDR, got)
				}
				return
			}
			if len(got) != 1 {
				t.Fatalf("Validate(%q) = %+v, want exactly one rejection", tt.remoteCIDR, got)
			}
			r := got[0]
			if r.Reason() != ReasonInvalidRemoteCIDR {
				t.Errorf("rejection reason = %q, want %q", r.Reason(), ReasonInvalidRemoteCIDR)
			}
			if r.RemoteCIDR != tt.remoteCIDR {
				t.Errorf("rejection RemoteCIDR = %q, want %q (must name the offending CIDR)", r.RemoteCIDR, tt.remoteCIDR)
			}
			if r.Network != tt.wantNetwork {
				t.Errorf("rejection Network = %q, want %q", r.Network, tt.wantNetwork)
			}
			// The message must name the offending CIDR so an operator can act on
			// the Forbidden / Ready reason without cross-referencing.
			if msg := r.Message(); !strings.Contains(msg, tt.remoteCIDR) {
				t.Errorf("rejection Message() = %q, want it to name %q", msg, tt.remoteCIDR)
			}
		})
	}
}

// TestValidate_CrossTenantRemoteOverlapAllowed proves the boundary from the T07
// tech spec: two remote CIDRs that overlap each other but are both disjoint from
// the cluster networks are fine — routes are namespace-scoped, so only overlap
// with the cluster networks is rejected. Passing them in one call stands in for
// two tenants declaring overlapping remotes.
func TestValidate_CrossTenantRemoteOverlapAllowed(t *testing.T) {
	got := Validate([]string{"172.31.0.0/16", "172.31.5.0/24"}, defaultClusters())
	if len(got) != 0 {
		t.Fatalf("overlapping-but-cluster-disjoint remote CIDRs must be allowed, got rejections %+v", got)
	}
}

// TestValidate_ReportsEveryOffender proves Validate does not stop at the first
// bad entry: a batch with two colliding CIDRs and one good one yields two
// rejections, each naming its own offending value.
func TestValidate_ReportsEveryOffender(t *testing.T) {
	got := Validate([]string{"10.244.1.0/24", "172.31.0.0/16", "10.96.9.0/24"}, defaultClusters())
	if len(got) != 2 {
		t.Fatalf("want 2 rejections (pod + service overlap), got %d: %+v", len(got), got)
	}
	seen := map[string]string{}
	for _, r := range got {
		seen[r.RemoteCIDR] = r.Network
	}
	if seen["10.244.1.0/24"] != NetworkPod {
		t.Errorf("10.244.1.0/24 should collide with %q, got %q", NetworkPod, seen["10.244.1.0/24"])
	}
	if seen["10.96.9.0/24"] != NetworkService {
		t.Errorf("10.96.9.0/24 should collide with %q, got %q", NetworkService, seen["10.96.9.0/24"])
	}
	if _, ok := seen["172.31.0.0/16"]; ok {
		t.Errorf("172.31.0.0/16 is disjoint and must not be rejected")
	}
}
