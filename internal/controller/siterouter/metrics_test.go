// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Cozystack Authors.

package siterouter

import (
	"encoding/json"
	"errors"
	"testing"

	"github.com/prometheus/client_golang/prometheus/testutil"
	"sigs.k8s.io/controller-runtime/pkg/metrics"

	"github.com/cozystack/cozystack/internal/vyos"
)

// tunnelUpValue reads the site_router_tunnel_up gauge for one (namespace,
// instance, peer) triple via testutil. WithLabelValues yields a single-series
// collector, so ToFloat64 returns exactly that child's value.
func tunnelUpValue(t *testing.T, namespace, instance, peer string) float64 {
	t.Helper()
	return testutil.ToFloat64(tunnelUpGauge.WithLabelValues(namespace, instance, peer))
}

// gaugeSeriesFor gathers the named gauge family from the controller-runtime
// registry and returns the peer->value map for one (namespace, instance). Unlike
// testutil.ToFloat64 it does NOT materialise a series, so an absent peer is
// genuinely absent from the result — which is how the stale-series deletion is
// asserted.
func gaugeSeriesFor(t *testing.T, name, namespace, instance string) map[string]float64 {
	t.Helper()
	mfs, err := metrics.Registry.Gather()
	if err != nil {
		t.Fatalf("gather metrics: %v", err)
	}
	out := map[string]float64{}
	for _, mf := range mfs {
		if mf.GetName() != name {
			continue
		}
		for _, m := range mf.GetMetric() {
			labels := map[string]string{}
			for _, lp := range m.GetLabel() {
				labels[lp.GetName()] = lp.GetValue()
			}
			if labels[metricNamespaceLabel] == namespace && labels[metricInstanceLabel] == instance {
				out[labels[metricPeerLabel]] = m.GetGauge().GetValue()
			}
		}
	}
	return out
}

// TestUpdateMetrics_TunnelUpReflectsState encodes the T10 acceptance "with a
// tunnel up, site_router_tunnel_up reports 1 and drops to 0 when the peer is
// unreachable": an Up observation sets the gauge to 1, a Down observation to 0.
func TestUpdateMetrics_TunnelUpReflectsState(t *testing.T) {
	const inst = "metrics-tun"
	r := newTestReconciler(t)

	i := &instance{
		hr:        siteRouterHR(inst),
		name:      inst,
		namespace: "tenant-test",
		ipsecObservations: []vyos.IPSecObservation{
			{PeerName: "aws-prod", PeerAddress: "203.0.113.10", State: vyos.IPSecTunnelStateUp},
		},
	}
	if !r.updateMetrics(i) {
		t.Fatalf("first updateMetrics should report a change")
	}
	if got := tunnelUpValue(t, "tenant-test", inst, "aws-prod"); got != 1 {
		t.Errorf("tunnel Up should set site_router_tunnel_up=1, got %v", got)
	}

	// The peer becomes unreachable: the tunnel drops to Down.
	i.ipsecObservations = []vyos.IPSecObservation{
		{PeerName: "aws-prod", PeerAddress: "203.0.113.10", State: vyos.IPSecTunnelStateDown},
	}
	if !r.updateMetrics(i) {
		t.Fatalf("a state change (Up->Down) should report a change")
	}
	if got := tunnelUpValue(t, "tenant-test", inst, "aws-prod"); got != 0 {
		t.Errorf("tunnel Down should set site_router_tunnel_up=0, got %v", got)
	}
}

