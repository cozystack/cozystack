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
			// IPv6 parses fine but Overlaps is false across families, so without
			// an explicit family check it would slip past every cluster network.
			name:        "IPv6 remoteCIDR rejected as unsupported",
			remoteCIDR:  "fd00::/8",
			clusters:    defaultClusters(),
			wantReject:  true,
			wantNetwork: NetworkUnsupported,
		},
		{
			name:        "IPv4-mapped IPv6 rejected as unsupported",
			remoteCIDR:  "::ffff:10.244.0.0/104",
			clusters:    defaultClusters(),
			wantReject:  true,
			wantNetwork: NetworkUnsupported,
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
			got, err := Validate(Inputs{RemoteCIDRs: []string{tt.remoteCIDR}}, tt.clusters)
			if err != nil {
				t.Fatalf("Validate(%q) returned an unexpected error: %v", tt.remoteCIDR, err)
			}
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
			if r.Value != tt.remoteCIDR {
				t.Errorf("rejection Value = %q, want %q (must name the offending CIDR)", r.Value, tt.remoteCIDR)
			}
			if r.Field != FieldRemoteCIDRs {
				t.Errorf("rejection Field = %q, want %q", r.Field, FieldRemoteCIDRs)
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
	got, err := Validate(Inputs{RemoteCIDRs: []string{"172.31.0.0/16", "172.31.5.0/24"}}, defaultClusters())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("overlapping-but-cluster-disjoint remote CIDRs must be allowed, got rejections %+v", got)
	}
}

// TestValidate_ReportsEveryOffender proves Validate does not stop at the first
// bad entry: a batch with two colliding CIDRs and one good one yields two
// rejections, each naming its own offending value.
func TestValidate_ReportsEveryOffender(t *testing.T) {
	got, err := Validate(Inputs{RemoteCIDRs: []string{"10.244.1.0/24", "172.31.0.0/16", "10.96.9.0/24"}}, defaultClusters())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("want 2 rejections (pod + service overlap), got %d: %+v", len(got), got)
	}
	seen := map[string]string{}
	for _, r := range got {
		seen[r.Value] = r.Network
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

// TestClusterNetworksFromConfigMap covers the shared ConfigMap→ClusterNetworks
// mapping both the controller and the apiserver admission check rely on: a nil
// map (absent ConfigMap) yields the platform defaults, present keys override, and
// an empty-string value falls back to the default rather than blanking the field.
func TestClusterNetworksFromConfigMap(t *testing.T) {
	t.Run("nil map yields defaults", func(t *testing.T) {
		got := ClusterNetworksFromConfigMap(nil)
		if got.PodCIDR != DefaultPodCIDR || got.ServiceCIDR != DefaultServiceCIDR || got.JoinCIDR != DefaultJoinCIDR {
			t.Fatalf("nil map must yield the platform defaults, got %+v", got)
		}
	})

	t.Run("present keys override defaults", func(t *testing.T) {
		got := ClusterNetworksFromConfigMap(map[string]string{
			ConfigMapKeyPodCIDR:     "192.168.0.0/16",
			ConfigMapKeyServiceCIDR: "172.16.0.0/12",
			ConfigMapKeyJoinCIDR:    "100.65.0.0/16",
		})
		if got.PodCIDR != "192.168.0.0/16" {
			t.Errorf("PodCIDR = %q, want the ConfigMap value 192.168.0.0/16", got.PodCIDR)
		}
		if got.ServiceCIDR != "172.16.0.0/12" {
			t.Errorf("ServiceCIDR = %q, want the ConfigMap value 172.16.0.0/12", got.ServiceCIDR)
		}
		if got.JoinCIDR != "100.65.0.0/16" {
			t.Errorf("JoinCIDR = %q, want the ConfigMap value 100.65.0.0/16", got.JoinCIDR)
		}
	})

	t.Run("empty value falls back to default", func(t *testing.T) {
		got := ClusterNetworksFromConfigMap(map[string]string{ConfigMapKeyPodCIDR: ""})
		if got.PodCIDR != DefaultPodCIDR {
			t.Errorf("empty pod-cidr must fall back to %q, got %q", DefaultPodCIDR, got.PodCIDR)
		}
	})

	// The pairing that makes a typo dangerous, pinned next to the fallback it
	// defeats: a malformed value is NOT a missing one. It is carried through and
	// displaces the platform default, which is why Validate has to treat it as an
	// error rather than skip it. See TestValidate_MalformedClusterNetworkFailsClosed.
	t.Run("malformed value displaces the default rather than falling back", func(t *testing.T) {
		got := ClusterNetworksFromConfigMap(map[string]string{ConfigMapKeyPodCIDR: "10.244.0.0/33"})
		if got.PodCIDR != "10.244.0.0/33" {
			t.Errorf("PodCIDR = %q, want the malformed ConfigMap value carried through", got.PodCIDR)
		}
		if _, err := Validate(Inputs{RemoteCIDRs: []string{"10.244.1.0/24"}}, got); err == nil {
			t.Error("a deny set built from the malformed value must fail closed")
		}
	})
}

// TestValidate_StaticRouteDestinations pins the second of the three fields that
// program routes. staticRoutes[].destination reaches `protocols static route`
// directly and its schema pattern admits every prefix length, so /0 and a
// cluster-overlapping prefix both have to be caught here or nowhere.
func TestValidate_StaticRouteDestinations(t *testing.T) {
	tests := []struct {
		name        string
		destination string
		wantNetwork string
	}{
		{"default route", "0.0.0.0/0", NetworkDefaultRoute},
		{"pod CIDR", "10.244.7.0/24", NetworkPod},
		{"service CIDR", "10.96.0.0/16", NetworkService},
		{"node address", "192.168.100.0/24", NetworkNode},
		{"link-local", "169.254.169.254/32", NetworkLinkLocal},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := Validate(Inputs{StaticRouteDestinations: []string{tt.destination}}, defaultClusters())
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if len(got) != 1 {
				t.Fatalf("Validate(staticRoute %q) = %+v, want exactly one rejection", tt.destination, got)
			}
			if got[0].Network != tt.wantNetwork {
				t.Errorf("Network = %q, want %q", got[0].Network, tt.wantNetwork)
			}
			// The message has to name the field, not just the address: the same
			// value is legal in one field and rejected in another, so "10.96.0.0/16
			// overlaps the service network" alone does not say what to edit.
			if msg := got[0].Message(); !strings.Contains(msg, FieldStaticRoute) {
				t.Errorf("Message() = %q, want it to name %q", msg, FieldStaticRoute)
			}
		})
	}

	t.Run("disjoint destination is allowed", func(t *testing.T) {
		got, err := Validate(Inputs{StaticRouteDestinations: []string{"172.31.0.0/16"}}, defaultClusters())
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(got) != 0 {
			t.Fatalf("a cluster-disjoint static route must be allowed, got %+v", got)
		}
	})
}

