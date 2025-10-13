// Package ovnstatus provides an OVNClient that returns structured NB/SB health.
// It prefers JSON outputs and falls back to minimal text parsing for "Servers".
package ovnstatus

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

/************** Public API **************/

// DB is the logical DB name in ovsdb-server.
type DB string

const (
	DBNorthbound DB = "OVN_Northbound"
	DBSouthbound DB = "OVN_Southbound"
)

// RunnerFunc allows dependency-injecting the command runner.
type RunnerFunc func(ctx context.Context, bin string, args ...string) (string, error)

// OVNClient holds config + runner and exposes health methods.
type OVNClient struct {
	// Paths to local control sockets.
	NBCTLPath string // e.g., /var/run/ovn/ovnnb_db.ctl
	SBCTLPath string // e.g., /var/run/ovn/ovnsb_db.ctl
	NBDBSock  string // tcp:127.0.0.1:6641, unix:/var/run/ovn/ovnnb_db.sock, etc
	SBDBSock  string // tcp:127.0.0.1:6642, unix:/var/run/ovn/ovnsb_db.sock, etc

	// TLS for ovsdb-client (used for _Server queries). ovn-appctl uses ctl socket, no TLS needed.
	UseSSL bool
	Key    string
	Cert   string
	CACert string

	FreshLastMsgThreshold time.Duration
	// Optional expected replica count for stale-member checks.
	ExpectedReplicas int

	// Runner is the pluggable command runner. If nil, a default runner is used.
	Runner RunnerFunc
}

func (o *OVNClient) ApplyDefaults() {
	if o.NBCTLPath == "" {
		o.NBCTLPath = "/var/run/ovn/ovnnb_db.ctl"
	}
	if o.SBCTLPath == "" {
		o.SBCTLPath = "/var/run/ovn/ovnsb_db.ctl"
	}
	if o.NBDBSock == "" {
		o.NBDBSock = "unix:/var/run/ovn/ovnnb_db.sock"
	}
	if o.SBDBSock == "" {
		o.SBDBSock = "unix:/var/run/ovn/ovnsb_db.sock"
	}
	if o.ExpectedReplicas == 0 {
		o.ExpectedReplicas = 3
	}
	if o.FreshLastMsgThreshold == 0 {
		o.FreshLastMsgThreshold = 10 * time.Second
	}
}

// ServerLocalView is what the local ovsdb-server reports via _Server.Database.
type ServerLocalView struct {
	Leader    bool   `json:"leader"`
	Connected bool   `json:"connected"`
	CID       string `json:"cid"` // cluster UUID
	SID       string `json:"sid"` // this server UUID
	Index     int64  `json:"index"`
}

// ClusterStatus is a structured view of cluster/status.
type ClusterStatus struct {
	Name      string          `json:"name,omitempty"`
	Role      string          `json:"role,omitempty"` // leader/follower (local)
	Term      int64           `json:"term,omitempty"`
	Index     int64           `json:"index,omitempty"`
	Connected bool            `json:"connected,omitempty"`
	Servers   []ClusterServer `json:"servers,omitempty"`
}

// ClusterServer is an entry in the Servers list.
type ClusterServer struct {
	SID        string `json:"sid,omitempty"`
	Address    string `json:"address,omitempty"`
	Role       string `json:"role,omitempty"`
	Self       bool   `json:"self,omitempty"`
	Connected  bool   `json:"connected,omitempty"`
	LastMsgMs  *int64 `json:"lastMsgMs,omitempty"`
	NextIndex  *int64 `json:"nextIndex,omitempty"`  // NEW
	MatchIndex *int64 `json:"matchIndex,omitempty"` // NEW
}

// HealthSnapshot bundles both sources for easy checks.
type HealthSnapshot struct {
	DB    DB
	Local ServerLocalView
	Full  ClusterStatus
}

