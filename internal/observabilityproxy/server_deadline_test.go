/*
Copyright 2026 The Cozystack Authors.

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

package observabilityproxy

import (
	"bufio"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
)

// pipeListener hands the server one end of a net.Pipe per dial. A pipe has
// no buffer, so a client that stops reading blocks the server's next write
// at once, as a full TCP window eventually does.
type pipeListener struct {
	conns  chan net.Conn
	closed chan struct{}
	once   sync.Once
}

type pipeAddr struct{}

func (pipeAddr) Network() string { return "pipe" }
func (pipeAddr) String() string  { return "pipe" }

func servePipe(t *testing.T, handler http.Handler) *pipeListener {
	t.Helper()
	l := &pipeListener{conns: make(chan net.Conn), closed: make(chan struct{})}
	srv := &http.Server{Handler: handler, ReadHeaderTimeout: 10 * time.Second}
	go func() { _ = srv.Serve(l) }()
	t.Cleanup(func() { _ = srv.Close() })
	return l
}

func (l *pipeListener) Accept() (net.Conn, error) {
	select {
	case c := <-l.conns:
		return c, nil
	case <-l.closed:
		return nil, net.ErrClosed
	}
}

func (l *pipeListener) Close() error {
	l.once.Do(func() { close(l.closed) })
	return nil
}

func (l *pipeListener) Addr() net.Addr { return pipeAddr{} }

// send opens a connection and writes raw to it without waiting for the
// server to read all of it.
func (l *pipeListener) send(t *testing.T, raw string) net.Conn {
	t.Helper()
	client, server := net.Pipe()
	t.Cleanup(func() { _ = client.Close() })
	select {
	case l.conns <- server:
	case <-time.After(2 * time.Second):
		t.Fatal("pipe server is not accepting")
	}
	go func() { _, _ = client.Write([]byte(raw)) }()
	return client
}

// slotsHeld counts the concurrency and tail slots in use. Tests watch it
// instead of probing with requests of their own, which would compete for
// the slot under test.
func slotsHeld(h *harness) int {
	h.server.limits.mu.Lock()
	defer h.server.limits.mu.Unlock()
	n := h.server.limits.total
	for _, c := range h.server.limits.active {
		n += c
	}
	return n
}

func eventually(t *testing.T, within time.Duration, cond func() bool, msg string) {
	t.Helper()
	deadline := time.Now().Add(within)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatal(msg)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func readResponse(t *testing.T, conn net.Conn) *http.Response {
	t.Helper()
	got := make(chan *http.Response, 1)
	go func() {
		resp, err := http.ReadResponse(bufio.NewReader(conn), nil)
		if err != nil {
			close(got)
			return
		}
		got <- resp
	}()
	select {
	case resp, ok := <-got:
		if !ok {
			t.Fatal("reading the response failed")
		}
		return resp
	case <-time.After(3 * time.Second):
		t.Fatal("no response")
	}
	return nil
}

// atUpstream calls probe each time a request reaches the upstream and
// sends the result on the returned channel. The proxy holds the request's
// slot from before it calls the upstream until the upstream answers, so
// the probe sees it held however late the test goroutine gets scheduled.
func atUpstream(probe func(*harness) int, next http.HandlerFunc) (http.HandlerFunc, *atomic.Pointer[harness], <-chan int) {
	var h atomic.Pointer[harness]
	seen := make(chan int, 1)
	return func(w http.ResponseWriter, r *http.Request) {
		seen <- probe(h.Load())
		next(w, r)
	}, &h, seen
}

func receive(t *testing.T, ch <-chan int, msg string) int {
	t.Helper()
	select {
	case v := <-ch:
		return v
	case <-time.After(3 * time.Second):
		t.Fatal(msg)
	}
	return 0
}

// TestServerSlowBodyHoldsSlotUntilDeadline sends a POST whose body never
// completes: it must count against the user's concurrency, and the read
// must give up at the request timeout instead of holding the handler.
func TestServerSlowBodyHoldsSlotUntilDeadline(t *testing.T) {
	// The slot is only observable while the body is pending, so the
	// timeout is long enough for the poll below to see it on a busy runner.
	h := newHarness(t, Config{
		RequestTimeout: time.Second,
		Limiter:        LimiterConfig{MaxConcurrentPerUser: 1},
	}, nil)
	pipe := servePipe(t, h.server)
	conn := pipe.send(t, "POST "+logsRoot+"query HTTP/1.1\r\nHost: proxy\r\nAuthorization: Bearer good\r\n"+
		"Content-Type: application/x-www-form-urlencoded\r\nContent-Length: 100\r\n\r\nquery=")

	eventually(t, time.Second, func() bool { return slotsHeld(h) == 1 },
		"a request with an unfinished body does not hold a concurrency slot")
	if resp := readResponse(t, conn); resp.StatusCode != http.StatusRequestTimeout {
		t.Fatalf("unfinished body answered %d, want 408", resp.StatusCode)
	}
	eventually(t, 2*time.Second, func() bool { return slotsHeld(h) == 0 },
		"the slot of an unfinished body was not released")
}

// TestServerStalledClientReleasesTail holds a tail whose client stops
// reading, once before the headers and once after the first line: the
// slot must come back within a few idle timeouts in both cases.
func TestServerStalledClientReleasesTail(t *testing.T) {
	for _, readHeaders := range []bool{false, true} {
		lines := make(chan string, 1)
		done := make(chan struct{})
		upstream, hp, held := atUpstream(func(h *harness) int { return h.server.limits.openTails() }, tailUpstream(lines, done))
		h := newHarness(t, Config{
			TailIdleTimeout: 200 * time.Millisecond,
			Limiter:         LimiterConfig{MaxTails: 1},
		}, upstream)
		hp.Store(h)
		t.Cleanup(func() { close(done) })
		pipe := servePipe(t, h.server)
		conn := pipe.send(t, "GET "+logsRoot+"tail?query=* HTTP/1.1\r\nHost: proxy\r\nAuthorization: Bearer good\r\n\r\n")
		if readHeaders {
			if resp := readResponse(t, conn); resp.StatusCode != http.StatusOK {
				t.Fatalf("tail answered %d", resp.StatusCode)
			}
			lines <- `{"_msg":"unread"}`
		}
		if n := receive(t, held, "the tail never reached the store"); n != 1 {
			t.Fatalf("open tails while the store answers = %d, want 1", n)
		}
		eventually(t, 2*time.Second, func() bool { return h.server.limits.openTails() == 0 },
			"a client that stopped reading keeps its tail slot")
	}
}

// TestServerStalledClientReleasesQuery does the same for a non-tail
// response, which must be written by the request deadline.
func TestServerStalledClientReleasesQuery(t *testing.T) {
	upstream, hp, held := atUpstream(slotsHeld, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/stream+json")
		_, _ = io.WriteString(w, `{"_msg":"hello"}`+"\n")
	})
	h := newHarness(t, Config{
		RequestTimeout: 200 * time.Millisecond,
		Limiter:        LimiterConfig{MaxConcurrentPerUser: 1},
	}, upstream)
	hp.Store(h)
	pipe := servePipe(t, h.server)
	pipe.send(t, "GET "+logsRoot+"query?query=* HTTP/1.1\r\nHost: proxy\r\nAuthorization: Bearer good\r\n\r\n")
	if n := receive(t, held, "the stalled request never reached the store"); n != 1 {
		t.Fatalf("slots held while the store answers = %d, want 1", n)
	}
	eventually(t, 2*time.Second, func() bool { return slotsHeld(h) == 0 },
		"a client that stopped reading keeps its concurrency slot")
}

// TestServerActiveTailOutlivesWriteDeadline keeps a tail busy for several
// idle timeouts: the write deadline must be renewed, not fixed.
func TestServerActiveTailOutlivesWriteDeadline(t *testing.T) {
	lines := make(chan string)
	done := make(chan struct{})
	t.Cleanup(func() { close(done) })
	h := newHarness(t, Config{TailIdleTimeout: 150 * time.Millisecond}, tailUpstream(lines, done))
	resp := h.do(http.MethodGet, logsRoot+"tail?query=*", "good", nil, nil)
	defer func() { _ = resp.Body.Close() }()
	rd := bufio.NewReader(resp.Body)
	for i := range 8 {
		lines <- `{"_msg":"line"}`
		if _, err := rd.ReadString('\n'); err != nil {
			t.Fatalf("line %d: %v", i, err)
		}
		time.Sleep(75 * time.Millisecond)
	}
}

// TestServerTailUpstreamWithoutHeaders gives up on a store that accepts a
// tail but never answers it, instead of holding the slot until the client
// leaves.
func TestServerTailUpstreamWithoutHeaders(t *testing.T) {
	release := make(chan struct{})
	h := newHarness(t, Config{
		TailIdleTimeout: 200 * time.Millisecond,
		Limiter:         LimiterConfig{MaxTails: 1},
	}, func(_ http.ResponseWriter, r *http.Request) {
		select {
		case <-release:
		case <-r.Context().Done():
		}
	})
	// After the harness, so it runs before the servers wait for handlers.
	t.Cleanup(func() { close(release) })
	got := make(chan int, 1)
	go func() { got <- h.status(http.MethodGet, logsRoot+"tail?query=*", "good") }()
	select {
	case code := <-got:
		if code != http.StatusGatewayTimeout {
			t.Fatalf("silent store answered %d, want 504", code)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("a tail waits for upstream headers without bound")
	}
	if n := h.server.limits.openTails(); n != 0 {
		t.Fatalf("open tails = %d after the tail ended", n)
	}
}

// TestServerAbortsTruncatedResponse cuts the client connection when the
// store's body breaks off after the headers went out, so a partial answer
// does not end as a clean 200.
func TestServerAbortsTruncatedResponse(t *testing.T) {
	h := newHarness(t, Config{}, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Length", "100")
		_, _ = io.WriteString(w, `{"_msg":"a"}`+"\n")
	})
	resp := h.do(http.MethodGet, logsRoot+"query?query=*", "good", nil, nil)
	defer func() { _ = resp.Body.Close() }()
	if _, err := io.ReadAll(resp.Body); err == nil {
		t.Fatal("a truncated store answer reached the client as a complete response")
	}
}

// TestServerUndeliveredBodyReleasesSlot declares a body that never comes
// on requests whose form parsing does not read it: a GET, a POST that is
// not a form, and a GET tail. net/http would drain that body with no
// deadline once the headers go out, so the slot must still come back.
func TestServerUndeliveredBodyReleasesSlot(t *testing.T) {
	for name, raw := range map[string]string{
		"get query":       "GET " + logsRoot + "query?query=* HTTP/1.1\r\nHost: proxy\r\nAuthorization: Bearer good\r\nContent-Length: 10\r\n\r\n",
		"post text query": "POST " + logsRoot + "query?query=* HTTP/1.1\r\nHost: proxy\r\nAuthorization: Bearer good\r\nContent-Type: text/plain\r\nContent-Length: 10\r\n\r\n",
		"get tail":        "GET " + logsRoot + "tail?query=* HTTP/1.1\r\nHost: proxy\r\nAuthorization: Bearer good\r\nContent-Length: 10\r\n\r\n",
	} {
		t.Run(name, func(t *testing.T) {
			lines := make(chan string, 1)
			done := make(chan struct{})
			h := newHarness(t, Config{
				RequestTimeout:  200 * time.Millisecond,
				TailIdleTimeout: 200 * time.Millisecond,
				Limiter:         LimiterConfig{MaxConcurrentPerUser: 1, MaxTails: 1},
			}, tailUpstream(lines, done))
			t.Cleanup(func() { close(done) })
			pipe := servePipe(t, h.server)
			conn := pipe.send(t, raw)
			held := func() bool { return slotsHeld(h) > 0 }
			eventually(t, time.Second, held, "the request never took a slot")
			if resp := readResponse(t, conn); resp.StatusCode != http.StatusRequestTimeout {
				t.Fatalf("undelivered body answered %d, want 408", resp.StatusCode)
			}
			eventually(t, time.Second, func() bool { return !held() }, "an undelivered body keeps its slot")
		})
	}
}

func TestServerOversizedBody(t *testing.T) {
	h := newHarness(t, Config{}, nil)
	for _, ct := range []string{"application/x-www-form-urlencoded", "text/plain"} {
		req, _ := http.NewRequest(http.MethodPost, h.proxy.URL+logsRoot+"query?query=*", strings.NewReader("query="+strings.Repeat("x", maxFormBytes)))
		req.Header.Set("Authorization", "Bearer good")
		req.Header.Set("Content-Type", ct)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusRequestEntityTooLarge {
			t.Errorf("%s body over the cap answered %d, want 413", ct, resp.StatusCode)
		}
	}
}

// TestServerEarlyRefusalWithUnreadBody refuses requests before the body is
// read: a bad token, a full concurrency slot, and a full tail slot.
// net/http drains up to 256 KiB of an unread body before it sends the
// headers, so the answer must not wait on a body that never comes, even
// under a long request timeout.
func TestServerEarlyRefusalWithUnreadBody(t *testing.T) {
	for _, timeout := range []time.Duration{200 * time.Millisecond, time.Minute} {
		for _, tc := range []struct {
			name, raw string
			hold      string
			want      int
		}{
			{"bad token", "POST " + logsRoot + "query HTTP/1.1\r\nHost: proxy\r\nAuthorization: Bearer bad\r\nContent-Type: application/x-www-form-urlencoded\r\nContent-Length: 1000\r\n\r\nquery=", "", http.StatusUnauthorized},
			{"concurrency slot taken", "POST " + logsRoot + "query HTTP/1.1\r\nHost: proxy\r\nAuthorization: Bearer good\r\nContent-Type: application/x-www-form-urlencoded\r\nContent-Length: 1000\r\n\r\nquery=", "query", http.StatusTooManyRequests},
			{"tail slot taken", "GET " + logsRoot + "tail?query=* HTTP/1.1\r\nHost: proxy\r\nAuthorization: Bearer good\r\nContent-Length: 1000\r\n\r\n", "tail", http.StatusTooManyRequests},
		} {
			// A held query ends at the request timeout by design, so the
			// slot is only reliably taken under the long one.
			if tc.hold == "query" && timeout < time.Second {
				continue
			}
			t.Run(fmt.Sprintf("%s/%s", timeout, tc.name), func(t *testing.T) {
				lines := make(chan string, 1)
				done := make(chan struct{})
				h := newHarness(t, Config{
					RequestTimeout:  timeout,
					TailIdleTimeout: time.Minute,
					Limiter:         LimiterConfig{MaxConcurrentPerUser: 1, MaxTails: 1},
				}, tailUpstream(lines, done))
				t.Cleanup(func() { close(done) })
				pipe := servePipe(t, h.server)
				if tc.hold != "" {
					pipe.send(t, "GET "+logsRoot+tc.hold+"?query=* HTTP/1.1\r\nHost: proxy\r\nAuthorization: Bearer good\r\n\r\n")
					eventually(t, time.Second, func() bool { return slotsHeld(h) == 1 }, "the first request never took its slot")
				}
				start := time.Now()
				conn := pipe.send(t, tc.raw)
				resp := readResponse(t, conn)
				if resp.StatusCode != tc.want || time.Since(start) > time.Second {
					t.Fatalf("answered %d after %s, want %d within a second", resp.StatusCode, time.Since(start), tc.want)
				}
			})
		}
	}
}

// stalledAPIServer answers the TokenReview for "good" unless stallTokens
// is set, and never answers a SubjectAccessReview: it holds each stalled
// call until the caller gives up or the test ends.
func stalledAPIServer(t *testing.T, stallTokens bool) kubernetes.Interface {
	t.Helper()
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/tokenreviews") && !stallTokens {
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"apiVersion":"authentication.k8s.io/v1","kind":"TokenReview","status":{"authenticated":true,"user":{"username":"alice"}}}`)
			return
		}
		select {
		case <-r.Context().Done():
		case <-release:
		}
	}))
	t.Cleanup(srv.Close)
	// After srv, so it runs first and srv.Close does not wait on a stalled call.
	t.Cleanup(func() { close(release) })
	return kubernetes.NewForConfigOrDie(&rest.Config{Host: srv.URL})
}

// TestServerStalledReviewAnswersInTime holds TokenReview or
// SubjectAccessReview open on a POST whose body is complete but unread:
// net/http does not watch the connection until the body is read, so only
// a bounded review call ends the request.
func TestServerStalledReviewAnswersInTime(t *testing.T) {
	for _, tc := range []struct {
		name, path  string
		stallTokens bool
	}{
		{"logs token review", logsRoot + "query", true},
		{"logs subject access review", logsRoot + "query", false},
		{"metrics token review", metricsRoot + "api/v1/query", true},
		{"metrics subject access review", metricsRoot + "api/v1/query", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t, Config{RequestTimeout: 200 * time.Millisecond}, nil)
			h.server.auth = NewAuthorizer(stalledAPIServer(t, tc.stallTokens), 10*time.Second)
			got := make(chan int, 1)
			go func() {
				resp := h.do(http.MethodPost, tc.path, "good", url.Values{"query": {"up"}}, nil)
				_ = resp.Body.Close()
				got <- resp.StatusCode
			}()
			select {
			case code := <-got:
				if code != http.StatusServiceUnavailable {
					t.Fatalf("stalled %s answered %d, want 503", tc.name, code)
				}
			case <-time.After(2 * time.Second):
				t.Fatalf("a stalled %s holds the request past ten request timeouts", tc.name)
			}
		})
	}
}

// TestServerMetricsRequestLifetime repeats the body, write and break-off
// checks on the metrics root, which has its own handler.
func TestServerMetricsRequestLifetime(t *testing.T) {
	query := metricsRoot + "api/v1/query?query=up"
	t.Run("slow body", func(t *testing.T) {
		h := newHarness(t, Config{
			RequestTimeout: 200 * time.Millisecond,
			Limiter:        LimiterConfig{MaxConcurrentPerUser: 1},
		}, promUpstream)
		pipe := servePipe(t, h.server)
		conn := pipe.send(t, "POST "+metricsRoot+"api/v1/query HTTP/1.1\r\nHost: proxy\r\nAuthorization: Bearer good\r\n"+
			"Content-Type: application/x-www-form-urlencoded\r\nContent-Length: 100\r\n\r\nquery=")
		eventually(t, time.Second, func() bool { return slotsHeld(h) == 1 },
			"a metrics request with an unfinished body does not hold a concurrency slot")
		if resp := readResponse(t, conn); resp.StatusCode != http.StatusRequestTimeout {
			t.Fatalf("unfinished body answered %d, want 408", resp.StatusCode)
		}
		eventually(t, time.Second, func() bool { return slotsHeld(h) == 0 }, "the slot of an unfinished body was not released")
	})
	t.Run("stalled client", func(t *testing.T) {
		// Larger than the server's write buffer, so the handler itself
		// blocks on the client.
		big := `{"status":"success","data":"` + strings.Repeat("x", 64<<10) + `"}`
		h := newHarness(t, Config{
			RequestTimeout: 200 * time.Millisecond,
			Limiter:        LimiterConfig{MaxConcurrentPerUser: 1},
		}, func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, big) })
		pipe := servePipe(t, h.server)
		pipe.send(t, "GET "+query+" HTTP/1.1\r\nHost: proxy\r\nAuthorization: Bearer good\r\n\r\n")
		eventually(t, time.Second, func() bool { return slotsHeld(h) == 1 }, "the stalled metrics request never held its slot")
		eventually(t, 2*time.Second, func() bool { return slotsHeld(h) == 0 },
			"a metrics client that stopped reading keeps its concurrency slot")
	})
	t.Run("truncated answer", func(t *testing.T) {
		h := newHarness(t, Config{}, func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Length", "100")
			_, _ = io.WriteString(w, `{"status":"success",`)
		})
		// Nothing has been flushed yet when the copy fails, so the
		// connection may close before the status line.
		req, _ := http.NewRequest(http.MethodGet, h.proxy.URL+query, nil)
		req.Header.Set("Authorization", "Bearer good")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			return
		}
		defer func() { _ = resp.Body.Close() }()
		if _, err := io.ReadAll(resp.Body); err == nil {
			t.Fatal("a truncated store answer reached the client as a complete response")
		}
	})
}