// TestValidate_BGPNeighborAddresses pins the third field. A neighbour address is
// a bare host rather than a prefix, and it carries a consequence the other two do
// not: renderBGPFirewallAccept writes an input-filter accept for TCP 179 from
// that address, so an unjudged neighbour opens the management chain for an
// address the deny set would reject as a remoteCIDR.
func TestValidate_BGPNeighborAddresses(t *testing.T) {
	tests := []struct {
		name        string
		address     string
		wantNetwork string
	}{
		{"node address", "192.168.100.10", NetworkNode},
		{"pod address", "10.244.3.9", NetworkPod},
		{"service address", "10.96.0.1", NetworkService},
		{"loadbalancer address", "203.0.113.20", NetworkLBPool},
		{"metadata address", "169.254.169.254", NetworkLinkLocal},
		{"loopback", "127.0.0.1", NetworkLoopback},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := Validate(Inputs{BGPNeighborAddresses: []string{tt.address}}, defaultClusters())
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if len(got) != 1 {
				t.Fatalf("Validate(bgp neighbor %q) = %+v, want exactly one rejection", tt.address, got)
			}
			if got[0].Network != tt.wantNetwork {
				t.Errorf("Network = %q, want %q", got[0].Network, tt.wantNetwork)
			}
			if got[0].Field != FieldBGPNeighbor {
				t.Errorf("Field = %q, want %q", got[0].Field, FieldBGPNeighbor)
			}
		})
	}

	t.Run("a prefix is not a neighbour address", func(t *testing.T) {
		// The field is a bare host; a value with a prefix length is malformed
		// rather than silently masked to its network.
		got, err := Validate(Inputs{BGPNeighborAddresses: []string{"198.51.100.0/24"}}, defaultClusters())
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(got) != 1 || got[0].Network != NetworkMalformed {
			t.Fatalf("want one malformed rejection, got %+v", got)
		}
	})

	t.Run("disjoint neighbour is allowed", func(t *testing.T) {
		got, err := Validate(Inputs{BGPNeighborAddresses: []string{"198.51.100.7"}}, defaultClusters())
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(got) != 0 {
			t.Fatalf("a cluster-disjoint neighbour must be allowed, got %+v", got)
		}
	})
}

