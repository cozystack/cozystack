package ovnstatus

import (
	"fmt"
	"sort"
	"strings"
)

// ---- Public API ------------------------------------------------------------

// MemberView is a normalized membership view (from one member's perspective).
type MemberView struct {
	FromSID     string            // the reporter's SID (hs.Local.SID)
	FromAddress string            // best-effort: address of self from Servers (if present)
	Members     map[string]string // SID -> Address (as reported by this member)
}

// ViewDiff is the difference between one view and a chosen "truth" view.
type ViewDiff struct {
	MissingSIDs       []string             // SIDs absent in this view but present in truth
	ExtraSIDs         []string             // SIDs present in this view but absent in truth
	AddressMismatches map[string][2]string // SID -> [truthAddr, thisAddr] when both have SID but addresses differ
}

// ConsensusResult summarizes cluster agreement across views.
type ConsensusResult struct {
	AllAgree        bool                // true if all views are identical
	HasMajority     bool                // true if some view is held by >= quorum
	QuorumSize      int                 // floor(n/2)+1
	MajorityKey     string              // canonical key of the majority view (if any)
	MajorityMembers []string            // SIDs of reporters in the majority
	MinorityMembers []string            // SIDs of reporters not in the majority
	TruthView       MemberView          // the majority's canonical view (if HasMajority); empty otherwise
	Diffs           map[string]ViewDiff // per-reporter diffs vs TruthView (only meaningful if HasMajority)
}

// BuildMemberView extracts a normalized view for one snapshot.
// It uses hs.Full.Servers as the authoritative list this reporter sees.
func BuildMemberView(hs HealthSnapshot) MemberView {
	mv := MemberView{
		FromSID: hs.Local.SID,
		Members: make(map[string]string, len(hs.Full.Servers)),
	}

	// Fill Members map and try to capture self address.
	for _, s := range hs.Full.Servers {
		if s.SID == "" || s.Address == "" {
			continue
		}
		mv.Members[s.SID] = s.Address
		if s.Self {
			mv.FromAddress = s.Address
		}
	}
	return mv
}

// AnalyzeConsensus checks agreement across a slice of views for one cluster.
// It answers:
//  1. do all views agree exactly?
//  2. if not, is there a majority agreement?
//  3. who’s in the minority, and how does each minority view differ?
func AnalyzeConsensus(views []MemberView) ConsensusResult {
	n := len(views)
	cr := ConsensusResult{
		QuorumSize: (n / 2) + 1,
		Diffs:      make(map[string]ViewDiff, n),
	}
	if n == 0 {
		return cr
	}

	// Fingerprint each view's Members map; group reporters by fingerprint key.
	type group struct {
		key   string
		views []MemberView
	}
	groupsByKey := map[string]*group{}

	for _, v := range views {
		key := fingerprintMembers(v.Members)
		g, ok := groupsByKey[key]
		if !ok {
			g = &group{key: key}
			groupsByKey[key] = g
		}
		g.views = append(g.views, v)
	}

	// If only one unique fingerprint → everyone agrees.
	if len(groupsByKey) == 1 {
		for _, g := range groupsByKey {
			cr.AllAgree = true
			cr.HasMajority = true
			cr.MajorityKey = g.key
			cr.TruthView = g.views[0] // any member in this group shares the same map
			for _, v := range g.views {
				cr.MajorityMembers = append(cr.MajorityMembers, v.FromSID)
				cr.Diffs[v.FromSID] = ViewDiff{} // empty
			}
			return cr
		}
	}

	// Pick the largest group as a candidate majority.
	var maxG *group
	for _, g := range groupsByKey {
		if maxG == nil || len(g.views) > len(maxG.views) {
			maxG = g
		}
	}
	if maxG != nil && len(maxG.views) >= cr.QuorumSize {
		cr.HasMajority = true
		cr.MajorityKey = maxG.key
		cr.TruthView = maxG.views[0] // canonical truth view
		for _, v := range maxG.views {
			cr.MajorityMembers = append(cr.MajorityMembers, v.FromSID)
			cr.Diffs[v.FromSID] = ViewDiff{} // empty
		}
		// Minority: everyone not in the majority group
		majoritySet := map[string]struct{}{}
		for _, v := range maxG.views {
			majoritySet[v.FromSID] = struct{}{}
		}
		for _, v := range views {
			if _, ok := majoritySet[v.FromSID]; !ok {
				cr.MinorityMembers = append(cr.MinorityMembers, v.FromSID)
				cr.Diffs[v.FromSID] = diffViews(cr.TruthView.Members, v.Members)
			}
		}
		return cr
	}

	// No majority -> pick the largest group as "reference" for diffs (optional).
	// We'll still fill Diffs vs that reference to aid debugging.
	if maxG != nil {
		cr.TruthView = maxG.views[0]
		for _, v := range views {
			cr.Diffs[v.FromSID] = diffViews(cr.TruthView.Members, v.Members)
		}
		// Populate members lists (no majority)
		for _, v := range maxG.views {
			cr.MajorityMembers = append(cr.MajorityMembers, v.FromSID)
		}
		for _, v := range views {
			found := false
			for _, m := range cr.MajorityMembers {
				if m == v.FromSID {
					found = true
					break
				}
			}
			if !found {
				cr.MinorityMembers = append(cr.MinorityMembers, v.FromSID)
			}
		}
	}
	return cr
}

