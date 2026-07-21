// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Cozystack Authors.

package siterouter

import (
	"reflect"

	"github.com/prometheus/client_golang/prometheus"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/metrics"

	"github.com/cozystack/cozystack/internal/vyos"
)

// Prometheus label names shared by the SiteRouter metrics. Kept as constants so
// the vec definitions and the test helpers agree on their order and spelling.
const (
	metricNamespaceLabel = "namespace"
	metricInstanceLabel  = "instance"
	metricPeerLabel      = "peer"
)

// SiteRouter controller metrics, registered on the controller-runtime metrics
// registry (served on --metrics-bind-address, scraped by the platform via the
// controller chart's VMPodScrape). They are fed from the runtime-poll
// observations (pollRuntimeState) — the same data T09 projects onto status.
//
// What is emitted and why (the counters that were specced but are NOT emitted are
// documented on purpose):
//   - site_router_tunnel_up      per-peer IPsec SA state (1=Up, 0=Down/Connecting).
//   - site_router_bgp_session_up per-neighbor BGP session state (1=Established,
//     0=otherwise); only ever set when the instance has BGP neighbors.
//   - site_router_config_apply_errors_total per-instance count of failed VyOS
//     /configure pushes.
//
// site_router_tunnel_rekeys_total / _rx_bytes / _tx_bytes are intentionally NOT
// emitted: the ported ParseIPSecSA extracts only per-peer state from
// `show vpn ipsec sa` (its parity contract with cozyportal), and the VyOS
// operational output that command returns carries no byte/packet/rekey columns to
// source them from. Emitting them would mean changing the guest command AND
// widening the parser AND the IPSecObservation struct — speculative work against
// an output shape not represented in any fixture, which the T10 risk note says to
// avoid rather than destabilise the internal/vyos parity tests. See the T10 report.
var (
	tunnelUpGauge = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Name: "site_router_tunnel_up",
		Help: "IPsec site-to-site tunnel state per peer (1 = Up, 0 = Down/Connecting).",
	}, []string{metricNamespaceLabel, metricInstanceLabel, metricPeerLabel})

	bgpSessionUpGauge = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Name: "site_router_bgp_session_up",
		Help: "BGP session state per neighbor (1 = Established, 0 = otherwise). Set only when BGP is enabled.",
	}, []string{metricNamespaceLabel, metricInstanceLabel, metricPeerLabel})

	configApplyErrorsCounter = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "site_router_config_apply_errors_total",
		Help: "Total number of failed VyOS configuration pushes (POST /configure) per instance.",
	}, []string{metricNamespaceLabel, metricInstanceLabel})
)

func init() {
	metrics.Registry.MustRegister(tunnelUpGauge, bgpSessionUpGauge, configApplyErrorsCounter)
}

// metricSnapshot is the comparable projection of an instance's tunnel/BGP
// observations onto the gauge values (peer -> 0/1). updateMetrics compares the
// freshly-derived snapshot against the one it last wrote so it only touches the
// Prometheus vecs when the state actually changed — the bump-on-change discipline
// that keeps the 30s poll from generating scrape churn. Maps are always non-nil
// so reflect.DeepEqual treats an empty set and a nil set alike.
type metricSnapshot struct {
	tunnels map[string]float64
	bgp     map[string]float64
}

func snapshotEqual(a, b metricSnapshot) bool {
	return reflect.DeepEqual(a.tunnels, b.tunnels) && reflect.DeepEqual(a.bgp, b.bgp)
}

// updateMetrics reconciles the per-peer tunnel/BGP gauges from the instance's
// latest observations and reports whether anything changed. It is a no-op (and
// returns false) when the observations are byte-for-byte the state last written,
// so an unchanged poll neither re-sets a gauge nor churns the scrape. On a change
// it sets each observed peer's gauge and deletes the series for peers that have
// disappeared (no label-cardinality leak).
func (r *SiteRouterReconciler) updateMetrics(inst *instance) bool {
	next := metricSnapshot{
		tunnels: make(map[string]float64, len(inst.ipsecObservations)),
		bgp:     make(map[string]float64, len(inst.bgpObservations)),
	}
	for _, o := range inst.ipsecObservations {
		if o.PeerName == "" {
			continue
		}
		next.tunnels[o.PeerName] = boolToFloat(o.State == vyos.IPSecTunnelStateUp)
	}
	for _, o := range inst.bgpObservations {
		if o.PeerAddress == "" {
			continue
		}
		next.bgp[o.PeerAddress] = boolToFloat(o.Session == vyos.BGPSessionStateEstablished)
	}

	key := client.ObjectKeyFromObject(inst.hr)

	r.metricsMu.Lock()
	defer r.metricsMu.Unlock()

	prev, had := r.lastMetricSnapshot[key]
	if had && snapshotEqual(prev, next) {
		return false
	}

	ns, name := inst.namespace, inst.name

	// Retire the series for peers no longer observed before (re)setting the live
	// ones, so a peer that drops out does not linger at its last value.
	if had {
		for peer := range prev.tunnels {
			if _, ok := next.tunnels[peer]; !ok {
				tunnelUpGauge.DeleteLabelValues(ns, name, peer)
			}
		}
		for peer := range prev.bgp {
			if _, ok := next.bgp[peer]; !ok {
				bgpSessionUpGauge.DeleteLabelValues(ns, name, peer)
			}
		}
	}

	for peer, v := range next.tunnels {
		tunnelUpGauge.WithLabelValues(ns, name, peer).Set(v)
	}
	for peer, v := range next.bgp {
		bgpSessionUpGauge.WithLabelValues(ns, name, peer).Set(v)
	}

	if r.lastMetricSnapshot == nil {
		r.lastMetricSnapshot = map[types.NamespacedName]metricSnapshot{}
	}
	r.lastMetricSnapshot[key] = next
	return true
}

// recordConfigApplyError advances the per-instance config-apply error counter. It
// is called from the VyOS /configure failure path, so the counter tracks genuine
// push failures rather than being re-derived from state each poll.
func (r *SiteRouterReconciler) recordConfigApplyError(inst *instance) {
	configApplyErrorsCounter.WithLabelValues(inst.namespace, inst.name).Inc()
}

// forgetMetrics deletes every series this instance owns and drops its cached
// snapshot, so a deleted SiteRouter leaves no lingering metrics behind. Called
// from reconcileDelete alongside forgetAppliedHash.
func (r *SiteRouterReconciler) forgetMetrics(inst *instance) {
	ns, name := inst.namespace, inst.name
	key := client.ObjectKeyFromObject(inst.hr)

	r.metricsMu.Lock()
	defer r.metricsMu.Unlock()

	if prev, had := r.lastMetricSnapshot[key]; had {
		for peer := range prev.tunnels {
			tunnelUpGauge.DeleteLabelValues(ns, name, peer)
		}
		for peer := range prev.bgp {
			bgpSessionUpGauge.DeleteLabelValues(ns, name, peer)
		}
		delete(r.lastMetricSnapshot, key)
	}
	configApplyErrorsCounter.DeleteLabelValues(ns, name)
}

func boolToFloat(b bool) float64 {
	if b {
		return 1
	}
	return 0
}
