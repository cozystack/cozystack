// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Cozystack Authors.

// Package denyset validates a SiteRouter instance's declared remoteCIDRs against
// the cluster-owned networks they must never overlap.
//
// It is a dependency-light leaf package — standard library net/netip plus a
// plain cluster-CIDR struct, nothing else — so it can be imported both by the
// site-router controller (internal/controller/siterouter) and by the SiteRouter
// admission check in the aggregated apiserver (pkg/registry/apps/application)
// without dragging controller-runtime into the apiserver. The controller and the
// admission plugin share this single validator so a remoteCIDR that would
// blackhole cluster traffic is rejected identically at admission time
// (synchronous Forbidden) and at reconcile time (a machine-readable Ready
// reason). See DECISIONS.md D9/D10.
package denyset

import (
	"fmt"
	"net/netip"
)

// ReasonInvalidRemoteCIDR is the stable, machine-readable reason both callers
// surface for a remoteCIDR that is malformed or overlaps a cluster network: the
// admission check as the reason of a Forbidden error, the controller as the
// reason of its Ready=False condition. It is part of the contract cozyportal
// consumes, so it must not change.
const ReasonInvalidRemoteCIDR = "InvalidRemoteCIDR"

// Machine-readable labels for the network a rejected remoteCIDR collides with.
// They appear in Rejection.Network and are stable API.
const (
	NetworkPod          = "pod"
	NetworkService      = "service"
	NetworkJoin         = "join"
	NetworkNode         = "node"
	NetworkLBPool       = "loadbalancer-pool"
	NetworkLinkLocal    = "link-local"
	NetworkLoopback     = "loopback"
	NetworkDefaultRoute = "default-route"
	NetworkMalformed    = "malformed"
	// NetworkUnsupported labels a remoteCIDR whose address family is not IPv4.
	// Phase-1 routing and the cluster CIDR keys (ipv4-*) are IPv4-only, and
	// netip.Prefix.Overlaps reports false across families, so an IPv6 (or
	// IPv4-mapped-IPv6) remoteCIDR would silently pass every overlap check and
	// be programmed as a route that can never match cluster traffic. Reject it
	// outright instead.
	NetworkUnsupported = "unsupported-address-family"
)

// Always-reserved networks enforced unconditionally, independent of the
// caller-supplied ClusterNetworks: the link-local block (which includes the
// 169.254.169.254 cloud metadata address) and host loopback. The 0.0.0.0/0
// default route is handled separately (any /0 prefix is rejected outright).
const (
	linkLocalCIDR = "169.254.0.0/16"
	loopbackCIDR  = "127.0.0.0/8"
)

// ClusterNetworks are the cluster-owned networks a tenant's remoteCIDRs must be
// disjoint from. PodCIDR/ServiceCIDR/JoinCIDR come from the cozy-system/cozystack
// ConfigMap (ipv4-pod-cidr / ipv4-svc-cidr / ipv4-join-cidr) with the
// platform-values defaults as fallback; NodeCIDRs and LBPools come from Node / LB
// pool discovery or a controller flag. The admission check is handed the same
// values. Empty string fields are skipped, so a caller that cannot discover a
// given network simply does not enforce against it. The always-reserved networks
// (link-local 169.254.0.0/16, loopback 127.0.0.0/8, and the 0.0.0.0/0 default
// route) are enforced unconditionally and need not be supplied here.
type ClusterNetworks struct {
	PodCIDR     string
	ServiceCIDR string
	JoinCIDR    string
	NodeCIDRs   []string
	LBPools     []string
}

// Rejection describes one remoteCIDR that failed validation: the offending value
// exactly as it was declared, a machine label for the network it collides with
// (one of the Network* constants, or NetworkMalformed when the value does not
// parse) and the colliding network's CIDR (empty when the value is malformed).
// Reason() is always ReasonInvalidRemoteCIDR.
type Rejection struct {
	// RemoteCIDR is the offending remoteCIDR, verbatim as declared.
	RemoteCIDR string
	// Network is the machine label of the network the value collided with, or
	// NetworkMalformed when RemoteCIDR could not be parsed as a CIDR.
	Network string
	// Collides is the colliding network's CIDR; empty when Network is
	// NetworkMalformed.
	Collides string
}

