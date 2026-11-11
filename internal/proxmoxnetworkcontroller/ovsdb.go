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

// A minimal OVSDB client (RFC 7047): one JSON-RPC connection, the transact
// method, and answers to the server's echo keepalive. The controller touches
// three OVN NB tables with a handful of operations, which does not justify
// libovsdb: that module brings a model generator, a monitor cache and its own
// set of k8s-adjacent dependencies, for what is a few hundred lines of JSON.
//
// Only what this package sends and reads is encoded: string, integer and uuid
// atoms, sets of uuids and string-to-string maps.

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"sort"
	"strings"
	"time"
)

const (
	ovnNorthbound = "OVN_Northbound"

	// defaultOVSDBTimeout bounds one transaction when the caller's context
	// allows longer.
	defaultOVSDBTimeout = 10 * time.Second
	// defaultOVSDBDialTimeout bounds the TCP connect and the TLS handshake.
	// The NB is in the cluster; one that does not answer within this is not
	// going to.
	defaultOVSDBDialTimeout = 3 * time.Second
	// defaultOVSDBConnectBackoff is how long a failed connect is returned to
	// every caller without trying again, so an NB that is down costs one
	// dial timeout per backoff, not one per network.
	defaultOVSDBConnectBackoff = 15 * time.Second
	// defaultOVSDBMaxConnAge bounds how long one connection is reused. The
	// default address is a Service that selects the RAFT leader; a fresh
	// connection follows a leader change, an old one keeps talking to a
	// follower, which forwards writes but may serve stale reads.
	defaultOVSDBMaxConnAge = time.Minute
	// defaultOVSDBMaxIdle bounds how long a connection may sit unused and
	// still be reused. Nothing reads an idle connection, so the server's
	// echo probes go unanswered and it hangs up after its inactivity probe
	// interval (5s at the least, Kube-OVN sets 180s); a request on such a
	// connection would fail half-way, with no way to know whether it was
	// applied. The transactions of one reconcile pass share a connection.
	defaultOVSDBMaxIdle = 4 * time.Second
)

// ovsdbTransactor runs one OVSDB transaction. The NB client implements it;
// tests substitute an in-memory database.
type ovsdbTransactor interface {
	Transact(ctx context.Context, ops ...ovsdbOp) ([]ovsdbResult, error)
}

// ovsdbOp is one operation of a transact request. Only the members its op
// takes are encoded: ovsdb-server rejects any other.
type ovsdbOp struct {
	Op        string
	Table     string
	Where     []ovsdbCondition
	Columns   []string
	Row       map[string]any
	Mutations []ovsdbMutation
	UUIDName  string
	// Until and WaitRows make a wait operation: the transaction aborts
	// unless the rows matching Where, projected on Columns, are exactly
	// WaitRows (Until "==") or are not (Until "!="). Its timeout is 0, as
	// for the waits ovsdb-idl sends for verified columns.
	Until    string
	WaitRows []map[string]any
}

// ovsdbCondition is [column, function, value].
type ovsdbCondition [3]any

// ovsdbMutation is [column, mutator, value].
type ovsdbMutation [3]any

func ovsdbWhere(column, function string, value any) ovsdbCondition {
	return ovsdbCondition{column, function, value}
}

func ovsdbMutate(column, mutator string, value any) ovsdbMutation {
	return ovsdbMutation{column, mutator, value}
}

// MarshalJSON encodes the operation with exactly the members RFC 7047 allows
// for its op; where is always present for the ops that take it.
func (o ovsdbOp) MarshalJSON() ([]byte, error) {
	m := map[string]any{"op": o.Op, "table": o.Table}
	switch o.Op {
	case "select", "update", "mutate", "delete", "wait":
		where := o.Where
		if where == nil {
			where = []ovsdbCondition{}
		}
		m["where"] = where
	}
	switch o.Op {
	case "insert":
		m["row"] = o.Row
		if o.UUIDName != "" {
			m["uuid-name"] = o.UUIDName
		}
	case "update":
		m["row"] = o.Row
	case "mutate":
		m["mutations"] = o.Mutations
	case "select":
		if o.Columns != nil {
			m["columns"] = o.Columns
		}
	case "wait":
		rows := o.WaitRows
		if rows == nil {
			rows = []map[string]any{}
		}
		m["columns"], m["until"], m["rows"], m["timeout"] = o.Columns, o.Until, rows, 0
	}
	return json.Marshal(m)
}

// ovsUUID is a uuid atom, ["uuid", "<uuid>"].
type ovsUUID string

func (u ovsUUID) MarshalJSON() ([]byte, error) {
	return json.Marshal([]string{"uuid", string(u)})
}

