package kubeovnplunger

import (
	"time"

	"github.com/cozystack/cozystack/pkg/ovnstatus"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

type metrics struct {
	// --- Core cluster health (per DB/cid) ---
	clusterQuorum           *prometheus.GaugeVec // 1/0
	allAgree                *prometheus.GaugeVec // 1/0
	membersExpected         *prometheus.GaugeVec
	membersObserved         *prometheus.GaugeVec
	ipsExpected             *prometheus.GaugeVec
	ipsObserved             *prometheus.GaugeVec
	excessMembers           *prometheus.GaugeVec
	missingMembers          *prometheus.GaugeVec
	unexpectedIPsCount      *prometheus.GaugeVec
	missingExpectedIPsCount *prometheus.GaugeVec
	ipConflictsCount        *prometheus.GaugeVec
	sidAddrDisagreements    *prometheus.GaugeVec

	// --- Consensus summary (per DB/cid) ---
	consensusMajoritySize *prometheus.GaugeVec
	consensusMinoritySize *prometheus.GaugeVec
	consensusDiffsTotal   *prometheus.GaugeVec

	// --- Detail exports (sparse, keyed by IP/SID) ---
	unexpectedIPGauge      *prometheus.GaugeVec // {db,cid,ip} -> 1
	missingExpectedIPGauge *prometheus.GaugeVec // {db,cid,ip} -> 1
	ipConflictGauge        *prometheus.GaugeVec // {db,cid,ip} -> count(sids)
	suspectStaleGauge      *prometheus.GaugeVec // {db,cid,sid} -> 1

	// --- Per-member liveness/freshness (per DB/cid/sid[/ip]) ---
	memberConnected       *prometheus.GaugeVec // {db,cid,sid,ip}
	memberLeader          *prometheus.GaugeVec // {db,cid,sid}
	memberLastMsgMs       *prometheus.GaugeVec // {db,cid,sid}
	memberIndex           *prometheus.GaugeVec // {db,cid,sid}
	memberIndexGap        *prometheus.GaugeVec // {db,cid,sid}
	memberReporter        *prometheus.GaugeVec // {db,cid,sid}
	memberMissingReporter *prometheus.GaugeVec // {db,cid,sid}

	// --- Ops/housekeeping ---
	leaderTransitionsTotal *prometheus.CounterVec // {db,cid}
	collectErrorsTotal     *prometheus.CounterVec // {db,cid}
	publishEventsTotal     *prometheus.CounterVec // {db,cid}
	snapshotTimestampSec   *prometheus.GaugeVec   // {db,cid}
}

func (r *KubeOVNPlunger) initMetrics() {
	p := promauto.With(r.Registry)

	ns := "ovn"

	// --- Core cluster health ---
	r.metrics.clusterQuorum = p.NewGaugeVec(prometheus.GaugeOpts{
		Namespace: ns, Subsystem: "cluster", Name: "quorum",
		Help: "1 if cluster has quorum, else 0",
	}, []string{"db", "cid"})

	r.metrics.allAgree = p.NewGaugeVec(prometheus.GaugeOpts{
		Namespace: ns, Subsystem: "cluster", Name: "all_agree",
		Help: "1 if all members report identical membership",
	}, []string{"db", "cid"})

	r.metrics.membersExpected = p.NewGaugeVec(prometheus.GaugeOpts{
		Namespace: ns, Subsystem: "cluster", Name: "members_expected",
		Help: "Expected cluster size (replicas)",
	}, []string{"db", "cid"})

	r.metrics.membersObserved = p.NewGaugeVec(prometheus.GaugeOpts{
		Namespace: ns, Subsystem: "cluster", Name: "members_observed",
		Help: "Observed members (distinct SIDs across views)",
	}, []string{"db", "cid"})

	r.metrics.ipsExpected = p.NewGaugeVec(prometheus.GaugeOpts{
		Namespace: ns, Subsystem: "cluster", Name: "ips_expected",
		Help: "Expected distinct member IPs (from k8s hints)",
	}, []string{"db", "cid"})

	r.metrics.ipsObserved = p.NewGaugeVec(prometheus.GaugeOpts{
		Namespace: ns, Subsystem: "cluster", Name: "ips_observed",
		Help: "Observed distinct member IPs (from OVN views)",
	}, []string{"db", "cid"})

	r.metrics.excessMembers = p.NewGaugeVec(prometheus.GaugeOpts{
		Namespace: ns, Subsystem: "cluster", Name: "excess_members",
		Help: "Members over expected (>=0)",
	}, []string{"db", "cid"})

	r.metrics.missingMembers = p.NewGaugeVec(prometheus.GaugeOpts{
		Namespace: ns, Subsystem: "cluster", Name: "missing_members",
		Help: "Members short of expected (>=0)",
	}, []string{"db", "cid"})

	r.metrics.unexpectedIPsCount = p.NewGaugeVec(prometheus.GaugeOpts{
		Namespace: ns, Subsystem: "cluster", Name: "unexpected_ips",
		Help: "Count of IPs in OVN not present in k8s expected set",
	}, []string{"db", "cid"})

	r.metrics.missingExpectedIPsCount = p.NewGaugeVec(prometheus.GaugeOpts{
		Namespace: ns, Subsystem: "cluster", Name: "missing_expected_ips",
		Help: "Count of expected IPs not found in OVN",
	}, []string{"db", "cid"})

	r.metrics.ipConflictsCount = p.NewGaugeVec(prometheus.GaugeOpts{
		Namespace: ns, Subsystem: "cluster", Name: "ip_conflicts",
		Help: "Number of IPs claimed by multiple SIDs",
	}, []string{"db", "cid"})

	r.metrics.sidAddrDisagreements = p.NewGaugeVec(prometheus.GaugeOpts{
		Namespace: ns, Subsystem: "cluster", Name: "sid_address_disagreements",
		Help: "Number of SIDs seen with >1 distinct addresses",
	}, []string{"db", "cid"})

	// --- Consensus summary ---
	r.metrics.consensusMajoritySize = p.NewGaugeVec(prometheus.GaugeOpts{
		Namespace: ns, Subsystem: "consensus", Name: "majority_size",
		Help: "Majority group size (0 if none)",
	}, []string{"db", "cid"})

	r.metrics.consensusMinoritySize = p.NewGaugeVec(prometheus.GaugeOpts{
		Namespace: ns, Subsystem: "consensus", Name: "minority_size",
		Help: "Minority group size",
	}, []string{"db", "cid"})

	r.metrics.consensusDiffsTotal = p.NewGaugeVec(prometheus.GaugeOpts{
		Namespace: ns, Subsystem: "consensus", Name: "diffs_total",
		Help: "Total per-reporter differences vs truth (missing + extra + mismatches)",
	}, []string{"db", "cid"})

	// --- Detail exports (sparse) ---
	r.metrics.unexpectedIPGauge = p.NewGaugeVec(prometheus.GaugeOpts{
		Namespace: ns, Subsystem: "consensus", Name: "unexpected_ip",
		Help: "Unexpected IP present in OVN; value fixed at 1",
	}, []string{"db", "cid", "ip"})

	r.metrics.missingExpectedIPGauge = p.NewGaugeVec(prometheus.GaugeOpts{
		Namespace: ns, Subsystem: "consensus", Name: "missing_expected_ip",
		Help: "Expected IP missing from OVN; value fixed at 1",
	}, []string{"db", "cid", "ip"})

	r.metrics.ipConflictGauge = p.NewGaugeVec(prometheus.GaugeOpts{
		Namespace: ns, Subsystem: "consensus", Name: "ip_conflict",
		Help: "Number of SIDs claiming the same IP for this key",
	}, []string{"db", "cid", "ip"})

	r.metrics.suspectStaleGauge = p.NewGaugeVec(prometheus.GaugeOpts{
		Namespace: ns, Subsystem: "consensus", Name: "suspect_stale",
		Help: "Suspected stale SID candidate for kick; value fixed at 1 (emit only when remediation is warranted)",
	}, []string{"db", "cid", "sid"})

	// --- Per-member liveness/freshness ---
	r.metrics.memberConnected = p.NewGaugeVec(prometheus.GaugeOpts{
		Namespace: ns, Subsystem: "member", Name: "connected",
		Help: "1 if local server reports connected/quorum, else 0",
	}, []string{"db", "cid", "sid", "ip"})

	r.metrics.memberLeader = p.NewGaugeVec(prometheus.GaugeOpts{
		Namespace: ns, Subsystem: "member", Name: "leader",
		Help: "1 if this member is leader, else 0",
	}, []string{"db", "cid", "sid"})

	r.metrics.memberLastMsgMs = p.NewGaugeVec(prometheus.GaugeOpts{
		Namespace: ns, Subsystem: "member", Name: "last_msg_ms",
		Help: "Follower->leader 'last msg' age in ms (legacy heuristic). NaN/omit if unknown",
	}, []string{"db", "cid", "sid"})

	r.metrics.memberIndex = p.NewGaugeVec(prometheus.GaugeOpts{
		Namespace: ns, Subsystem: "member", Name: "index",
		Help: "Local Raft log index",
	}, []string{"db", "cid", "sid"})

	r.metrics.memberIndexGap = p.NewGaugeVec(prometheus.GaugeOpts{
		Namespace: ns, Subsystem: "member", Name: "index_gap",
		Help: "Leader index minus local index (>=0)",
	}, []string{"db", "cid", "sid"})

	r.metrics.memberReporter = p.NewGaugeVec(prometheus.GaugeOpts{
		Namespace: ns, Subsystem: "member", Name: "reporter",
		Help: "1 if a self-view from this SID was collected in the scrape cycle",
	}, []string{"db", "cid", "sid"})

	r.metrics.memberMissingReporter = p.NewGaugeVec(prometheus.GaugeOpts{
		Namespace: ns, Subsystem: "member", Name: "missing_reporter",
		Help: "1 if SID appears in union but produced no self-view",
	}, []string{"db", "cid", "sid"})

	// --- Ops/housekeeping ---
	r.metrics.leaderTransitionsTotal = p.NewCounterVec(prometheus.CounterOpts{
		Namespace: ns, Subsystem: "ops", Name: "leader_transitions_total",
		Help: "Count of observed leader SID changes",
	}, []string{"db", "cid"})

	r.metrics.collectErrorsTotal = p.NewCounterVec(prometheus.CounterOpts{
		Namespace: ns, Subsystem: "ops", Name: "collect_errors_total",
		Help: "Count of errors during health collection/analysis",
	}, []string{"db", "cid"})

	r.metrics.publishEventsTotal = p.NewCounterVec(prometheus.CounterOpts{
		Namespace: ns, Subsystem: "ops", Name: "publish_events_total",
		Help: "Count of SSE publish events (optional)",
	}, []string{"db", "cid"})

	r.metrics.snapshotTimestampSec = p.NewGaugeVec(prometheus.GaugeOpts{
		Namespace: ns, Subsystem: "ops", Name: "snapshot_timestamp_seconds",
		Help: "Unix timestamp of the last successful consensus snapshot",
	}, []string{"db", "cid"})
}

func (r *KubeOVNPlunger) WriteClusterMetrics(db string, snaps []ovnstatus.HealthSnapshot, ecv ovnstatus.ExtendedConsensusResult, expectedReplicas int) {
	cid := cidFromSnaps(snaps)

	// Core cluster health
	r.metrics.clusterQuorum.WithLabelValues(db, cid).Set(b2f(ecv.HasMajority))
	r.metrics.allAgree.WithLabelValues(db, cid).Set(b2f(ecv.AllAgree))
	r.metrics.membersExpected.WithLabelValues(db, cid).Set(float64(expectedReplicas))
	r.metrics.membersObserved.WithLabelValues(db, cid).Set(float64(ecv.MembersCount))
	r.metrics.ipsExpected.WithLabelValues(db, cid).Set(float64(len(ecv.ConsensusResult.TruthView.Members))) // optional; or len(hints.ExpectedIPs)
	r.metrics.ipsObserved.WithLabelValues(db, cid).Set(float64(ecv.DistinctIPCount))
	r.metrics.excessMembers.WithLabelValues(db, cid).Set(float64(ecv.ExpectedExcess))
	r.metrics.missingMembers.WithLabelValues(db, cid).Set(float64(ecv.ExpectedShortfall))
	r.metrics.unexpectedIPsCount.WithLabelValues(db, cid).Set(float64(len(ecv.UnexpectedIPs)))
	r.metrics.missingExpectedIPsCount.WithLabelValues(db, cid).Set(float64(len(ecv.MissingExpectedIPs)))
	r.metrics.ipConflictsCount.WithLabelValues(db, cid).Set(float64(len(ecv.IPConflicts)))

	// Count SIDs with >1 distinct addresses
	disagree := 0
	for _, n := range ecv.SIDAddressDisagreements {
		if n > 1 {
			disagree++
		}
	}
	r.metrics.sidAddrDisagreements.WithLabelValues(db, cid).Set(float64(disagree))

	// Consensus summary
	r.metrics.consensusMajoritySize.WithLabelValues(db, cid).Set(float64(len(ecv.MajorityMembers)))
	r.metrics.consensusMinoritySize.WithLabelValues(db, cid).Set(float64(len(ecv.MinorityMembers)))

	// Sum diffs across reporters (missing + extra + mismatches)
	totalDiffs := 0
	for _, d := range ecv.Diffs {
		totalDiffs += len(d.MissingSIDs) + len(d.ExtraSIDs) + len(d.AddressMismatches)
	}
	r.metrics.consensusDiffsTotal.WithLabelValues(db, cid).Set(float64(totalDiffs))

	// Sparse per-key exports (reset then re-emit for this {db,cid})
	r.metrics.unexpectedIPGauge.DeletePartialMatch(prometheus.Labels{"db": db, "cid": cid})
	for _, ip := range ecv.UnexpectedIPs {
		r.metrics.unexpectedIPGauge.WithLabelValues(db, cid, ip).Set(1)
	}

	r.metrics.missingExpectedIPGauge.DeletePartialMatch(prometheus.Labels{"db": db, "cid": cid})
	for _, ip := range ecv.MissingExpectedIPs {
		r.metrics.missingExpectedIPGauge.WithLabelValues(db, cid, ip).Set(1)
	}

	r.metrics.ipConflictGauge.DeletePartialMatch(prometheus.Labels{"db": db, "cid": cid})
	for ip, sids := range ecv.IPConflicts {
		r.metrics.ipConflictGauge.WithLabelValues(db, cid, ip).Set(float64(len(sids)))
	}

	// Only emit suspects when remediation is warranted (e.g., TooManyMembers / unexpected IPs / conflicts)
	r.metrics.suspectStaleGauge.DeletePartialMatch(prometheus.Labels{"db": db, "cid": cid})
	if ecv.TooManyMembers || len(ecv.UnexpectedIPs) > 0 || len(ecv.IPConflicts) > 0 {
		for _, sid := range ecv.SuspectStaleSIDs {
			r.metrics.suspectStaleGauge.WithLabelValues(db, cid, sid).Set(1)
		}
	}

	// Snapshot timestamp
	r.metrics.snapshotTimestampSec.WithLabelValues(db, cid).Set(float64(time.Now().Unix()))
}

func (r *KubeOVNPlunger) WriteMemberMetrics(db string, snaps []ovnstatus.HealthSnapshot, views []ovnstatus.MemberView, ecv ovnstatus.ExtendedConsensusResult) {
	cid := cidFromSnaps(snaps)

	// Figure out current leader SID (prefer local view from any leader snapshot)
	curLeader := ""
	for _, s := range snaps {
		if s.Local.Leader {
			curLeader = s.Local.SID
			break
		}
	}
	// Leader transitions
	key := db + "|" + cid
	if prev, ok := r.lastLeader[key]; ok && prev != "" && curLeader != "" && prev != curLeader {
		r.metrics.leaderTransitionsTotal.WithLabelValues(db, cid).Inc()
	}
	if curLeader != "" {
		r.lastLeader[key] = curLeader
	}

	// Build quick maps for reporter set & IP per SID (best-effort)
	reporter := map[string]struct{}{}
	for _, v := range views {
		if v.FromSID != "" {
			reporter[v.FromSID] = struct{}{}
		}
	}
	sidToIP := map[string]string{}
	for _, v := range views {
		for sid, addr := range v.Members {
			if sidToIP[sid] == "" && addr != "" {
				sidToIP[sid] = ovnstatus.AddrToIP(addr) // expose addrToIP or wrap here
			}
		}
	}

	// Reset member vectors for this {db,cid} (avoid stale series)
	r.metrics.memberConnected.DeletePartialMatch(prometheus.Labels{"db": db, "cid": cid})
	r.metrics.memberLeader.DeletePartialMatch(prometheus.Labels{"db": db, "cid": cid})
	r.metrics.memberLastMsgMs.DeletePartialMatch(prometheus.Labels{"db": db, "cid": cid})
	r.metrics.memberIndex.DeletePartialMatch(prometheus.Labels{"db": db, "cid": cid})
	r.metrics.memberIndexGap.DeletePartialMatch(prometheus.Labels{"db": db, "cid": cid})
	r.metrics.memberReporter.DeletePartialMatch(prometheus.Labels{"db": db, "cid": cid})
	r.metrics.memberMissingReporter.DeletePartialMatch(prometheus.Labels{"db": db, "cid": cid})

	// Leader index (to compute gaps)
	lIdx := leaderIndex(snaps, curLeader)

	// Emit one series per snapshot (self view)
	for _, s := range snaps {
		sid := s.Local.SID
		ip := sidToIP[sid]
		if ip == "" {
			ip = "unknown"
		}

		r.metrics.memberConnected.WithLabelValues(db, cid, sid, ip).Set(b2f(s.Local.Connected))
		r.metrics.memberLeader.WithLabelValues(db, cid, sid).Set(b2f(s.Local.Leader))
		r.metrics.memberIndex.WithLabelValues(db, cid, sid).Set(float64(s.Local.Index))

		if lIdx != nil && s.Local.Index >= 0 {
			gap := *lIdx - s.Local.Index
			if gap < 0 {
				gap = 0
			}
			r.metrics.memberIndexGap.WithLabelValues(db, cid, sid).Set(float64(gap))
		}

		// Reporter presence
		_, isReporter := reporter[sid]
		r.metrics.memberReporter.WithLabelValues(db, cid, sid).Set(b2f(isReporter))
	}

	// “Missing reporter” SIDs = union − reporters (from ecv)
	reporterSet := map[string]struct{}{}
	for sid := range reporter {
		reporterSet[sid] = struct{}{}
	}
	unionSet := map[string]struct{}{}
	for _, sid := range ecv.UnionMembers {
		unionSet[sid] = struct{}{}
	}
	for sid := range unionSet {
		if _, ok := reporterSet[sid]; !ok {
			r.metrics.memberMissingReporter.WithLabelValues(db, cid, sid).Set(1)
		}
	}

	// Legacy follower freshness (if you kept LastMsgMs in servers parsing)
	// We only know LastMsgMs from the Full.Servers in each snapshot; pick the freshest per SID.
	lastMsg := map[string]int64{}
	for _, s := range snaps {
		for _, srv := range s.Full.Servers {
			if srv.LastMsgMs != nil {
				cur, ok := lastMsg[srv.SID]
				if !ok || *srv.LastMsgMs < cur {
					lastMsg[srv.SID] = *srv.LastMsgMs
				}
			}
		}
	}
	for sid, ms := range lastMsg {
		r.metrics.memberLastMsgMs.WithLabelValues(db, cid, sid).Set(float64(ms))
	}
}

func (r *KubeOVNPlunger) deleteAllFor(db, cid string) {
	// Cluster-level vecs (db,cid)
	r.metrics.clusterQuorum.DeletePartialMatch(prometheus.Labels{"db": db, "cid": cid})
	r.metrics.allAgree.DeletePartialMatch(prometheus.Labels{"db": db, "cid": cid})
	r.metrics.membersExpected.DeletePartialMatch(prometheus.Labels{"db": db, "cid": cid})
	r.metrics.membersObserved.DeletePartialMatch(prometheus.Labels{"db": db, "cid": cid})
	r.metrics.ipsExpected.DeletePartialMatch(prometheus.Labels{"db": db, "cid": cid})
	r.metrics.ipsObserved.DeletePartialMatch(prometheus.Labels{"db": db, "cid": cid})
	r.metrics.excessMembers.DeletePartialMatch(prometheus.Labels{"db": db, "cid": cid})
	r.metrics.missingMembers.DeletePartialMatch(prometheus.Labels{"db": db, "cid": cid})
	r.metrics.unexpectedIPsCount.DeletePartialMatch(prometheus.Labels{"db": db, "cid": cid})
	r.metrics.missingExpectedIPsCount.DeletePartialMatch(prometheus.Labels{"db": db, "cid": cid})
	r.metrics.ipConflictsCount.DeletePartialMatch(prometheus.Labels{"db": db, "cid": cid})
	r.metrics.sidAddrDisagreements.DeletePartialMatch(prometheus.Labels{"db": db, "cid": cid})

	r.metrics.consensusMajoritySize.DeletePartialMatch(prometheus.Labels{"db": db, "cid": cid})
	r.metrics.consensusMinoritySize.DeletePartialMatch(prometheus.Labels{"db": db, "cid": cid})
	r.metrics.consensusDiffsTotal.DeletePartialMatch(prometheus.Labels{"db": db, "cid": cid})

	// Sparse detail vecs (db,cid,*)
	r.metrics.unexpectedIPGauge.DeletePartialMatch(prometheus.Labels{"db": db, "cid": cid})
	r.metrics.missingExpectedIPGauge.DeletePartialMatch(prometheus.Labels{"db": db, "cid": cid})
	r.metrics.ipConflictGauge.DeletePartialMatch(prometheus.Labels{"db": db, "cid": cid})
	r.metrics.suspectStaleGauge.DeletePartialMatch(prometheus.Labels{"db": db, "cid": cid})

	// Per-member vecs (db,cid,*)
	r.metrics.memberConnected.DeletePartialMatch(prometheus.Labels{"db": db, "cid": cid})
	r.metrics.memberLeader.DeletePartialMatch(prometheus.Labels{"db": db, "cid": cid})
	r.metrics.memberLastMsgMs.DeletePartialMatch(prometheus.Labels{"db": db, "cid": cid})
	r.metrics.memberIndex.DeletePartialMatch(prometheus.Labels{"db": db, "cid": cid})
	r.metrics.memberIndexGap.DeletePartialMatch(prometheus.Labels{"db": db, "cid": cid})
	r.metrics.memberReporter.DeletePartialMatch(prometheus.Labels{"db": db, "cid": cid})
	r.metrics.memberMissingReporter.DeletePartialMatch(prometheus.Labels{"db": db, "cid": cid})

	// Ops vecs (db,cid)
	r.metrics.leaderTransitionsTotal.DeletePartialMatch(prometheus.Labels{"db": db, "cid": cid})
	r.metrics.collectErrorsTotal.DeletePartialMatch(prometheus.Labels{"db": db, "cid": cid})
	r.metrics.publishEventsTotal.DeletePartialMatch(prometheus.Labels{"db": db, "cid": cid})
	r.metrics.snapshotTimestampSec.DeletePartialMatch(prometheus.Labels{"db": db, "cid": cid})
}