// Reason returns the stable machine-readable reason for every rejection.
func (Rejection) Reason() string { return ReasonInvalidRemoteCIDR }

// Message returns a human-readable explanation naming the offending remoteCIDR
// and the network it collides with, suitable for a Forbidden error or a Ready
// condition message.
func (r Rejection) Message() string {
	if r.Network == NetworkMalformed {
		return fmt.Sprintf("remoteCIDR %q is not a valid CIDR", r.RemoteCIDR)
	}
	if r.Network == NetworkUnsupported {
		return fmt.Sprintf("remoteCIDR %q uses an unsupported address family; only IPv4 is supported", r.RemoteCIDR)
	}
	return fmt.Sprintf("remoteCIDR %q overlaps the cluster %s network %s", r.RemoteCIDR, r.Network, r.Collides)
}

// denyNet is one network a remoteCIDR must not overlap, paired with its label.
type denyNet struct {
	label  string
	prefix netip.Prefix
}

// Validate returns one Rejection per remoteCIDR that is malformed or overlaps a
// cluster network from clusters or one of the always-reserved networks. A
// nil/empty result means every remoteCIDR is safe to route. Overlap between two
// declared remote CIDRs is not a concern — routes are namespace-scoped, so
// cross-tenant remote overlap is allowed; only overlap with the cluster networks
// is rejected. The function is pure and hermetic: no I/O, no globals. Every
// offender is reported (validation does not stop at the first).
func Validate(remoteCIDRs []string, clusters ClusterNetworks) []Rejection {
	deny := buildDenyNetworks(clusters)

	var rejections []Rejection
	for _, raw := range remoteCIDRs {
		p, err := netip.ParsePrefix(raw)
		if err != nil {
			rejections = append(rejections, Rejection{RemoteCIDR: raw, Network: NetworkMalformed})
			continue
		}
		p = p.Masked()

		// IPv4-only (Phase 1). netip.Prefix.Overlaps is false across address
		// families, so a non-IPv4 prefix would pass every cluster-network check
		// silently; reject it before any overlap test rather than program a route
		// that can never match cluster traffic.
		if !p.Addr().Is4() {
			rejections = append(rejections, Rejection{RemoteCIDR: raw, Network: NetworkUnsupported})
			continue
		}

		// A default route (or any /0) blackholes all cluster traffic. Report it
		// as such rather than as an overlap with whichever cluster network is
		// checked first.
		if p.Bits() == 0 {
			rejections = append(rejections, Rejection{
				RemoteCIDR: raw,
				Network:    NetworkDefaultRoute,
				Collides:   "0.0.0.0/0",
			})
			continue
		}

		// First colliding network wins; the deny list is ordered so the most
		// specific, operator-recognisable label is reported. Overlaps() catches
		// containment in either direction — a remoteCIDR inside a cluster network
		// and a broad remoteCIDR that swallows one.
		for _, d := range deny {
			if p.Overlaps(d.prefix) {
				rejections = append(rejections, Rejection{
					RemoteCIDR: raw,
					Network:    d.label,
					Collides:   d.prefix.String(),
				})
				break
			}
		}
	}
	return rejections
}

// buildDenyNetworks assembles the ordered list of networks a remoteCIDR must not
// overlap: the caller-supplied cluster networks (skipping empty/unparseable
// fields, per the empty-field-skipped contract) followed by the always-reserved
// link-local and loopback blocks.
func buildDenyNetworks(c ClusterNetworks) []denyNet {
	var out []denyNet
	add := func(label, cidr string) {
		if cidr == "" {
			return
		}
		if p, err := netip.ParsePrefix(cidr); err == nil {
			out = append(out, denyNet{label: label, prefix: p.Masked()})
		}
	}

	add(NetworkPod, c.PodCIDR)
	add(NetworkService, c.ServiceCIDR)
	add(NetworkJoin, c.JoinCIDR)
	for _, n := range c.NodeCIDRs {
		add(NetworkNode, n)
	}
	for _, lb := range c.LBPools {
		add(NetworkLBPool, lb)
	}
	add(NetworkLinkLocal, linkLocalCIDR)
	add(NetworkLoopback, loopbackCIDR)

	return out
}