func (u *ovsUUID) UnmarshalJSON(b []byte) error {
	var v []string
	if err := json.Unmarshal(b, &v); err != nil || len(v) != 2 || v[0] != "uuid" {
		return fmt.Errorf("not a uuid atom: %s", b)
	}
	*u = ovsUUID(v[1])
	return nil
}

// ovsNamedUUID refers to a row inserted earlier in the same transaction under
// that uuid-name, ["named-uuid", "<name>"].
type ovsNamedUUID string

func (n ovsNamedUUID) MarshalJSON() ([]byte, error) {
	return json.Marshal([]string{"named-uuid", string(n)})
}

// ovsSet is a set of atoms, ["set", [...]]. A one-element set is encoded as
// the bare atom, the way ovsdb-server writes it.
type ovsSet []any

func (s ovsSet) MarshalJSON() ([]byte, error) {
	if len(s) == 1 {
		return json.Marshal(s[0])
	}
	elems := []any(s)
	if elems == nil {
		elems = []any{}
	}
	return json.Marshal([]any{"set", elems})
}

// ovsMap is a string-to-string map, ["map", [[k, v], ...]], sorted by key.
type ovsMap map[string]string

func (m ovsMap) MarshalJSON() ([]byte, error) {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	pairs := make([][2]string, 0, len(keys))
	for _, k := range keys {
		pairs = append(pairs, [2]string{k, m[k]})
	}
	return json.Marshal([]any{"map", pairs})
}

// ovsdbResult is one element of a transact reply. Elements after a failed
// operation are null and decode to the zero value.
type ovsdbResult struct {
	Count   int        `json:"count,omitempty"`
	UUID    *ovsUUID   `json:"uuid,omitempty"`
	Rows    []ovsdbRow `json:"rows,omitempty"`
	Error   string     `json:"error,omitempty"`
	Details string     `json:"details,omitempty"`
}

// ovsdbRow is a selected row, column by column in wire encoding.
type ovsdbRow map[string]json.RawMessage

func (r ovsdbRow) str(col string) string {
	var s string
	_ = json.Unmarshal(r[col], &s)
	return s
}

func (r ovsdbRow) integer(col string) int {
	var n int
	_ = json.Unmarshal(r[col], &n)
	return n
}

// uuids reads a uuid column of any cardinality: an atom, or a set of zero or
// more atoms.
func (r ovsdbRow) uuids(col string) []string {
	var v []json.RawMessage
	if err := json.Unmarshal(r[col], &v); err != nil || len(v) != 2 {
		return nil
	}
	var tag string
	if err := json.Unmarshal(v[0], &tag); err != nil {
		return nil
	}
	switch tag {
	case "uuid":
		var u ovsUUID
		if err := u.UnmarshalJSON(r[col]); err != nil {
			return nil
		}
		return []string{string(u)}
	case "set":
		var elems []ovsUUID
		if err := json.Unmarshal(v[1], &elems); err != nil {
			return nil
		}
		out := make([]string, 0, len(elems))
		for _, e := range elems {
			out = append(out, string(e))
		}
		return out
	}
	return nil
}

func (r ovsdbRow) uuid(col string) string {
	if u := r.uuids(col); len(u) == 1 {
		return u[0]
	}
	return ""
}

func (r ovsdbRow) strMap(col string) map[string]string {
	var raw []json.RawMessage
	if err := json.Unmarshal(r[col], &raw); err != nil || len(raw) != 2 {
		return map[string]string{}
	}
	var tag string
	var pairs [][2]string
	if err := json.Unmarshal(raw[0], &tag); err != nil || tag != "map" {
		return map[string]string{}
	}
	if err := json.Unmarshal(raw[1], &pairs); err != nil {
		return map[string]string{}
	}
	out := make(map[string]string, len(pairs))
	for _, p := range pairs {
		out[p[0]] = p[1]
	}
	return out
}

// ovsdbOpError is an operation or commit failure the server reported inside
// an otherwise successful reply. The connection stays usable.
type ovsdbOpError struct {
	Index   int // operation index; len(ops) for a commit failure
	Op      string
	Kind    string // the RFC 7047 error string, e.g. "constraint violation"
	Details string
}

func (e *ovsdbOpError) Error() string {
	what := "commit"
	if e.Op != "" {
		what = fmt.Sprintf("operation %d (%s)", e.Index, e.Op)
	}
	if e.Details != "" {
		return fmt.Sprintf("ovsdb %s failed: %s: %s", what, e.Kind, e.Details)
	}
	return fmt.Sprintf("ovsdb %s failed: %s", what, e.Kind)
}

