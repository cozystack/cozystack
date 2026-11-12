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

package sse

import (
	"context"
	"errors"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// streamWriter accepts the first okWrites writes and fails every one after.
type streamWriter struct {
	mu       sync.Mutex
	header   http.Header
	okWrites int
	body     strings.Builder
}

func (w *streamWriter) Header() http.Header { return w.header }
func (w *streamWriter) WriteHeader(int)     {}
func (w *streamWriter) Flush()              {}

func (w *streamWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.okWrites == 0 {
		return 0, errors.New("connection reset")
	}
	w.okWrites--
	return w.body.Write(p)
}

func (w *streamWriter) String() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.body.String()
}

// serve runs the stream handler for a client that never disconnects and
// reports whether the handler gave the client up within a second.
func serve(t *testing.T, s *Server, w *streamWriter, publish string) bool {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	r := httptest.NewRequest(http.MethodGet, "/stream", nil).WithContext(ctx)
	done := make(chan struct{})
	go func() {
		s.handleStream(w, r)
		close(done)
	}()
	if publish != "" {
		deadline := time.Now().Add(time.Second)
		for len(s.snapshotClients()) == 0 && time.Now().Before(deadline) {
			time.Sleep(time.Millisecond)
		}
		// Onto the client's queue, which only the pump reads, so the failing
		// write is the pump's and not the handler's write of latest.
		for _, c := range s.snapshotClients() {
			c.ch <- publish
		}
	}
	select {
	case <-done:
		return len(s.snapshotClients()) == 0
	case <-time.After(time.Second):
		return false
	}
}

func (s *Server) snapshotClients() []*client {
	s.clientsMu.RLock()
	defer s.clientsMu.RUnlock()
	var out []*client
	for c := range s.clients {
		out = append(out, c)
	}
	return out
}

func newTestServer(heartbeat time.Duration) *Server {
	return New(Options{Heartbeat: heartbeat, Logger: log.New(io.Discard, "", 0)})
}

// A client whose connection stops taking writes is dropped at once, at
// whichever write first fails, instead of holding its handler until the
// request context ends.
func TestStreamDropsClientOnWriteFailure(t *testing.T) {
	for _, tc := range []struct {
		name      string
		okWrites  int
		latest    string
		publish   string
		heartbeat time.Duration
	}{
		{name: "connect comment", okWrites: 0, heartbeat: time.Hour},
		{name: "latest payload", okWrites: 1, latest: "state", heartbeat: time.Hour},
		{name: "published event", okWrites: 1, publish: "event", heartbeat: time.Hour},
		{name: "heartbeat", okWrites: 1, heartbeat: 10 * time.Millisecond},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := newTestServer(tc.heartbeat)
			if tc.latest != "" {
				s.Publish(tc.latest)
			}
			if !serve(t, s, &streamWriter{header: http.Header{}, okWrites: tc.okWrites}, tc.publish) {
				t.Fatal("the handler kept a client whose writes fail")
			}
		})
	}
}

// Each line of a payload is its own data field, and the event ends with an
// empty line.
func TestStreamFramesMultilinePayload(t *testing.T) {
	s := newTestServer(time.Hour)
	s.Publish("one\ntwo\n")
	w := &streamWriter{header: http.Header{}, okWrites: 100}
	ctx, cancel := context.WithCancel(context.Background())
	r := httptest.NewRequest(http.MethodGet, "/stream", nil).WithContext(ctx)
	done := make(chan struct{})
	go func() {
		s.handleStream(w, r)
		close(done)
	}()
	deadline := time.Now().Add(time.Second)
	for !strings.Contains(w.String(), "data: two\n\n") && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	cancel()
	<-done
	if got := w.String(); !strings.Contains(got, "data: one\ndata: two\n\n") {
		t.Fatalf("stream = %q, want both lines framed as one event", got)
	}
	if len(s.snapshotClients()) != 0 {
		t.Fatal("a disconnected client stayed registered")
	}
}

// Shutdown ends every open stream, and the handler that then returns does
// not close the client a second time.
func TestShutdownEndsOpenStreams(t *testing.T) {
	s := newTestServer(time.Hour)
	w := &streamWriter{header: http.Header{}, okWrites: 100}
	r := httptest.NewRequest(http.MethodGet, "/stream", nil)
	done := make(chan struct{})
	go func() {
		s.handleStream(w, r)
		close(done)
	}()
	deadline := time.Now().Add(time.Second)
	for len(s.snapshotClients()) == 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if err := s.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("Shutdown left the stream open")
	}
}