// TestUpdateMetrics_BGPSessionUp verifies the BGP gauge tracks the session FSM:
// Established -> 1, anything else -> 0. Emitted only when BGP observations are
// present (i.e. BGP is enabled on the instance).
func TestUpdateMetrics_BGPSessionUp(t *testing.T) {
	const inst = "metrics-bgp"
	r := newTestReconciler(t)

	i := &instance{
		hr:        siteRouterHR(inst),
		name:      inst,
		namespace: "tenant-test",
		bgpObservations: []vyos.BGPObservation{
			{PeerAddress: "203.0.113.1", Session: vyos.BGPSessionStateEstablished},
			{PeerAddress: "203.0.113.2", Session: vyos.BGPSessionStateIdle},
		},
	}
	if !r.updateMetrics(i) {
		t.Fatalf("first updateMetrics should report a change")
	}
	up := testutil.ToFloat64(bgpSessionUpGauge.WithLabelValues("tenant-test", inst, "203.0.113.1"))
	if up != 1 {
		t.Errorf("Established BGP session should set site_router_bgp_session_up=1, got %v", up)
	}
	down := testutil.ToFloat64(bgpSessionUpGauge.WithLabelValues("tenant-test", inst, "203.0.113.2"))
	if down != 0 {
		t.Errorf("Idle BGP session should set site_router_bgp_session_up=0, got %v", down)
	}
}

// TestUpdateMetrics_NoChurnWhenUnchanged encodes the T10 risk "do not emit metric
// churn on every poll when state is unchanged (bump on change)": a second call
// with identical observations must report no change and touch nothing.
func TestUpdateMetrics_NoChurnWhenUnchanged(t *testing.T) {
	const inst = "metrics-churn"
	r := newTestReconciler(t)

	obs := []vyos.IPSecObservation{
		{PeerName: "peerA", State: vyos.IPSecTunnelStateUp},
	}
	i := &instance{hr: siteRouterHR(inst), name: inst, namespace: "tenant-test", ipsecObservations: obs}

	if !r.updateMetrics(i) {
		t.Fatalf("first updateMetrics should report a change")
	}
	// Same observations again: the 30s poll re-reads identical state.
	if r.updateMetrics(i) {
		t.Errorf("updateMetrics must report NO change when observations are unchanged (no scrape churn)")
	}
	// Value is still correct after the no-op.
	if got := tunnelUpValue(t, "tenant-test", inst, "peerA"); got != 1 {
		t.Errorf("gauge value should be preserved across a no-op update, got %v", got)
	}
}

// TestUpdateMetrics_DropsStalePeerSeries verifies that when a peer disappears from
// the observations the controller deletes its gauge series (no label-cardinality
// leak), rather than leaving a stale value pinned forever.
func TestUpdateMetrics_DropsStalePeerSeries(t *testing.T) {
	const inst = "metrics-stale"
	r := newTestReconciler(t)

	i := &instance{
		hr:        siteRouterHR(inst),
		name:      inst,
		namespace: "tenant-test",
		ipsecObservations: []vyos.IPSecObservation{
			{PeerName: "keep", State: vyos.IPSecTunnelStateUp},
			{PeerName: "drop", State: vyos.IPSecTunnelStateUp},
		},
	}
	r.updateMetrics(i)
	if series := gaugeSeriesFor(t, "site_router_tunnel_up", "tenant-test", inst); len(series) != 2 {
		t.Fatalf("expected 2 tunnel series initially, got %d: %v", len(series), series)
	}

	// "drop" is no longer reported by the guest.
	i.ipsecObservations = []vyos.IPSecObservation{
		{PeerName: "keep", State: vyos.IPSecTunnelStateUp},
	}
	if !r.updateMetrics(i) {
		t.Fatalf("removing a peer should report a change")
	}
	series := gaugeSeriesFor(t, "site_router_tunnel_up", "tenant-test", inst)
	if _, ok := series["drop"]; ok {
		t.Errorf("stale peer series must be deleted, still present: %v", series)
	}
	if _, ok := series["keep"]; !ok {
		t.Errorf("live peer series must be retained, series: %v", series)
	}
}