// checkResults turns the first failure in a reply into an error.
func checkResults(ops []ovsdbOp, res []ovsdbResult) error {
	if len(res) < len(ops) {
		return fmt.Errorf("ovsdb reply has %d results for %d operations", len(res), len(ops))
	}
	for i, r := range res {
		if r.Error == "" {
			continue
		}
		e := &ovsdbOpError{Index: i, Kind: r.Error, Details: r.Details}
		if i < len(ops) {
			e.Op = ops[i].Op
		}
		return e
	}
	return nil
}

// +kubebuilder:object:generate=false

// TLSConfigSource hands out the client TLS configuration for an ssl:
// address. Invalidate is called after a failed handshake, so rotated
// certificates are picked up even when the source could not tell they
// changed.
type TLSConfigSource interface {
	TLSConfig(ctx context.Context) (*tls.Config, error)
	Invalidate()
}

// +kubebuilder:object:generate=false

// NBClient is an OVSDB client for the OVN northbound database. It holds one
// connection, reused while it is younger than a minute and was used in the
// last few seconds, and dropped on any transport error. Calls are serialised;
// a caller waiting for another one's call gives up with its context. After a
// failed connect it fails fast for a while (defaultOVSDBConnectBackoff).
type NBClient struct {
	addresses      []string
	database       string
	tls            TLSConfigSource
	timeout        time.Duration
	dialTimeout    time.Duration
	connectBackoff time.Duration
	maxAge         time.Duration
	maxIdle        time.Duration
	dial           func(ctx context.Context, network, address string) (net.Conn, error)

	// sem is the lock of everything below: a channel, so that waiting for
	// it can end with the caller's context.
	sem         chan struct{}
	conn        net.Conn
	dec         *json.Decoder
	opened      time.Time
	lastUsed    time.Time
	nextID      uint64
	failedUntil time.Time
	connErr     error
}

// NewNBClient returns a client for a comma-separated list of OVSDB addresses
// ("ssl:host:port" or "tcp:host:port"), tried in order on each connect. The
// TLS source is only used for ssl: addresses and may be nil without them.
func NewNBClient(addresses string, tlsSource TLSConfigSource) (*NBClient, error) {
	var list []string
	for _, a := range strings.Split(addresses, ",") {
		a = strings.TrimSpace(a)
		if a == "" {
			continue
		}
		scheme, _, err := splitOVSDBAddress(a)
		if err != nil {
			return nil, err
		}
		if scheme == "ssl" && tlsSource == nil {
			return nil, fmt.Errorf("OVSDB address %q needs TLS material", a)
		}
		list = append(list, a)
	}
	if len(list) == 0 {
		return nil, errors.New("no OVSDB address given")
	}
	d := &net.Dialer{KeepAlive: 30 * time.Second}
	return &NBClient{
		addresses: list, database: ovnNorthbound, tls: tlsSource,
		timeout: defaultOVSDBTimeout, dialTimeout: defaultOVSDBDialTimeout, connectBackoff: defaultOVSDBConnectBackoff,
		maxAge: defaultOVSDBMaxConnAge, maxIdle: defaultOVSDBMaxIdle, dial: d.DialContext,
		sem: make(chan struct{}, 1),
	}, nil
}

// splitOVSDBAddress parses "ssl:host:port" and "tcp:host:port", with an IPv6
// host in brackets.
func splitOVSDBAddress(a string) (scheme, hostPort string, err error) {
	scheme, hostPort, ok := strings.Cut(a, ":")
	if !ok || (scheme != "ssl" && scheme != "tcp") {
		return "", "", fmt.Errorf("OVSDB address %q: want ssl:<host>:<port> or tcp:<host>:<port>", a)
	}
	if _, _, err := net.SplitHostPort(hostPort); err != nil {
		return "", "", fmt.Errorf("OVSDB address %q: %w", a, err)
	}
	return scheme, hostPort, nil
}

// Transact runs ops as one transaction. A transport failure closes the
// connection and is returned as is; a failed operation is an *ovsdbOpError
// and the results are returned with it.
func (c *NBClient) Transact(ctx context.Context, ops ...ovsdbOp) ([]ovsdbResult, error) {
	select {
	case c.sem <- struct{}{}:
	case <-ctx.Done():
		return nil, fmt.Errorf("wait for the OVN NB connection: %w", ctx.Err())
	}
	defer func() { <-c.sem }()
	if c.conn != nil && (time.Since(c.opened) > c.maxAge || time.Since(c.lastUsed) > c.maxIdle) {
		c.closeLocked()
	}
	if c.conn == nil {
		if time.Now().Before(c.failedUntil) {
			return nil, fmt.Errorf("not retrying until %s: %w", c.failedUntil.UTC().Format(time.RFC3339), c.connErr)
		}
		if err := c.connectLocked(ctx); err != nil {
			c.failedUntil, c.connErr = time.Now().Add(c.connectBackoff), err
			return nil, err
		}
		c.failedUntil, c.connErr = time.Time{}, nil
	}
	res, err := c.transactLocked(ctx, ops)
	if err != nil {
		c.closeLocked()
		return nil, err
	}
	c.lastUsed = time.Now()
	return res, checkResults(ops, res)
}