// TestValidate_AllFieldsReportedTogether proves the three fields share one pass
// and one result: a request that is wrong in all three is told so all at once,
// rather than one field per apply.
func TestValidate_AllFieldsReportedTogether(t *testing.T) {
	got, err := Validate(Inputs{
		RemoteCIDRs:             []string{"10.244.1.0/24"},
		StaticRouteDestinations: []string{"0.0.0.0/0"},
		BGPNeighborAddresses:    []string{"192.168.100.10"},
	}, defaultClusters())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("want one rejection per offending field, got %d: %+v", len(got), got)
	}
	seen := map[string]string{}
	for _, r := range got {
		seen[r.Field] = r.Network
	}
	for field, want := range map[string]string{
		FieldRemoteCIDRs: NetworkPod,
		FieldStaticRoute: NetworkDefaultRoute,
		FieldBGPNeighbor: NetworkNode,
	} {
		if seen[field] != want {
			t.Errorf("field %q collided with %q, want %q", field, seen[field], want)
		}
	}
}

// TestValidate_MalformedClusterNetworkFailsClosed is the regression for the
// fail-open this used to have. buildDenyNetworks dropped an unparseable cluster
// network with no error and no log, and ClusterNetworksFromConfigMap lets any
// non-empty value displace the platform default — so an operator typo in the
// cozystack ConfigMap made the guard NARROWER than leaving the key out
// altogether, and a remoteCIDR equal to the real pod CIDR sailed through both
// enforcement points.
func TestValidate_MalformedClusterNetworkFailsClosed(t *testing.T) {
	clusters := defaultClusters()
	clusters.PodCIDR = "10.244.0.0/33" // an operator typo: not a valid prefix

	got, err := Validate(Inputs{RemoteCIDRs: []string{"10.244.1.0/24"}}, clusters)
	if err == nil {
		t.Fatalf("a malformed cluster network must fail the check, got rejections %+v and no error", got)
	}
	if got != nil {
		t.Errorf("no rejections should be returned alongside the error, got %+v", got)
	}
	if !strings.Contains(err.Error(), "10.244.0.0/33") {
		t.Errorf("error must name the offending value, got %q", err)
	}
}

// TestBuildDenyNetworks covers the assembly directly, which nothing did before:
// the empty-field-skipped contract, the always-reserved entries that are added
// whatever the caller passes, and the error every malformed field contributes.
func TestBuildDenyNetworks(t *testing.T) {
	t.Run("empty fields are skipped, reserved blocks always present", func(t *testing.T) {
		got, err := buildDenyNetworks(ClusterNetworks{})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		labels := map[string]bool{}
		for _, d := range got {
			labels[d.label] = true
		}
		if !labels[NetworkLinkLocal] || !labels[NetworkLoopback] {
			t.Errorf("the reserved link-local and loopback blocks must be present regardless of input, got %+v", got)
		}
		if labels[NetworkPod] || labels[NetworkService] || labels[NetworkJoin] {
			t.Errorf("empty cluster fields must be skipped, got %+v", got)
		}
	})

	t.Run("every malformed field is reported", func(t *testing.T) {
		_, err := buildDenyNetworks(ClusterNetworks{
			PodCIDR:     "not-a-cidr",
			ServiceCIDR: "10.96.0.0/16",
			NodeCIDRs:   []string{"192.168.100.0/24", "192.168.300.0/24"},
		})
		if err == nil {
			t.Fatal("want an error naming both malformed fields")
		}
		for _, want := range []string{"not-a-cidr", "192.168.300.0/24"} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("error %q must name %q", err, want)
			}
		}
	})
}