// TestUpdateMetrics_ForgetOnDelete verifies forgetMetrics clears an instance's
// series and cached snapshot so a deleted instance leaves no lingering metrics.
func TestUpdateMetrics_ForgetOnDelete(t *testing.T) {
	const inst = "metrics-forget"
	r := newTestReconciler(t)

	i := &instance{
		hr:        siteRouterHR(inst),
		name:      inst,
		namespace: "tenant-test",
		ipsecObservations: []vyos.IPSecObservation{
			{PeerName: "peerX", State: vyos.IPSecTunnelStateUp},
		},
	}
	r.updateMetrics(i)
	r.forgetMetrics(i)

	if series := gaugeSeriesFor(t, "site_router_tunnel_up", "tenant-test", inst); len(series) != 0 {
		t.Errorf("forgetMetrics should delete all series for the instance, got %v", series)
	}
}

// TestReconcile_PollErrorPreservesMetrics encodes the R7 fix: a transient
// runtime-poll failure (ShowVPNIPSecSA errors) must NOT erase the tunnel/BGP
// gauges. Each reconcile builds a fresh instance whose observation slices start
// empty; feeding those to updateMetrics on a failed poll would look like every
// peer disappeared and delete all series. The reconcile now skips updateMetrics
// when the poll errored, so the prior snapshot survives until the next good poll.
func TestReconcile_PollErrorPreservesMetrics(t *testing.T) {
	const inst = "metrics-pollerr"
	fakeV := &fakeVyOS{
		retrieveResult:    json.RawMessage(`{"rule":{"5":{"action":"accept"}}}`),
		ipsecObservations: []vyos.IPSecObservation{{PeerName: "aws-prod", State: vyos.IPSecTunnelStateUp}},
	}
	r, _ := newVyOSReconciler(t, fakeV, readyObjects(t, inst, routedValues(), "10.244.0.5")...)

	// 1) A good poll sets the tunnel gauge to 1.
	reconcileInstance(t, r, inst)
	if got := tunnelUpValue(t, "tenant-test", inst, "aws-prod"); got != 1 {
		t.Fatalf("expected site_router_tunnel_up=1 after a good poll, got %v", got)
	}

	// 2) The next poll fails transiently: the gauge must be preserved, not deleted.
	fakeV.setIPSecErr(errors.New("vyos query timeout"))
	reconcileInstance(t, r, inst)

	series := gaugeSeriesFor(t, "site_router_tunnel_up", "tenant-test", inst)
	if _, ok := series["aws-prod"]; !ok {
		t.Errorf("a transient poll failure must not erase the tunnel gauge, series: %v", series)
	}
	if series["aws-prod"] != 1 {
		t.Errorf("expected the prior gauge value (1) preserved across a poll failure, got %v", series["aws-prod"])
	}
}

// TestReconcile_ConfigApplyErrorsCounter encodes the T10 acceptance
// "config_apply_errors_total increments on a Configure error": a failing POST
// /configure advances the counter for the instance, and a second failure advances
// it again. Exercised end-to-end through Reconcile so the vyospush wiring is
// covered, using a unique instance name so the package-global counter is isolated.
func TestReconcile_ConfigApplyErrorsCounter(t *testing.T) {
	const inst = "metrics-cfgerr"
	fakeV := &fakeVyOS{configureErr: errors.New("vyos api timeout")}
	r, _ := newVyOSReconciler(t, fakeV, readyObjects(t, inst, routedValues(), "10.244.0.5")...)

	before := testutil.ToFloat64(configApplyErrorsCounter.WithLabelValues("tenant-test", inst))
	if before != 0 {
		t.Fatalf("counter should start at 0 for a fresh instance, got %v", before)
	}

	reconcileInstance(t, r, inst)
	if got := testutil.ToFloat64(configApplyErrorsCounter.WithLabelValues("tenant-test", inst)); got != 1 {
		t.Errorf("a Configure failure should increment site_router_config_apply_errors_total to 1, got %v", got)
	}

	reconcileInstance(t, r, inst)
	if got := testutil.ToFloat64(configApplyErrorsCounter.WithLabelValues("tenant-test", inst)); got != 2 {
		t.Errorf("a second Configure failure should increment the counter to 2, got %v", got)
	}
}