// ---- Internals -------------------------------------------------------------

func fingerprintMembers(m map[string]string) string {
	// Produce a stable "SID=Addr" joined string.
	if len(m) == 0 {
		return ""
	}
	keys := make([]string, 0, len(m))
	for sid := range m {
		keys = append(keys, sid)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, sid := range keys {
		parts = append(parts, sid+"="+m[sid])
	}
	return strings.Join(parts, "|")
}

func diffViews(truth, other map[string]string) ViewDiff {
	var d ViewDiff
	d.AddressMismatches = make(map[string][2]string)

	// Build sets
	truthKeys := make([]string, 0, len(truth))
	otherKeys := make([]string, 0, len(other))
	for k := range truth {
		truthKeys = append(truthKeys, k)
	}
	for k := range other {
		otherKeys = append(otherKeys, k)
	}
	sort.Strings(truthKeys)
	sort.Strings(otherKeys)

	// Missing & mismatches
	for _, sid := range truthKeys {
		tAddr := truth[sid]
		oAddr, ok := other[sid]
		if !ok {
			d.MissingSIDs = append(d.MissingSIDs, sid)
			continue
		}
		if tAddr != oAddr {
			d.AddressMismatches[sid] = [2]string{tAddr, oAddr}
		}
	}
	// Extra
	for _, sid := range otherKeys {
		if _, ok := truth[sid]; !ok {
			d.ExtraSIDs = append(d.ExtraSIDs, sid)
		}
	}
	return d
}

// ---- Pretty helpers (optional) --------------------------------------------

func (cr ConsensusResult) String() string {
	var b strings.Builder
	fmt.Fprintf(&b, "AllAgree=%v, HasMajority=%v (quorum=%d)\n", cr.AllAgree, cr.HasMajority, cr.QuorumSize)
	if cr.HasMajority {
		fmt.Fprintf(&b, "MajorityMembers: %v\n", cr.MajorityMembers)
		if len(cr.MinorityMembers) > 0 {
			fmt.Fprintf(&b, "MinorityMembers: %v\n", cr.MinorityMembers)
		}
	}
	for sid, d := range cr.Diffs {
		if len(d.MissingSIDs) == 0 && len(d.ExtraSIDs) == 0 && len(d.AddressMismatches) == 0 {
			continue
		}
		fmt.Fprintf(&b, "- %s diffs:\n", sid)
		if len(d.MissingSIDs) > 0 {
			fmt.Fprintf(&b, "  missing: %v\n", d.MissingSIDs)
		}
		if len(d.ExtraSIDs) > 0 {
			fmt.Fprintf(&b, "  extra:   %v\n", d.ExtraSIDs)
		}
		if len(d.AddressMismatches) > 0 {
			fmt.Fprintf(&b, "  addr mismatches:\n")
			for k, v := range d.AddressMismatches {
				fmt.Fprintf(&b, "    %s: truth=%s this=%s\n", k, v[0], v[1])
			}
		}
	}
	return b.String()
}

// Hints about the cluster from outside OVN (e.g., Kubernetes).
type Hints struct {
	// ExpectedReplicas, if >0, is the intended cluster size; if 0 and ExpectedIPs provided,
	// we derive ExpectedReplicas = len(ExpectedIPs).
	ExpectedReplicas int

	// ExpectedIPs is the set of node IPs you expect to participate (unique per member).
	// Optional label can be a pod/node name for reporting (empty string is fine).
	ExpectedIPs map[string]string // ip -> label
}

// ExtendedConsensusResult augments ConsensusResult with IP-centric signals.
type ExtendedConsensusResult struct {
	ConsensusResult

	// Union across all views (what anyone reported).
	UnionMembers []string // SIDs (sorted)
	UnionIPs     []string // IPs  (sorted)

	// Reporters (SIDs that produced a HealthSnapshot / self-view).
	Reporters []string // SIDs (sorted)

	// Members that appear in UnionMembers but for which we have no reporter snapshot.
	MissingReporters []string // SIDs (sorted)

	// IPs seen in union but NOT in hints.ExpectedIPs (if provided).
	UnexpectedIPs []string // sorted

	// Expected IPs that did NOT appear anywhere in union.
	MissingExpectedIPs []string // sorted

	// Size checks; MembersCount is distinct SIDs; DistinctIPCount is distinct IPs.
	MembersCount      int
	DistinctIPCount   int
	TooManyMembers    bool // MembersCount > ExpectedReplicas
	TooFewMembers     bool // MembersCount < ExpectedReplicas
	ExpectedShortfall int  // ExpectedReplicas - MembersCount (>=0)
	ExpectedExcess    int  // MembersCount - ExpectedReplicas (>=0)

	// IPConflicts: an IP mapped to multiple SIDs (shouldn’t happen if identity is clean).
	IPConflicts map[string][]string // ip -> []sids

	// SIDAddressDisagreements: number of distinct addresses observed for a SID.
	SIDAddressDisagreements map[string]int // sid -> count(address variants)

	// Suspect stale SIDs: candidates to kick (heuristic, IP-focused).
	// Ranked by: (1) IP not expected, (2) not self-reporting, (3) lowest reference count.
	SuspectStaleSIDs []string // sorted by suspicion
}

// AddrToIP extracts the host/IP from strings like:
//
//	"tcp:10.0.0.1:6641", "ssl:[192.168.100.12]:6643", "tcp:[fe80::1]:6641"
func AddrToIP(addr string) string {
	a := strings.TrimSpace(addr)
	// Strip scheme prefix
	if i := strings.Index(a, ":"); i != -1 && (strings.HasPrefix(a, "tcp:") || strings.HasPrefix(a, "ssl:")) {
		a = a[i+1:]
	}
	// If bracketed IPv6: [fe80::1]:6641
	if strings.HasPrefix(a, "[") {
		if j := strings.Index(a, "]"); j != -1 {
			return a[1:j]
		}
	}
	// IPv4 or unbracketed IPv6 with :port → split last colon safely
	if i := strings.LastIndex(a, ":"); i != -1 {
		return a[:i]
	}
	return a // fallback
}

func setKeys(m map[string]struct{}) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
func setDiff(a, b map[string]struct{}) []string {
	out := []string{}
	for k := range a {
		if _, ok := b[k]; !ok {
			out = append(out, k)
		}
	}
	sort.Strings(out)
	return out
}

// AnalyzeConsensusWithIPHints extends AnalyzeConsensus using ExpectedIPs instead of ExpectedSIDs.
func AnalyzeConsensusWithIPHints(views []MemberView, hints *Hints) ExtendedConsensusResult {
	base := AnalyzeConsensus(views) // keeps majority/minority, per-view diffs (SID->addr)

	// Build unions and stats
	unionSID := map[string]struct{}{}
	unionIP := map[string]struct{}{}
	reporterSID := map[string]struct{}{}
	refCountSID := map[string]int{}                     // how many times a SID is referenced across all views
	addrVariantsSID := map[string]map[string]struct{}{} // SID -> set(address strings)
	ipToSIDs := map[string]map[string]struct{}{}        // ip -> set(SID)

	for _, v := range views {
		if v.FromSID != "" {
			reporterSID[v.FromSID] = struct{}{}
		}
		for sid, addr := range v.Members {
			if sid == "" || addr == "" {
				continue
			}
			unionSID[sid] = struct{}{}
			refCountSID[sid]++
			// address canon
			if _, ok := addrVariantsSID[sid]; !ok {
				addrVariantsSID[sid] = map[string]struct{}{}
			}
			addrVariantsSID[sid][addr] = struct{}{}
			// IP canon
			ip := AddrToIP(addr)
			if ip != "" {
				unionIP[ip] = struct{}{}
				if _, ok := ipToSIDs[ip]; !ok {
					ipToSIDs[ip] = map[string]struct{}{}
				}
				ipToSIDs[ip][sid] = struct{}{}
			}
		}
	}

	// Prepare hint set for IPs
	var expectedIPsSet map[string]struct{}
	expectedReplicas := 0
	if hints != nil {
		if len(hints.ExpectedIPs) > 0 {
			expectedIPsSet = make(map[string]struct{}, len(hints.ExpectedIPs))
			for ip := range hints.ExpectedIPs {
				expectedIPsSet[ip] = struct{}{}
			}
			expectedReplicas = len(hints.ExpectedIPs)
		}
		if hints.ExpectedReplicas > 0 {
			expectedReplicas = hints.ExpectedReplicas
		}
	}

	unionSIDs := setKeys(unionSID)
	unionIPs := setKeys(unionIP)
	reporters := setKeys(reporterSID)
	missingReporters := setDiff(unionSID, reporterSID) // SIDs seen but no self-view

	// IP-based unexpected / missing vs hints
	var unexpectedIPs, missingExpectedIPs []string
	if expectedIPsSet != nil {
		unexpectedIPs = setDiff(unionIP, expectedIPsSet)
		missingExpectedIPs = setDiff(expectedIPsSet, unionIP)
	}

	// Size checks (by SIDs)
	membersCount := len(unionSID)
	distinctIPCount := len(unionIP)
	tooMany, tooFew := false, false
	shortfall, excess := 0, 0
	if expectedReplicas > 0 {
		if membersCount > expectedReplicas {
			tooMany = true
			excess = membersCount - expectedReplicas
		} else if membersCount < expectedReplicas {
			tooFew = true
			shortfall = expectedReplicas - membersCount
		}
	}

	// IP conflicts: same IP claimed under multiple SIDs
	ipConflicts := map[string][]string{}
	for ip, sids := range ipToSIDs {
		if len(sids) > 1 {
			ipConflicts[ip] = setKeys(sids)
		}
	}

	// SID address disagreements: how many distinct addresses per SID
	sidAddrDisagree := map[string]int{}
	for sid, addrs := range addrVariantsSID {
		sidAddrDisagree[sid] = len(addrs)
	}

	// --- Suspect stale SIDs -------------------------------------------------
	//
	// Only produce suspects when there is evidence of staleness:
	// - too many members (over expected replicas), or
	// - unexpected IPs exist, or
	// - IP conflicts exist.
	// Then rank by (unexpected IP) > (not self-reporting) > (low reference count)
	// and trim to the number we actually need to remove (ExpectedExcess).
	produceSuspects := tooMany || len(unexpectedIPs) > 0 || len(ipConflicts) > 0

	suspectList := []string{}
	if produceSuspects {
		suspectScore := map[string]int{}
		for sid := range unionSID {
			score := 0

			// Representative IP for this SID (pick lexicographically smallest addr -> ip)
			var sidIP string
			if av := addrVariantsSID[sid]; len(av) > 0 {
				addrs := setKeys(av)
				sort.Strings(addrs)
				sidIP = AddrToIP(addrs[0])
			}

			// Strongest signal: IP not expected
			if expectedIPsSet != nil && sidIP != "" {
				if _, ok := expectedIPsSet[sidIP]; !ok {
					score += 1000
				}
			}
			// Not self-reporting is suspicious (but not fatal by itself)
			if _, ok := reporterSID[sid]; !ok {
				score += 100
			}
			// Fewer references → a bit more suspicious
			score += 10 - min(refCountSID[sid], 10)

			suspectScore[sid] = score
		}

		suspectList = make([]string, 0, len(suspectScore))
		for sid := range suspectScore {
			suspectList = append(suspectList, sid)
		}
		sort.Slice(suspectList, func(i, j int) bool {
			if suspectScore[suspectList[i]] != suspectScore[suspectList[j]] {
				return suspectScore[suspectList[i]] > suspectScore[suspectList[j]]
			}
			return suspectList[i] < suspectList[j]
		})

		// Trim to just what we need to remediate if we’re over capacity.
		if tooMany && excess > 0 && len(suspectList) > excess {
			suspectList = suspectList[:excess]
		}
	}

	return ExtendedConsensusResult{
		ConsensusResult:         base,
		UnionMembers:            unionSIDs,
		UnionIPs:                unionIPs,
		Reporters:               reporters,
		MissingReporters:        missingReporters,
		UnexpectedIPs:           unexpectedIPs,
		MissingExpectedIPs:      missingExpectedIPs,
		MembersCount:            membersCount,
		DistinctIPCount:         distinctIPCount,
		TooManyMembers:          tooMany,
		TooFewMembers:           tooFew,
		ExpectedShortfall:       shortfall,
		ExpectedExcess:          excess,
		IPConflicts:             ipConflicts,
		SIDAddressDisagreements: sidAddrDisagree,
		SuspectStaleSIDs:        suspectList,
	}
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// PrettyString renders a human-friendly multi-line summary of ExtendedConsensusResult.
// It combines consensus status with IP/SID hints.
func (res ExtendedConsensusResult) PrettyString() string {
	var b strings.Builder

	fmt.Fprintf(&b, "Consensus summary:\n")
	fmt.Fprintf(&b, "  AllAgree: %v\n", res.AllAgree)
	fmt.Fprintf(&b, "  HasMajority: %v (quorum=%d)\n", res.HasMajority, res.QuorumSize)
	fmt.Fprintf(&b, "  MembersCount: %d (distinct IPs=%d)\n", res.MembersCount, res.DistinctIPCount)

	if res.TooManyMembers {
		fmt.Fprintf(&b, "  ⚠ Too many members: expected %d, found %d (excess=%d)\n",
			res.MembersCount-res.ExpectedExcess, res.MembersCount, res.ExpectedExcess)
	}
	if res.TooFewMembers {
		fmt.Fprintf(&b, "  ⚠ Too few members: expected %d, found %d (shortfall=%d)\n",
			res.MembersCount+res.ExpectedShortfall, res.MembersCount, res.ExpectedShortfall)
	}

	if len(res.MajorityMembers) > 0 {
		fmt.Fprintf(&b, "  MajorityMembers (SIDs): %v\n", res.MajorityMembers)
	}
	if len(res.MinorityMembers) > 0 {
		fmt.Fprintf(&b, "  MinorityMembers (SIDs): %v\n", res.MinorityMembers)
	}

	if len(res.UnionIPs) > 0 {
		fmt.Fprintf(&b, "  Union IPs: %v\n", res.UnionIPs)
	}
	if len(res.Reporters) > 0 {
		fmt.Fprintf(&b, "  Reporters (self-SIDs): %v\n", res.Reporters)
	}
	if len(res.MissingReporters) > 0 {
		fmt.Fprintf(&b, "  ⚠ MissingReporters (no self-view): %v\n", res.MissingReporters)
	}

	if len(res.UnexpectedIPs) > 0 {
		fmt.Fprintf(&b, "  ⚠ UnexpectedIPs: %v\n", res.UnexpectedIPs)
	}
	if len(res.MissingExpectedIPs) > 0 {
		fmt.Fprintf(&b, "  ⚠ MissingExpectedIPs: %v\n", res.MissingExpectedIPs)
	}

	if len(res.IPConflicts) > 0 {
		fmt.Fprintf(&b, "  ⚠ IP conflicts:\n")
		for ip, sids := range res.IPConflicts {
			fmt.Fprintf(&b, "    %s claimed by %v\n", ip, sids)
		}
	}

	if len(res.SIDAddressDisagreements) > 0 {
		fmt.Fprintf(&b, "  SID address disagreements:\n")
		for sid, n := range res.SIDAddressDisagreements {
			if n > 1 {
				fmt.Fprintf(&b, "    %s has %d distinct addresses\n", sid, n)
			}
		}
	}

	if len(res.SuspectStaleSIDs) > 0 {
		fmt.Fprintf(&b, "  ⚠ SuspectStaleSIDs (ranked): %v\n", res.SuspectStaleSIDs)
	}

	// Per-reporter diffs vs truth
	if len(res.Diffs) > 0 && res.HasMajority {
		fmt.Fprintf(&b, "  Diffs vs truth view:\n")
		for sid, d := range res.Diffs {
			if len(d.MissingSIDs) == 0 && len(d.ExtraSIDs) == 0 && len(d.AddressMismatches) == 0 {
				continue
			}
			fmt.Fprintf(&b, "    %s:\n", sid)
			if len(d.MissingSIDs) > 0 {
				fmt.Fprintf(&b, "      missing SIDs: %v\n", d.MissingSIDs)
			}
			if len(d.ExtraSIDs) > 0 {
				fmt.Fprintf(&b, "      extra SIDs:   %v\n", d.ExtraSIDs)
			}
			for k, v := range d.AddressMismatches {
				fmt.Fprintf(&b, "      addr mismatch for %s: truth=%s this=%s\n", k, v[0], v[1])
			}
		}
	}

	return b.String()
}
