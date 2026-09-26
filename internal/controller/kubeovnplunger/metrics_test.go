package kubeovnplunger

import (
	"strings"
	"testing"

	"github.com/cozystack/cozystack/pkg/ovnstatus"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
)

func writeTwoMemberCluster(r *KubeOVNPlunger, db, cid, leaderSID, followerSID, leaderIP, followerIP string) {
	snaps := []ovnstatus.HealthSnapshot{
		{Local: ovnstatus.ServerLocalView{Leader: true, Connected: true, CID: cid, SID: leaderSID, Index: 100}},
		{Local: ovnstatus.ServerLocalView{Connected: true, CID: cid, SID: followerSID, Index: 97}},
	}
	members := map[string]string{
		leaderSID:   "ssl:" + leaderIP + ":6643",
		followerSID: "ssl:" + followerIP + ":6643",
	}
	views := []ovnstatus.MemberView{
		{FromSID: leaderSID, Members: members},
		{FromSID: followerSID, Members: members},
	}
	ecv := ovnstatus.ExtendedConsensusResult{
		ConsensusResult: ovnstatus.ConsensusResult{AllAgree: true, HasMajority: true},
		UnionMembers:    []string{leaderSID, followerSID},
		MembersCount:    2,
		DistinctIPCount: 2,
		UnexpectedIPs:   []string{"10.0.0.9"},
	}
	r.WriteClusterMetrics(db, snaps, ecv, 3)
	r.WriteMemberMetrics(db, snaps, views, ecv)
}

// TestMetrics_DeleteAllForDropsOnlyThatCluster pins the series that
// WriteClusterMetrics and WriteMemberMetrics export, and that
// deleteAllFor removes every series of one {db,cid} while leaving the
// other cluster's series in place.
func TestMetrics_DeleteAllForDropsOnlyThatCluster(t *testing.T) {
	reg := prometheus.NewRegistry()
	r := &KubeOVNPlunger{Registry: reg, lastLeader: map[string]string{}}
	r.initMetrics()

	writeTwoMemberCluster(r, "nb", "cid-stale", "sid-a1", "sid-a2", "10.0.0.1", "10.0.0.2")
	writeTwoMemberCluster(r, "nb", "cid-live", "sid-b1", "sid-b2", "10.0.1.1", "10.0.1.2")

	names := []string{
		"ovn_cluster_quorum",
		"ovn_cluster_members_expected",
		"ovn_consensus_unexpected_ip",
		"ovn_member_connected",
		"ovn_member_index_gap",
	}
	before := `
# HELP ovn_cluster_quorum 1 if cluster has quorum, else 0
# TYPE ovn_cluster_quorum gauge
ovn_cluster_quorum{cid="cid-live",db="nb"} 1
ovn_cluster_quorum{cid="cid-stale",db="nb"} 1
# HELP ovn_cluster_members_expected Expected cluster size (replicas)
# TYPE ovn_cluster_members_expected gauge
ovn_cluster_members_expected{cid="cid-live",db="nb"} 3
ovn_cluster_members_expected{cid="cid-stale",db="nb"} 3
# HELP ovn_consensus_unexpected_ip Unexpected IP present in OVN; value fixed at 1
# TYPE ovn_consensus_unexpected_ip gauge
ovn_consensus_unexpected_ip{cid="cid-live",db="nb",ip="10.0.0.9"} 1
ovn_consensus_unexpected_ip{cid="cid-stale",db="nb",ip="10.0.0.9"} 1
# HELP ovn_member_connected 1 if local server reports connected/quorum, else 0
# TYPE ovn_member_connected gauge
ovn_member_connected{cid="cid-live",db="nb",ip="10.0.1.1",sid="sid-b1"} 1
ovn_member_connected{cid="cid-live",db="nb",ip="10.0.1.2",sid="sid-b2"} 1
ovn_member_connected{cid="cid-stale",db="nb",ip="10.0.0.1",sid="sid-a1"} 1
ovn_member_connected{cid="cid-stale",db="nb",ip="10.0.0.2",sid="sid-a2"} 1
# HELP ovn_member_index_gap Leader index minus local index (>=0)
# TYPE ovn_member_index_gap gauge
ovn_member_index_gap{cid="cid-live",db="nb",sid="sid-b1"} 0
ovn_member_index_gap{cid="cid-live",db="nb",sid="sid-b2"} 3
ovn_member_index_gap{cid="cid-stale",db="nb",sid="sid-a1"} 0
ovn_member_index_gap{cid="cid-stale",db="nb",sid="sid-a2"} 3
`
	if err := testutil.GatherAndCompare(reg, strings.NewReader(before), names...); err != nil {
		t.Fatalf("metrics after write:\n%v", err)
	}

	r.deleteAllFor("nb", "cid-stale")

	after := `
# HELP ovn_cluster_quorum 1 if cluster has quorum, else 0
# TYPE ovn_cluster_quorum gauge
ovn_cluster_quorum{cid="cid-live",db="nb"} 1
# HELP ovn_cluster_members_expected Expected cluster size (replicas)
# TYPE ovn_cluster_members_expected gauge
ovn_cluster_members_expected{cid="cid-live",db="nb"} 3
# HELP ovn_consensus_unexpected_ip Unexpected IP present in OVN; value fixed at 1
# TYPE ovn_consensus_unexpected_ip gauge
ovn_consensus_unexpected_ip{cid="cid-live",db="nb",ip="10.0.0.9"} 1
# HELP ovn_member_connected 1 if local server reports connected/quorum, else 0
# TYPE ovn_member_connected gauge
ovn_member_connected{cid="cid-live",db="nb",ip="10.0.1.1",sid="sid-b1"} 1
ovn_member_connected{cid="cid-live",db="nb",ip="10.0.1.2",sid="sid-b2"} 1
# HELP ovn_member_index_gap Leader index minus local index (>=0)
# TYPE ovn_member_index_gap gauge
ovn_member_index_gap{cid="cid-live",db="nb",sid="sid-b1"} 0
ovn_member_index_gap{cid="cid-live",db="nb",sid="sid-b2"} 3
`
	if err := testutil.GatherAndCompare(reg, strings.NewReader(after), names...); err != nil {
		t.Fatalf("metrics after deleteAllFor(nb, cid-stale):\n%v", err)
	}
	if got := testutil.CollectAndCount(r.metrics.snapshotTimestampSec); got != 1 {
		t.Errorf("snapshot timestamp series after delete = %d, want 1", got)
	}
}
