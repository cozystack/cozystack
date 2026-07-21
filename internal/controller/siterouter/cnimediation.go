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

// mergeRoutes upserts a route entry {dst: cidr, gw: gatewayIP} for each remoteCIDR
// into the existing ovn.kubernetes.io/routes annotation value (which may be empty
// or hold entries programmed by another site-router instance sharing the
// namespace), keying by dst so an entry for a dst already present has only its gw
// replaced and unrelated entries are preserved. It returns the canonical
// (deterministically ordered) JSON so an unchanged desired state produces an
// identical string.
func mergeRoutes(existing, gatewayIP string, remoteCIDRs []string) (string, error) {
	entries, err := decodeRoutes(existing)
	if err != nil {
		return "", err
	}

	indexByDst := make(map[string]int, len(entries))
	for i := range entries {
		indexByDst[entries[i].Dst] = i
	}
	for _, cidr := range remoteCIDRs {
		if i, ok := indexByDst[cidr]; ok {
			entries[i].Gw = gatewayIP
			continue
		}
		entries = append(entries, routeEntry{Dst: cidr, Gw: gatewayIP})
		indexByDst[cidr] = len(entries) - 1
	}
	return encodeRoutes(entries)
}

// removeRoutes deletes only the entries whose dst is one of remoteCIDRs from the
// existing annotation value (the entries this instance programmed), preserving
// every other entry, and returns the remaining canonical JSON (emptyRoutes when
// nothing is left). It is the delete path that lets a finalizer withdraw an
// instance's routes without disturbing a co-tenant instance's.
func removeRoutes(existing string, remoteCIDRs []string) (string, error) {
	entries, err := decodeRoutes(existing)
	if err != nil {
		return "", err
	}

	drop := make(map[string]struct{}, len(remoteCIDRs))
	for _, cidr := range remoteCIDRs {
		drop[cidr] = struct{}{}
	}
	kept := make([]routeEntry, 0, len(entries))
	for _, e := range entries {
		if _, ok := drop[e.Dst]; ok {
			continue
		}
		kept = append(kept, e)
	}
	return encodeRoutes(kept)
}
