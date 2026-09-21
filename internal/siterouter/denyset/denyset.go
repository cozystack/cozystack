// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Cozystack Authors.

// Package denyset validates a SiteRouter instance's declared remoteCIDRs against
// the cluster-owned networks they must never overlap.
//
// The pure validator remains standard-library-only; discovery.go adds the shared
// Kubernetes object resolver used by both the site-router controller and the
// SiteRouter admission check. Keeping discovery beside validation prevents the
// two enforcement points from silently assembling different deny-sets.
package denyset

import (
	"errors"
	"fmt"
	"net/netip"
)

// ReasonInvalidRemoteCIDR is the stable, machine-readable reason both callers
// surface for a remoteCIDR that is malformed or overlaps a cluster network: the
// admission check in a Forbidden error and the controller in a Warning Event.
// It is part of the contract the upstream consumer consumes, so it must not
// change.
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

// Machine-readable names for the values fields the deny set judges. They appear
// in Rejection.Field and in the message a tenant sees, so a rejection says which
// field to fix rather than only which address is wrong.
const (
	FieldRemoteCIDRs = "remoteCIDRs"
	FieldStaticRoute = "staticRoutes[].destination"
	FieldBGPNeighbor = "bgp.neighbors[].address"
)

// Always-reserved networks enforced unconditionally, independent of the
// caller-supplied ClusterNetworks: the link-local block (which includes the
// 169.254.169.254 cloud metadata address) and host loopback. The 0.0.0.0/0
// default route is handled separately (any /0 prefix is rejected outright).
const (
	linkLocalCIDR = "169.254.0.0/16"
	loopbackCIDR  = "127.0.0.0/8"
)

// ConfigMap keys the cluster-wide cozy-system/cozystack ConfigMap exposes the
// cluster CIDRs under. Both callers (the site-router controller and the
// SiteRouter admission check) read the same keys, so they live here — the single
// source of truth for the ConfigMap→ClusterNetworks mapping (D10).
const (
	ConfigMapKeyPodCIDR     = "ipv4-pod-cidr"
	ConfigMapKeyServiceCIDR = "ipv4-svc-cidr"
	ConfigMapKeyJoinCIDR    = "ipv4-join-cidr"
)

// Platform-values defaults for the cluster networks
// (packages/core/platform/values.yaml networking.{podCIDR,serviceCIDR,joinCIDR}),
// used when the cozystack ConfigMap is absent or a key is unset so validation
// fails safe rather than skipping a network it could not discover.
const (
	DefaultPodCIDR     = "10.244.0.0/16"
	DefaultServiceCIDR = "10.96.0.0/16"
	DefaultJoinCIDR    = "100.64.0.0/16"
)

// ClusterNetworksFromConfigMap maps the raw data of the cozy-system/cozystack
// ConfigMap into ClusterNetworks, applying the platform-values defaults for any
// key that is absent or empty. Passing a nil map (the ConfigMap does not exist)
// yields the all-defaults set. NodeCIDRs and LBPools are left empty here and are
// populated from live Nodes and Services by DiscoverClusterNetworks.
func ClusterNetworksFromConfigMap(data map[string]string) ClusterNetworks {
	nets := ClusterNetworks{
		PodCIDR:     DefaultPodCIDR,
		ServiceCIDR: DefaultServiceCIDR,
		JoinCIDR:    DefaultJoinCIDR,
	}
	if v := data[ConfigMapKeyPodCIDR]; v != "" {
		nets.PodCIDR = v
	}
	if v := data[ConfigMapKeyServiceCIDR]; v != "" {
		nets.ServiceCIDR = v
	}
	if v := data[ConfigMapKeyJoinCIDR]; v != "" {
		nets.JoinCIDR = v
	}
	return nets
}

