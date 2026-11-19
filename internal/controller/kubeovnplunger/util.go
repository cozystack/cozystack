package kubeovnplunger

import "github.com/cozystack/cozystack/pkg/ovnstatus"

func b2f(b bool) float64 {
	if b {
		return 1
	}
	return 0
}

// Pull a cluster UUID (cid) from any snapshots’ Local.CID (falls back to "")
func cidFromSnaps(snaps []ovnstatus.HealthSnapshot) string {
	for _, s := range snaps {
		if s.Local.CID != "" {
			return s.Local.CID
		}
	}
	return ""
}

// Map SID -> last local index to compute gaps (optional)
func leaderIndex(snaps []ovnstatus.HealthSnapshot, leaderSID string) (idx *int64) {
	for _, s := range snaps {
		if s.Local.SID == leaderSID && s.Local.Index > 0 {
			v := s.Local.Index
			return &v
		}
	}
	return nil
}
