// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Cozystack Authors.

package denyset

import (
	"context"
	"fmt"
	"net/netip"
	"sort"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// DiscoverClusterNetworks resolves the complete deny-set input shared by
// SiteRouter admission and reconciliation. The ConfigMap supplies the stable
// pod/service/join CIDRs; live Nodes and Services supply the concrete node and
// allocated LoadBalancer addresses that cannot be represented by that ConfigMap.
//
// Individual addresses are expressed as IPv4 /32 prefixes. Validate uses prefix
// overlap rather than equality, so a tenant-declared subnet that contains even
// one live node or allocated service address is rejected as well. IPv6 addresses
// are ignored because SiteRouter Phase 1 accepts IPv4 remoteCIDRs only.
func DiscoverClusterNetworks(ctx context.Context, reader client.Reader, configMapKey types.NamespacedName) (ClusterNetworks, error) {
	cm := &corev1.ConfigMap{}
	err := reader.Get(ctx, configMapKey, cm)
	if err != nil && !apierrors.IsNotFound(err) {
		return ClusterNetworks{}, fmt.Errorf("get %s ConfigMap: %w", configMapKey, err)
	}

	var data map[string]string
	if err == nil {
		data = cm.Data
	}
	nets := ClusterNetworksFromConfigMap(data)

	nodes := &corev1.NodeList{}
	if err := reader.List(ctx, nodes); err != nil {
		return ClusterNetworks{}, fmt.Errorf("list Nodes for SiteRouter deny-set: %w", err)
	}
	for i := range nodes.Items {
		for _, address := range nodes.Items[i].Status.Addresses {
			if address.Type != corev1.NodeInternalIP && address.Type != corev1.NodeExternalIP {
				continue
			}
			nets.NodeCIDRs = appendIPv4HostPrefix(nets.NodeCIDRs, address.Address)
		}
	}

	services := &corev1.ServiceList{}
	if err := reader.List(ctx, services); err != nil {
		return ClusterNetworks{}, fmt.Errorf("list Services for SiteRouter deny-set: %w", err)
	}
	for i := range services.Items {
		svc := &services.Items[i]
		for _, address := range svc.Spec.ExternalIPs {
			nets.LBPools = appendIPv4HostPrefix(nets.LBPools, address)
		}
		if svc.Spec.Type != corev1.ServiceTypeLoadBalancer {
			continue
		}
		nets.LBPools = appendIPv4HostPrefix(nets.LBPools, svc.Spec.LoadBalancerIP)
		for _, ingress := range svc.Status.LoadBalancer.Ingress {
			nets.LBPools = appendIPv4HostPrefix(nets.LBPools, ingress.IP)
		}
	}

	nets.NodeCIDRs = sortedUnique(nets.NodeCIDRs)
	nets.LBPools = sortedUnique(nets.LBPools)
	return nets, nil
}

func appendIPv4HostPrefix(dst []string, raw string) []string {
	addr, err := netip.ParseAddr(raw)
	if err != nil || !addr.Is4() {
		return dst
	}
	return append(dst, netip.PrefixFrom(addr, addr.BitLen()).String())
}

func sortedUnique(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	out := make([]string, 0, len(values))
	for _, value := range values {
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}
	sort.Strings(out)
	return out
}