// StaleMemberCount returns how many configured servers exceed the expected replica count.
func (hs HealthSnapshot) StaleMemberCount(expectedReplicas int) int {
	n := len(hs.Full.Servers)
	if n <= expectedReplicas {
		return 0
	}
	return n - expectedReplicas
}

// HasQuorum returns whether the local server believes it has a majority.
func (hs HealthSnapshot) HasQuorum() bool { return hs.Local.Connected }

// IsLeader reports local leadership (per-DB).
func (hs HealthSnapshot) IsLeader() bool { return hs.Local.Leader }

// HealthNB returns a health snapshot for OVN_Northbound.
func (c *OVNClient) HealthNB(ctx context.Context) (HealthSnapshot, error) {
	return c.health(ctx, DBNorthbound, c.NBCTLPath)
}

// HealthSB returns a health snapshot for OVN_Southbound.
func (c *OVNClient) HealthSB(ctx context.Context) (HealthSnapshot, error) {
	return c.health(ctx, DBSouthbound, c.SBCTLPath)
}

// HealthBoth returns snapshots for both NB and SB.
func (c *OVNClient) HealthBoth(ctx context.Context) (nb HealthSnapshot, sb HealthSnapshot, err1, err2 error) {
	nb, err1 = c.HealthNB(ctx)
	sb, err2 = c.HealthSB(ctx)
	return nb, sb, err1, err2
}

/************** Implementation **************/

func (c *OVNClient) health(ctx context.Context, db DB, ctlPath string) (HealthSnapshot, error) {
	if ctlPath == "" {
		return HealthSnapshot{}, fmt.Errorf("missing ctlPath for %s", db)
	}
	local, err := c.getLocalServerView(ctx, db)
	if err != nil {
		return HealthSnapshot{}, err
	}
	full, err := c.getClusterStatus(ctx, db, ctlPath)
	if err != nil {
		// Return at least the local view.
		return HealthSnapshot{DB: db, Local: local}, err
	}
	// Optional cosmetic: sort Servers for stable output (self first, then by SID).
	/*
		sort.SliceStable(full.Servers, func(i, j int) bool {
			if full.Servers[i].Self != full.Servers[j].Self {
				return full.Servers[i].Self
			}
			return full.Servers[i].SID < full.Servers[j].SID
		})
	*/
	return HealthSnapshot{DB: db, Local: local, Full: full}, nil
}

type ovsdbQueryResp struct {
	Rows []struct {
		Leader    bool     `json:"leader"`
		Connected bool     `json:"connected"`
		CID       []string `json:"cid"`
		SID       []string `json:"sid"`
		Index     int64    `json:"index"`
	} `json:"rows"`
}

func (c *OVNClient) getLocalServerView(ctx context.Context, db DB) (ServerLocalView, error) {
	addr := ""
	switch db {
	case DBNorthbound:
		addr = c.NBDBSock
	case DBSouthbound:
		addr = c.SBDBSock
	default:
		return ServerLocalView{}, fmt.Errorf("unexpected value %s for ovn db, expected values %s, %s", db, DBNorthbound, DBSouthbound)
	}

	query := fmt.Sprintf(
		`["_Server",{"op":"select","table":"Database","where":[["name","==","%s"]],"columns":["leader","connected","cid","sid","index"]}]`,
		db,
	)

	args := []string{"query", addr, query}
	if c.UseSSL {
		args = []string{
			"-p", c.Key, "-c", c.Cert, "-C", c.CACert,
			"query", addr, query,
		}
	}

	out, err := c.run(ctx, "ovsdb-client", args...)
	if err != nil {
		return ServerLocalView{}, fmt.Errorf("ovsdb-client query failed: %w (out: %s)", err, out)
	}

	var resp []ovsdbQueryResp
	if err := json.Unmarshal([]byte(out), &resp); err != nil {
		return ServerLocalView{}, fmt.Errorf("parse _Server.Database JSON: %w", err)
	}
	if len(resp) == 0 || len(resp[0].Rows) == 0 {
		return ServerLocalView{}, errors.New("empty _Server.Database response")
	}
	row := resp[0].Rows[0]
	uuidOf := func(arr []string) (string, bool) {
		if len(arr) == 2 && arr[0] == "uuid" && arr[1] != "" {
			return arr[1], true
		}
		return "", false
	}
	cid, okCID := uuidOf(row.CID)
	sid, okSID := uuidOf(row.SID)
	if !okCID || !okSID {
		return ServerLocalView{}, fmt.Errorf("unexpected _Server.Database uuid encoding: cid=%v sid=%v", row.CID, row.SID)
	}
	return ServerLocalView{
		Leader:    row.Leader,
		Connected: row.Connected,
		CID:       cid,
		SID:       sid,
		Index:     row.Index,
	}, nil
}