// ClusterNetworks are the cluster-owned networks a tenant's remoteCIDRs must be
// disjoint from. PodCIDR/ServiceCIDR/JoinCIDR come from the cozy-system/cozystack
// ConfigMap (ipv4-pod-cidr / ipv4-svc-cidr / ipv4-join-cidr) with the
// platform-values defaults as fallback; NodeCIDRs and LBPools come from Node / LB
// address discovery. The admission check is handed the same values. Empty string
// fields are skipped. The always-reserved networks
// (link-local 169.254.0.0/16, loopback 127.0.0.0/8, and the 0.0.0.0/0 default
// route) are enforced unconditionally and need not be supplied here.
type ClusterNetworks struct {
	PodCIDR     string
	ServiceCIDR string
	JoinCIDR    string
	NodeCIDRs   []string
	LBPools     []string
}

// Rejection describes one declared value that failed validation: the offending
// value exactly as it was declared, the values field it came from, a machine
// label for the network it collides with (one of the Network* constants, or
// NetworkMalformed when the value does not parse) and the colliding network's
// CIDR (empty when the value is malformed). Reason() is always
// ReasonInvalidRemoteCIDR.
type Rejection struct {
	// Value is the offending declared value, verbatim.
	Value string
	// Field names the values field Value came from: one of the Field* constants.
	// Empty is read as FieldRemoteCIDRs.
	Field string
	// Network is the machine label of the network the value collided with, or
	// NetworkMalformed when Value could not be parsed.
	Network string
	// Collides is the colliding network's CIDR; empty when Network is
	// NetworkMalformed.
	Collides string
}

// Reason returns the stable machine-readable reason for every rejection.
func (Rejection) Reason() string { return ReasonInvalidRemoteCIDR }

// Message returns a human-readable explanation naming the offending value, the
// field it was declared in and the network it collides with, suitable for a
// Forbidden error or a Ready condition message.
func (r Rejection) Message() string {
	field := r.Field
	if field == "" {
		field = FieldRemoteCIDRs
	}
	if r.Network == NetworkMalformed {
		return fmt.Sprintf("%s %q is not a valid address", field, r.Value)
	}
	if r.Network == NetworkUnsupported {
		return fmt.Sprintf("%s %q uses an unsupported address family; only IPv4 is supported", field, r.Value)
	}
	return fmt.Sprintf("%s %q overlaps the cluster %s network %s", field, r.Value, r.Network, r.Collides)
}

// denyNet is one network a remoteCIDR must not overlap, paired with its label.
type denyNet struct {
	label  string
	prefix netip.Prefix
}

// Inputs are the tenant-declared values the deny set judges. Every field that
// programs a route or opens a firewall rule on the gateway belongs here: a guard
// that covers one of several inputs to the same subsystem reads as covering all
// of them. What is deliberately NOT judged is recorded in the security model
// alongside why.
type Inputs struct {
	// RemoteCIDRs are the declared remote networks, as prefixes.
	RemoteCIDRs []string
	// StaticRouteDestinations are staticRoutes[].destination, as prefixes. They
	// reach `protocols static route` directly, and the schema pattern admits
	// every prefix length including /0.
	StaticRouteDestinations []string
	// BGPNeighborAddresses are bgp.neighbors[].address, as bare hosts judged as
	// /32. Besides `protocols bgp neighbor` each one also writes an input-filter
	// accept for TCP 179, so an unjudged neighbour opens the management chain
	// for an address the deny set would have rejected as a remoteCIDR.
	BGPNeighborAddresses []string
}

