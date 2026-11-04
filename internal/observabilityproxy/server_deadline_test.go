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
	"net"
	"net/http"
	"sync"
	"testing"
	"time"
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

// TestServerSlowBodyHoldsSlotUntilDeadline sends a POST whose body never
// completes: it must count against the user's concurrency, and the read
// must give up at the request timeout instead of holding the handler.
func TestServerSlowBodyHoldsSlotUntilDeadline(t *testing.T) {
	h := newHarness(t, Config{
		RequestTimeout: 200 * time.Millisecond,
		Limiter:        LimiterConfig{MaxConcurrentPerUser: 1},
	}, nil)
	pipe := servePipe(t, h.server)
	conn := pipe.send(t, "POST "+logsRoot+"query HTTP/1.1\r\nHost: proxy\r\nAuthorization: Bearer good\r\n"+
		"Content-Type: application/x-www-form-urlencoded\r\nContent-Length: 100\r\n\r\nquery=")

	eventually(t, time.Second, func() bool {
		return h.status("GET", logsRoot+"query", "good") == http.StatusTooManyRequests
	}, "a request with an unfinished body does not hold a concurrency slot")
	if resp := readResponse(t, conn); resp.StatusCode != http.StatusRequestTimeout {
		t.Fatalf("unfinished body answered %d, want 408", resp.StatusCode)
	}
	eventually(t, 2*time.Second, func() bool {
		return h.status("GET", logsRoot+"query", "good") == http.StatusOK
	}, "the slot of an unfinished body was not released")
}
