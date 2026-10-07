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
	"github.com/prometheus/client_golang/prometheus"
	ctrlmetrics "sigs.k8s.io/controller-runtime/pkg/metrics"
)

var (
	zoneVLANsTotal = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Name: "cozy_proxmox_zone_vlans_total",
		Help: "VLANs the zone may allocate (ranges minus reserved).",
	}, []string{"zone"})
	zoneVLANsAllocated = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Name: "cozy_proxmox_zone_vlans_allocated",
		Help: "VLANs held by networks of the zone.",
	}, []string{"zone"})
	zoneVLANsFree = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Name: "cozy_proxmox_zone_vlans_free",
		Help: "VLANs still available in the zone.",
	}, []string{"zone"})
	zoneNetworks = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Name: "cozy_proxmox_zone_networks",
		Help: "ProxmoxNetworks bound to the zone.",
	}, []string{"zone"})
	orphanedVLANs = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Name: "cozy_proxmox_orphaned_vlans",
		Help: "Kube-OVN Vlans labelled for the zone whose ProxmoxNetwork no longer exists.",
	}, []string{"zone"})

	networkReady = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Name: "cozy_proxmox_network_ready",
		Help: "1 when the ProxmoxNetwork is Ready.",
	}, []string{"namespace", "network", "zone", "vlan"})
	networkPoolAddresses = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Name: "cozy_proxmox_network_ippool_addresses",
		Help: "Addresses of the network's InClusterIPPool by state.",
	}, []string{"namespace", "network", "state"})
	networkConsumers = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Name: "cozy_proxmox_network_consumers",
		Help: "Addresses allocated to VMs on the network.",
	}, []string{"namespace", "network"})
	networkDeleting = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Name: "cozy_proxmox_network_deleting",
		Help: "1 while a deleted network waits for its last consumer.",
	}, []string{"namespace", "network"})

	networkGatewayChassis = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Name: "cozy_proxmox_network_gateway_chassis",
		Help: "HA_Chassis in the OVN HA chassis group of the network's router port, as of the last pass that reached the OVN NB.",
	}, []string{"namespace", "network"})
	networkGatewayReady = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Name: "cozy_proxmox_network_gateway_ready",
		Help: "1 when the network's GatewayReady condition is True; absent while the controller does not manage gateways.",
	}, []string{"namespace", "network"})
	gatewayGroupsCollected = prometheus.NewCounter(prometheus.CounterOpts{
		Name: "cozy_proxmox_gateway_groups_collected_total",
		Help: "OVN HA chassis groups deleted because their ProxmoxNetwork was gone.",
	})
	gatewayNBErrors = prometheus.NewCounter(prometheus.CounterOpts{
		Name: "cozy_proxmox_gateway_nb_errors_total",
		Help: "Gateway passes and releases that could not reach or update the OVN NB.",
	})

	vlanAllocations = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "cozy_proxmox_vlan_allocations_total",
		Help: "VLAN allocation attempts by result.",
	}, []string{"zone", "result"})
	reconcileErrors = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "cozy_proxmox_network_reconcile_errors_total",
		Help: "Reconcile passes that ended in an error or a not-Ready condition, by reason.",
	}, []string{"reason"})
	reconcileDuration = prometheus.NewHistogram(prometheus.HistogramOpts{
		Name:    "cozy_proxmox_network_reconcile_duration_seconds",
		Help:    "Duration of a ProxmoxNetwork reconcile pass.",
		Buckets: prometheus.DefBuckets,
	})
)

func init() {
	ctrlmetrics.Registry.MustRegister(
		zoneVLANsTotal, zoneVLANsAllocated, zoneVLANsFree, zoneNetworks, orphanedVLANs,
		networkReady, networkPoolAddresses, networkConsumers, networkDeleting,
		networkGatewayChassis, networkGatewayReady, gatewayGroupsCollected, gatewayNBErrors,
		vlanAllocations, reconcileErrors, reconcileDuration,
	)
}

// forgetNetwork drops every series of a network that no longer exists, so a
// deleted network does not keep reporting its last values.
func forgetNetwork(namespace, name string) {
	networkReady.DeletePartialMatch(prometheus.Labels{"namespace": namespace, "network": name})
	networkPoolAddresses.DeletePartialMatch(prometheus.Labels{"namespace": namespace, "network": name})
	networkConsumers.DeleteLabelValues(namespace, name)
	networkDeleting.DeleteLabelValues(namespace, name)
	networkGatewayChassis.DeleteLabelValues(namespace, name)
	networkGatewayReady.DeleteLabelValues(namespace, name)
}

func forgetZone(zone string) {
	zoneVLANsTotal.DeleteLabelValues(zone)
	zoneVLANsAllocated.DeleteLabelValues(zone)
	zoneVLANsFree.DeleteLabelValues(zone)
	zoneNetworks.DeleteLabelValues(zone)
	orphanedVLANs.DeleteLabelValues(zone)
}
