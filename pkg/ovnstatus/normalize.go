package ovnstatus

import "strings"

// ----- SID normalization (handles legacy "b007" style SIDs) -----

// NormalizeViews expands truncated SIDs in each MemberView's Members map,
// using IP->fullSID learned from reporters and unique-prefix fallback.
type sidCanon struct{ raw, canon string }

func NormalizeViews(views []MemberView) []MemberView {
	// 1) Learn IP -> fullSID from reporters (self entries)
	ipToFull := map[string]string{}
	fullSIDs := map[string]struct{}{}

	for _, v := range views {
		if v.FromSID != "" {
			fullSIDs[v.FromSID] = struct{}{}
		}
		if v.FromAddress != "" {
			ip := AddrToIP(v.FromAddress)
			if ip != "" && v.FromSID != "" {
				ipToFull[ip] = v.FromSID
			}
		}
	}

	// Build a slice for prefix-matching fallback (hyphenless, lowercase)
	var known []sidCanon
	for fsid := range fullSIDs {
		known = append(known, sidCanon{
			raw:   fsid,
			canon: canonizeSID(fsid),
		})
	}

	// 2) Normalize each view's Members by replacing short SIDs with full SIDs
	out := make([]MemberView, 0, len(views))
	for _, v := range views {
		mv := MemberView{
			FromSID:     normalizeOneSID(v.FromSID, v.FromAddress, ipToFull, known),
			FromAddress: v.FromAddress,
			Members:     make(map[string]string, len(v.Members)),
		}
		for sid, addr := range v.Members {
			full := normalizeOneSIDWithAddr(sid, addr, ipToFull, known)
			// If remapping causes a collision, prefer keeping the address
			// from the entry that matches the full SID (no-op), otherwise last write wins.
			mv.Members[full] = addr
		}
		out = append(out, mv)
	}
	return out
}

func normalizeOneSIDWithAddr(sid, addr string, ipToFull map[string]string, known []sidCanon) string {
	// If it's already full-ish, return as-is
	if looksFullSID(sid) {
		return sid
	}
	// First try IP mapping
	if ip := AddrToIP(addr); ip != "" {
		if fsid, ok := ipToFull[ip]; ok {
			return fsid
		}
	}
	// Fallback: unique prefix match against known full SIDs (hyphens ignored)
	return expandByUniquePrefix(sid, known)
}

func normalizeOneSID(sid, selfAddr string, ipToFull map[string]string, known []sidCanon) string {
	if looksFullSID(sid) {
		return sid
	}
	if ip := AddrToIP(selfAddr); ip != "" {
		if fsid, ok := ipToFull[ip]; ok {
			return fsid
		}
	}
	return expandByUniquePrefix(sid, known)
}

func looksFullSID(s string) bool {
	// Heuristic: a v4 UUID with hyphens is 36 chars.
	// Some builds may print full without hyphens (32). Treat >= 32 hex-ish as "full".
	cs := canonizeSID(s)
	return len(cs) >= 32
}

func canonizeSID(s string) string {
	// lower + drop hyphens for prefix comparisons
	s = strings.ToLower(s)
	return strings.ReplaceAll(s, "-", "")
}

func expandByUniquePrefix(short string, known []sidCanon) string {
	p := canonizeSID(short)
	if p == "" {
		return short
	}
	matches := make([]string, 0, 2)
	for _, k := range known {
		if strings.HasPrefix(k.canon, p) {
			matches = append(matches, k.raw)
			if len(matches) > 1 {
				break
			}
		}
	}
	if len(matches) == 1 {
		return matches[0]
	}
	// ambiguous or none → leave as-is (will still be visible in diagnostics)
	return short
}