func (c *OVNClient) getClusterStatus(ctx context.Context, db DB, ctlPath string) (ClusterStatus, error) {
	out, err := c.run(ctx, "ovn-appctl", "-t", ctlPath, "cluster/status", string(db))
	if err != nil {
		return ClusterStatus{}, fmt.Errorf("cluster/status failed: %w (out: %s)", err, out)
	}
	return parseServersFromTextWithThreshold(out, c.FreshLastMsgThreshold), nil
}

func (c *OVNClient) run(ctx context.Context, bin string, args ...string) (string, error) {
	runner := c.Runner
	if runner == nil {
		runner = defaultRunner
	}
	return runner(ctx, bin, args...)
}

/************** Default runner **************/

func defaultRunner(ctx context.Context, bin string, args ...string) (string, error) {
	// Reasonable default timeout; caller can supply a context with its own deadline.
	if _, ok := ctx.Deadline(); !ok {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, 5*time.Second)
		defer cancel()
	}
	cmd := exec.CommandContext(ctx, bin, args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	out := strings.TrimSpace(stdout.String())
	if err != nil {
		if out == "" {
			out = strings.TrimSpace(stderr.String())
		}
		return out, err
	}
	return out, nil
}

/************** Helpers **************/

func parseClusterStatusJSON(out string) (ClusterStatus, bool) {
	var cs ClusterStatus
	if json.Unmarshal([]byte(out), &cs) == nil && len(cs.Servers) > 0 {
		return cs, true
	}
	var wrap struct {
		Data ClusterStatus `json:"data"`
	}
	if json.Unmarshal([]byte(out), &wrap) == nil && len(wrap.Data.Servers) > 0 {
		return wrap.Data, true
	}
	return ClusterStatus{}, false
}

func portOf(db DB) string {
	switch db {
	case DBNorthbound:
		return "6641"
	case DBSouthbound:
		return "6642"
	default:
		return "0"
	}
}

/************** Minimal text fallback for "Servers" **************/

// Accepts variants like:
//
//	Servers:
//	  77f0 (self) at tcp:10.0.0.1:6641 (leader)
//	  9a3b at tcp:10.0.0.2:6641 (follower)
//	  1c2d at ssl:10.0.0.3:6641 (backup)
//	  4e5f at tcp:10.0.0.4:6641 (disconnected)
var (
	reServersHeader = regexp.MustCompile(`(?m)^\s*Servers:\s*$`)
	reServerModern  = regexp.MustCompile(`^\s*([0-9a-fA-F-]+)\s*(\((?:self)\))?\s*at\s*([^\s]+)\s*\(([^)]+)\)`)
	reServerLegacy  = regexp.MustCompile(
		`^\s*` +
			`([0-9a-fA-F-]+)\s*` + // 1: primary SID
			`\(\s*([0-9a-fA-F-]+)\s+at\s+([^)]+)\)\s*` + // 2: inner SID, 3: address (may include [ip]:port)
			`(?:\((self)\)\s*)?` + // 4: optional "self"
			`(?:next_index=(\d+)\s+match_index=(\d+)\s*)?` + // 5: next_index, 6: match_index
			`(?:last msg\s+(\d+)\s+ms\s+ago)?\s*$`, // 7: last msg ms
	)
)

