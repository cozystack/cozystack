// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Cozystack Authors.

package siterouter

import (
	"encoding/json"
	"fmt"
	"sort"
)

const (
	// routesAnnotation is the kube-ovn annotation carrying per-namespace static
	// routes. The kube-ovn mutating webhook propagates it onto pods at CREATE, so
	// the controller sets it on the tenant namespace and fresh pods inherit the
	// return path to the remote site.
	routesAnnotation = "ovn.kubernetes.io/routes"
	// portSecurityAnnotation is the kube-ovn per-port source/MAC anti-spoof
	// toggle. The controller relaxes it on the gateway pod only.
	portSecurityAnnotation = "ovn.kubernetes.io/port_security"
	// portSecurityRelaxed is the value the controller writes to disable OVN
	// source/MAC filtering on the gateway port (D8); the guest source filter
	// (T08) is the compensating control.
	portSecurityRelaxed = "false"

	// routesFieldOwner is the distinct server-side-apply field manager the
	// controller uses when patching the namespace routes annotation, so it edits
	// only its own annotation without clobbering writers of other namespace
	// annotations (the package_reconciler.reconcileNamespaces idiom).
	routesFieldOwner = "site-router-controller"

	// emptyRoutes is the canonical encoding of a routes annotation with no
	// entries. removeRoutes returns it when the last entry is withdrawn; the
	// caller drops the annotation key entirely rather than leaving "[]" behind.
	emptyRoutes = "[]"
)

// routeEntry is one kube-ovn static route in the ovn.kubernetes.io/routes
// annotation: {"dst": "<remoteCIDR>", "gw": "<gateway-pod-ip>"}.
type routeEntry struct {
	Dst string `json:"dst"`
	Gw  string `json:"gw"`
}

type routeConflictError struct {
	Dst             string
	ExistingGateway string
	DesiredGateway  string
}

func (e *routeConflictError) Error() string {
	return fmt.Sprintf("destination %s is already routed through sibling gateway %s; refusing to replace it with %s", e.Dst, e.ExistingGateway, e.DesiredGateway)
}

// decodeRoutes parses a routes annotation value into entries. An empty string
// (no annotation yet) decodes to no entries, not an error.
func decodeRoutes(existing string) ([]routeEntry, error) {
	if existing == "" {
		return nil, nil
	}
	var entries []routeEntry
	if err := json.Unmarshal([]byte(existing), &entries); err != nil {
		return nil, fmt.Errorf("decode routes annotation %q: %w", existing, err)
	}
	return entries, nil
}

// encodeRoutes renders entries as canonical JSON: sorted by dst so an unchanged
// desired state always produces byte-identical output (a stable no-op guard for
// server-side apply). No entries encodes to emptyRoutes ("[]"), never "null".
func encodeRoutes(entries []routeEntry) (string, error) {
	if len(entries) == 0 {
		return emptyRoutes, nil
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Dst < entries[j].Dst })
	b, err := json.Marshal(entries)
	if err != nil {
		return "", fmt.Errorf("encode routes annotation: %w", err)
	}
	return string(b), nil
}

// mergeRoutes reconciles the existing ovn.kubernetes.io/routes annotation value
// (which may be empty or hold entries programmed by another site-router instance
// sharing the namespace) to this instance's desired state: it upserts a
// {dst: cidr, gw: gatewayIP} entry for each remoteCIDR (keying by dst, so an
// entry for a dst already present has only its gw replaced) AND withdraws any
// entry this instance still owns — gw == gatewayIP or previousGatewayIP — whose
// dst is no longer in remoteCIDRs, so a shrinking remoteCIDRs prunes the routes
// it left behind. Entries owned by a co-tenant gateway (a different gw) are
// always preserved. The current and previously persisted gateway pod IPs are the
// owner keys, which lets a pod-IP change migrate owned routes without claiming a
// sibling's same-destination entry. It returns canonical (deterministically
// ordered) JSON so an unchanged desired state produces an identical string.
func mergeRoutes(existing, gatewayIP, previousGatewayIP string, remoteCIDRs []string) (string, error) {
	entries, err := decodeRoutes(existing)
	if err != nil {
		return "", err
	}

	desired := make(map[string]struct{}, len(remoteCIDRs))
	for _, cidr := range remoteCIDRs {
		desired[cidr] = struct{}{}
	}

	// Keep co-tenant entries and this instance's still-desired entries; drop this
	// instance's entries whose dst dropped out of remoteCIDRs.
	kept := make([]routeEntry, 0, len(entries)+len(remoteCIDRs))
	indexByDst := make(map[string]int, len(entries)+len(remoteCIDRs))
	for _, e := range entries {
		owned := e.Gw == gatewayIP || (previousGatewayIP != "" && e.Gw == previousGatewayIP)
		if owned {
			if _, want := desired[e.Dst]; !want {
				continue
			}
		}
		indexByDst[e.Dst] = len(kept)
		kept = append(kept, e)
	}

	for _, cidr := range remoteCIDRs {
		if i, ok := indexByDst[cidr]; ok {
			owner := kept[i].Gw
			if owner != gatewayIP && (previousGatewayIP == "" || owner != previousGatewayIP) {
				return "", &routeConflictError{Dst: cidr, ExistingGateway: owner, DesiredGateway: gatewayIP}
			}
			kept[i].Gw = gatewayIP
			continue
		}
		indexByDst[cidr] = len(kept)
		kept = append(kept, routeEntry{Dst: cidr, Gw: gatewayIP})
	}
	return encodeRoutes(kept)
}

// removeRoutes withdraws this instance's route entries from the existing
// annotation value, keyed strictly by its known current and persisted gateway
// IPs. When no owner gateway IP is available it preserves the annotation
// byte-for-byte: guessing ownership by destination can delete a sibling
// SiteRouter's live same-destination route. The reconciler persists the last
// programmed gateway IP on the HelmRelease so this conservative no-op is limited
// to legacy/incomplete instances.
func removeRoutes(existing string, gatewayIPs ...string) (string, error) {
	owners := make(map[string]struct{}, len(gatewayIPs))
	for _, gatewayIP := range gatewayIPs {
		if gatewayIP != "" {
			owners[gatewayIP] = struct{}{}
		}
	}
	if len(owners) == 0 {
		return existing, nil
	}
	entries, err := decodeRoutes(existing)
	if err != nil {
		return "", err
	}

	kept := make([]routeEntry, 0, len(entries))
	for _, e := range entries {
		if _, owned := owners[e.Gw]; owned {
			continue
		}
		kept = append(kept, e)
	}
	return encodeRoutes(kept)
}
