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

// A stream still open when the server shuts down ends when its client
// disconnects, without closing the client a second time.
func TestShutdownThenDisconnect(t *testing.T) {
	s := newTestServer(time.Hour)
	ctx, cancel := context.WithCancel(context.Background())
	r := httptest.NewRequest(http.MethodGet, "/stream", nil).WithContext(ctx)
	done := make(chan struct{})
	go func() {
		s.handleStream(&streamWriter{header: http.Header{}, okWrites: 100}, r)
		close(done)
	}()
	deadline := time.Now().Add(time.Second)
	for len(s.snapshotClients()) == 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if err := s.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("the stream did not end after its client disconnected")
	}
}