func parseServersFromTextWithThreshold(text string, freshThreshold time.Duration) ClusterStatus {
	if freshThreshold <= 0 {
		freshThreshold = 10 * time.Second
	}
	freshMs := int64(freshThreshold / time.Millisecond)

	cs := ClusterStatus{}
	section := extractServersBlock(text)
	for _, ln := range strings.Split(section, "\n") {
		ln = strings.TrimRight(ln, "\r")
		if ln == "" {
			continue
		}

		// 1) Modern format
		if m := reServerModern.FindStringSubmatch(ln); len(m) > 0 {
			role := strings.ToLower(strings.TrimSpace(m[4]))
			cs.Servers = append(cs.Servers, ClusterServer{
				SID:       m[1],
				Self:      strings.Contains(m[2], "self"),
				Address:   strings.TrimSpace(m[3]),
				Role:      role,
				Connected: !strings.Contains(role, "disconn"),
			})
			continue
		}

		// 2) Legacy format (with optional indices and last-msg)
		if m := reServerLegacy.FindStringSubmatch(ln); len(m) > 0 {
			var (
				nextIdxPtr, matchIdxPtr, lastMsgPtr *int64
			)
			if m[5] != "" {
				if v, err := strconv.ParseInt(m[5], 10, 64); err == nil {
					nextIdxPtr = &v
				}
			}
			if m[6] != "" {
				if v, err := strconv.ParseInt(m[6], 10, 64); err == nil {
					matchIdxPtr = &v
				}
			}
			if m[7] != "" {
				if v, err := strconv.ParseInt(m[7], 10, 64); err == nil {
					lastMsgPtr = &v
				}
			}

			s := ClusterServer{
				SID:        m[1],
				Self:       m[4] == "self",
				Address:    strings.TrimSpace(m[3]),
				NextIndex:  nextIdxPtr,
				MatchIndex: matchIdxPtr,
				LastMsgMs:  lastMsgPtr,
				// Role unknown in this legacy format; leave empty.
			}

			// Connected heuristic:
			switch {
			case lastMsgPtr != nil:
				s.Connected = *lastMsgPtr <= freshMs
			case s.Self:
				s.Connected = true
			case nextIdxPtr != nil || matchIdxPtr != nil:
				// Seeing replication indices implies active exchange recently.
				s.Connected = true
			default:
				s.Connected = false
			}

			cs.Servers = append(cs.Servers, s)
			continue
		}

		// Unknown line → ignore
	}
	return cs
}

func extractServersBlock(text string) string {
	idx := reServersHeader.FindStringIndex(text)
	if idx == nil {
		return ""
	}
	rest := text[idx[1]:]

	var b strings.Builder
	lines := strings.Split(rest, "\n")
	sawAny := false

	for _, ln := range lines {
		// Normalize line endings and look at indentation
		ln = strings.TrimRight(ln, "\r") // handle CRLF
		trimmed := strings.TrimSpace(ln)

		// Blank line terminates the section *after* we've started collecting
		if trimmed == "" {
			if sawAny {
				break
			}
			continue
		}

		// Does the line belong to the Servers block?
		if startsWithUnicodeSpace(ln) || strings.HasPrefix(strings.TrimLeftFunc(ln, unicode.IsSpace), "-") {
			b.WriteString(ln)
			b.WriteByte('\n')
			sawAny = true
			continue
		}

		// First non-indented, non-blank line after we've started → end of block.
		if sawAny {
			break
		}
		// If we haven't started yet and this line isn't indented, keep scanning
		// (defensive; normally the very next line after "Servers:" is indented).
	}

	return b.String()
}

func startsWithUnicodeSpace(s string) bool {
	if s == "" {
		return false
	}
	r, _ := utf8.DecodeRuneInString(s)
	return unicode.IsSpace(r) // catches ' ', '\t', '\r', etc.
}