// Validate returns one Rejection per declared value that is malformed or
// overlaps a cluster network from clusters or one of the always-reserved
// networks. A nil/empty result means every value is safe to program. Overlap
// between two declared remote CIDRs is not a concern — routes are
// namespace-scoped, so cross-tenant remote overlap is allowed; only overlap with
// the cluster networks is rejected. Every offender is reported (validation does
// not stop at the first).
//
// The error return is reserved for a cluster network that does not parse, which
// is an operator-supplied ConfigMap value rather than a tenant one. It fails the
// whole check closed: a malformed value overrides the platform default, so
// dropping it silently would leave the deny set narrower than the default it
// replaced. Apart from that the function is pure and hermetic: no I/O, no globals.
func Validate(in Inputs, clusters ClusterNetworks) ([]Rejection, error) {
	deny, err := buildDenyNetworks(clusters)
	if err != nil {
		return nil, err
	}

	var rejections []Rejection
	rejections = append(rejections, validateField(FieldRemoteCIDRs, in.RemoteCIDRs, false, deny)...)
	rejections = append(rejections, validateField(FieldStaticRoute, in.StaticRouteDestinations, false, deny)...)
	rejections = append(rejections, validateField(FieldBGPNeighbor, in.BGPNeighborAddresses, true, deny)...)
	return rejections, nil
}

// validateField judges one values field. host selects the input shape: a bare
// address judged as a /32 (BGP neighbours) rather than a prefix.
func validateField(field string, values []string, host bool, deny []denyNet) []Rejection {
	var rejections []Rejection
	for _, raw := range values {
		p, err := parseDeclared(raw, host)
		if err != nil {
			rejections = append(rejections, Rejection{Value: raw, Field: field, Network: NetworkMalformed})
			continue
		}
		p = p.Masked()

		// IPv4-only (Phase 1). netip.Prefix.Overlaps is false across address
		// families, so a non-IPv4 prefix would pass every cluster-network check
		// silently; reject it before any overlap test rather than program a route
		// that can never match cluster traffic.
		if !p.Addr().Is4() {
			rejections = append(rejections, Rejection{Value: raw, Field: field, Network: NetworkUnsupported})
			continue
		}

		// A default route (or any /0) blackholes all cluster traffic. Report it
		// as such rather than as an overlap with whichever cluster network is
		// checked first.
		if p.Bits() == 0 {
			rejections = append(rejections, Rejection{
				Value:    raw,
				Field:    field,
				Network:  NetworkDefaultRoute,
				Collides: "0.0.0.0/0",
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
					Value:    raw,
					Field:    field,
					Network:  d.label,
					Collides: d.prefix.String(),
				})
				break
			}
		}
	}
	return rejections
}

// parseDeclared parses one declared value as a prefix. A host value (a BGP
// neighbour address) is a bare address and becomes a single-host prefix, so the
// same overlap test judges it.
func parseDeclared(raw string, host bool) (netip.Prefix, error) {
	if !host {
		return netip.ParsePrefix(raw)
	}
	addr, err := netip.ParseAddr(raw)
	if err != nil {
		return netip.Prefix{}, err
	}
	return netip.PrefixFrom(addr, addr.BitLen()), nil
}

// buildDenyNetworks assembles the ordered list of networks a declared value must
// not overlap: the caller-supplied cluster networks (skipping empty fields, per
// the empty-field-skipped contract) followed by the always-reserved link-local
// and loopback blocks.
//
// An empty field is skipped, but a non-empty one that does not parse is an
// error rather than a skip. The two are not the same: ClusterNetworksFromConfigMap
// falls back to the platform default for an absent key, so a value that is
// present and malformed has already displaced that default, and dropping it here
// would leave the deny set with a hole exactly where an operator typo put it.
func buildDenyNetworks(c ClusterNetworks) ([]denyNet, error) {
	var out []denyNet
	var errs []error
	add := func(label, cidr string) {
		if cidr == "" {
			return
		}
		p, err := netip.ParsePrefix(cidr)
		if err != nil {
			errs = append(errs, fmt.Errorf("cluster %s network %q is not a valid CIDR: %w", label, cidr, err))
			return
		}
		out = append(out, denyNet{label: label, prefix: p.Masked()})
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

	if len(errs) > 0 {
		return nil, errors.Join(errs...)
	}
	return out, nil
}