// Close drops the connection; the next Transact opens a new one.
func (c *NBClient) Close() {
	c.sem <- struct{}{}
	defer func() { <-c.sem }()
	c.closeLocked()
}

func (c *NBClient) closeLocked() {
	if c.conn != nil {
		_ = c.conn.Close()
	}
	c.conn, c.dec = nil, nil
}

func (c *NBClient) connectLocked(ctx context.Context) error {
	var errs []error
	for _, a := range c.addresses {
		conn, err := c.dialOne(ctx, a)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		c.conn, c.dec, c.opened, c.lastUsed = conn, json.NewDecoder(conn), time.Now(), time.Now()
		return nil
	}
	return fmt.Errorf("connect to OVN NB: %w", errors.Join(errs...))
}

func (c *NBClient) dialOne(ctx context.Context, address string) (net.Conn, error) {
	scheme, hostPort, err := splitOVSDBAddress(address)
	if err != nil {
		return nil, err
	}
	dctx, cancel := context.WithTimeout(ctx, c.dialTimeout)
	defer cancel()
	raw, err := c.dial(dctx, "tcp", hostPort)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", address, err)
	}
	if scheme == "tcp" {
		return raw, nil
	}
	cfg, err := c.tls.TLSConfig(dctx)
	if err != nil {
		_ = raw.Close()
		return nil, fmt.Errorf("%s: %w", address, err)
	}
	conn := tls.Client(raw, cfg)
	if err := conn.HandshakeContext(dctx); err != nil {
		_ = raw.Close()
		c.tls.Invalidate()
		return nil, fmt.Errorf("%s: TLS handshake: %w", address, err)
	}
	return conn, nil
}

type rpcMessage struct {
	ID     json.RawMessage `json:"id,omitempty"`
	Method string          `json:"method,omitempty"`
	Params json.RawMessage `json:"params,omitempty"`
	Result json.RawMessage `json:"result,omitempty"`
	Error  json.RawMessage `json:"error,omitempty"`
}

func (c *NBClient) transactLocked(ctx context.Context, ops []ovsdbOp) ([]ovsdbResult, error) {
	deadline := time.Now().Add(c.timeout)
	if d, ok := ctx.Deadline(); ok && d.Before(deadline) {
		deadline = d
	}
	if err := c.conn.SetDeadline(deadline); err != nil {
		return nil, err
	}
	conn := c.conn
	stop := context.AfterFunc(ctx, func() { _ = conn.SetDeadline(time.Now()) })
	defer stop()

	c.nextID++
	id := c.nextID
	params := make([]any, 0, len(ops)+1)
	params = append(params, c.database)
	for _, op := range ops {
		params = append(params, op)
	}
	req, err := json.Marshal(map[string]any{"method": "transact", "params": params, "id": id})
	if err != nil {
		return nil, err
	}
	if _, err := conn.Write(req); err != nil {
		return nil, fmt.Errorf("send transact: %w", err)
	}
	for {
		var msg rpcMessage
		if err := c.dec.Decode(&msg); err != nil {
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			return nil, fmt.Errorf("read transact reply: %w", err)
		}
		if msg.Method != "" {
			// Requests from the server: answer echo, ignore notifications.
			if msg.Method == "echo" && len(msg.ID) > 0 && string(msg.ID) != "null" {
				params := msg.Params
				if len(params) == 0 {
					params = json.RawMessage("[]")
				}
				reply, _ := json.Marshal(map[string]any{"id": msg.ID, "result": params, "error": nil})
				if _, err := conn.Write(reply); err != nil {
					return nil, fmt.Errorf("answer echo: %w", err)
				}
			}
			continue
		}
		var got uint64
		if err := json.Unmarshal(msg.ID, &got); err != nil || got != id {
			continue // a reply to a request this connection gave up on
		}
		if len(msg.Error) > 0 && string(msg.Error) != "null" {
			return nil, fmt.Errorf("ovsdb transact: %s", msg.Error)
		}
		var res []ovsdbResult
		if err := json.Unmarshal(msg.Result, &res); err != nil {
			return nil, fmt.Errorf("decode transact reply: %w", err)
		}
		return res, nil
	}
}
