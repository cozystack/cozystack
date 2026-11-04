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
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	authorizationv1 "k8s.io/api/authorization/v1"
)

type upstreamRequest struct {
	path    string
	form    url.Values
	headers http.Header
}

type harness struct {
	t        *testing.T
	kube     *fakeKube
	proxy    *httptest.Server
	server   *Server
	mu       sync.Mutex
	got      []upstreamRequest
	stores   []string
	upstream *httptest.Server
}

type mapStores map[string]string

func (m mapStores) Resolve(ns string) (string, error) { return m[ns], nil }

// newHarness wires the proxy to a fake apiserver that knows tokens "good"
// (alice) and "bob-token" (bob), both allowed list logs in tenant-a only,
// and to an upstream served by handler.
func newHarness(t *testing.T, cfg Config, handler http.HandlerFunc) *harness {
	h := &harness{t: t}
	h.kube = newFakeKube(map[string]string{"good": "alice", "bob-token": "bob"}, func(_ string, a *authorizationv1.ResourceAttributes) bool {
		return a.Namespace == "tenant-a" && a.Resource == "logs"
	})
	h.upstream = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			t.Errorf("upstream ParseForm: %v", err)
		}
		h.mu.Lock()
		h.got = append(h.got, upstreamRequest{path: r.URL.Path, form: r.PostForm, headers: r.Header.Clone()})
		h.mu.Unlock()
		if handler != nil {
			handler(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/stream+json")
		_, _ = io.WriteString(w, `{"_msg":"hello"}`+"\n")
	}))
	t.Cleanup(h.upstream.Close)
	cfg.UpstreamURL = func(store string) string {
		h.mu.Lock()
		h.stores = append(h.stores, store)
		h.mu.Unlock()
		return h.upstream.URL
	}
	if cfg.Logs == (LogsLimits{}) {
		cfg.Logs = testLimits()
	}
	h.server = NewServer(cfg, NewAuthorizer(h.kube.client, 10*time.Second), mapStores{"tenant-a": "tenant-a-store"}, nil)
	h.server.now = func() time.Time { return testNow }
	h.proxy = httptest.NewServer(h.server)
	t.Cleanup(h.proxy.Close)
	return h
}

const logsRoot = "/apis/cozystack.io/v1alpha1/namespaces/tenant-a/logs/select/logsql/"

func (h *harness) do(method, path, token string, body url.Values, headers map[string]string) *http.Response {
	h.t.Helper()
	var rd io.Reader
	if body != nil {
		rd = strings.NewReader(body.Encode())
	}
	req, err := http.NewRequest(method, h.proxy.URL+path, rd)
	if err != nil {
		h.t.Fatal(err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		h.t.Fatal(err)
	}
	return resp
}

func (h *harness) status(method, path, token string) int {
	h.t.Helper()
	resp := h.do(method, path, token, nil, nil)
	_, _ = io.Copy(io.Discard, resp.Body)
	_ = resp.Body.Close()
	return resp.StatusCode
}

func (h *harness) requests() []upstreamRequest {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]upstreamRequest(nil), h.got...)
}

func TestServerStatusCodes(t *testing.T) {
	h := newHarness(t, Config{}, nil)
	tests := []struct {
		name, method, path, token string
		want                      int
	}{
		{"ok", http.MethodGet, logsRoot + "query?query=error", "good", http.StatusOK},
		{"missing token", http.MethodGet, logsRoot + "query", "", http.StatusUnauthorized},
		{"invalid token", http.MethodGet, logsRoot + "query", "bad", http.StatusUnauthorized},
		{"other namespace", http.MethodGet, "/apis/cozystack.io/v1alpha1/namespaces/tenant-b/logs/select/logsql/query", "good", http.StatusForbidden},
		{"invalid namespace", http.MethodGet, "/apis/cozystack.io/v1alpha1/namespaces/Tenant/logs/select/logsql/query", "good", http.StatusBadRequest},
		{"unknown root", http.MethodGet, "/api/v1/namespaces/tenant-a/pods", "good", http.StatusNotFound},
		{"namespace metrics not served yet", http.MethodGet, "/apis/cozystack.io/v1alpha1/namespaces/tenant-a/metrics/api/v1/query", "good", http.StatusNotFound},
		{"application log not served yet", http.MethodGet, "/apis/apps.cozystack.io/v1alpha1/namespaces/tenant-a/postgreses/db/log/select/logsql/query", "good", http.StatusNotFound},
		{"application metrics not served yet", http.MethodGet, "/apis/apps.cozystack.io/v1alpha1/namespaces/tenant-a/postgreses/db/metrics/api/v1/query", "good", http.StatusNotFound},
		{"tenant_ids not proxied", http.MethodGet, "/apis/cozystack.io/v1alpha1/namespaces/tenant-a/logs/select/tenant_ids", "good", http.StatusNotFound},
		{"unknown logsql endpoint", http.MethodGet, logsRoot + "stats_query", "good", http.StatusNotFound},
		{"logs root itself", http.MethodGet, "/apis/cozystack.io/v1alpha1/namespaces/tenant-a/logs/", "good", http.StatusNotFound},
		{"ignore_global_time_filter", http.MethodGet, logsRoot + "query?query=" + url.QueryEscape("options(ignore_global_time_filter=true) *"), "good", http.StatusBadRequest},
		{"inverted range", http.MethodGet, logsRoot + "query?start=2026-11-04T11:00:00Z&end=2026-11-04T10:00:00Z", "good", http.StatusBadRequest},
		{"dot dot reaching another namespace", http.MethodGet, "/apis/cozystack.io/v1alpha1/namespaces/tenant-b/../tenant-a/logs/select/logsql/query", "good", http.StatusBadRequest},
		{"encoded dot dot", http.MethodGet, "/apis/cozystack.io/v1alpha1/namespaces/tenant-b/%2e%2e/tenant-a/logs/select/logsql/query", "good", http.StatusBadRequest},
		{"encoded slash", http.MethodGet, "/apis/cozystack.io/v1alpha1/namespaces/tenant-a%2Flogs/logs/select/logsql/query", "good", http.StatusBadRequest},
		{"double slash", http.MethodGet, "/apis/cozystack.io/v1alpha1/namespaces/tenant-a/logs//select/logsql/query", "good", http.StatusBadRequest},
		{"delete", http.MethodDelete, logsRoot + "query", "good", http.StatusMethodNotAllowed},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := h.status(tt.method, tt.path, tt.token); got != tt.want {
				t.Fatalf("status = %d, want %d", got, tt.want)
			}
		})
	}
	if reqs := h.requests(); len(reqs) != 1 {
		t.Fatalf("upstream saw %d requests, want only the allowed one", len(reqs))
	}
}

func TestServerRebuildsUpstreamRequest(t *testing.T) {
	h := newHarness(t, Config{}, nil)
	hostile := url.Values{
		"query":                {"error"},
		"extra_filters":        {`{kubernetes_namespace_name="tenant-b"}`},
		"extra_stream_filters": {`{kubernetes_namespace_name=~".*"}`},
		"AccountID":            {"9"},
	}
	headers := map[string]string{"AccountID": "9", "ProjectID": "9", "X-Forwarded-For": "1.2.3.4", "Cookie": "kc-access=secret; other=1"}

	resp := h.do(http.MethodGet, logsRoot+"query?"+hostile.Encode(), "good", nil, headers)
	_ = resp.Body.Close()
	resp = h.do(http.MethodPost, logsRoot+"query?extra_filters="+url.QueryEscape(`{x="y"}`), "good", hostile, headers)
	body, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK || string(body) != `{"_msg":"hello"}`+"\n" {
		t.Fatalf("POST answered %d %q", resp.StatusCode, body)
	}
	if ct := resp.Header.Get("Content-Type"); ct != "application/stream+json" {
		t.Fatalf("Content-Type = %q, want the upstream's", ct)
	}

	reqs := h.requests()
	if len(reqs) != 2 {
		t.Fatalf("upstream saw %d requests", len(reqs))
	}
	if reqs[0].form.Encode() != reqs[1].form.Encode() {
		t.Fatalf("GET and POST built different upstream requests:\n%v\n%v", reqs[0].form, reqs[1].form)
	}
	if n := h.kube.sars.Load(); n != 1 {
		t.Fatalf("SubjectAccessReviews = %d, want 1: POST must reuse the GET decision", n)
	}
	for _, r := range reqs {
		if r.path != "/select/logsql/query" {
			t.Errorf("upstream path = %s", r.path)
		}
		if got := r.form["extra_stream_filters"]; len(got) != 1 || got[0] != wantScope {
			t.Errorf("extra_stream_filters = %q", got)
		}
		if r.form.Has("extra_filters") || r.form.Has("AccountID") {
			t.Errorf("client scope parameter forwarded: %v", r.form)
		}
		if r.headers.Get("AccountID") != "0" || r.headers.Get("ProjectID") != "0" {
			t.Errorf("tenant headers = %q/%q, want 0/0", r.headers.Get("AccountID"), r.headers.Get("ProjectID"))
		}
		if ae := r.headers.Get("Accept-Encoding"); !strings.Contains(ae, "gzip") {
			t.Errorf("upstream query Accept-Encoding = %q, want gzip outside tails", ae)
		}
		for _, k := range []string{"Authorization", "X-Forwarded-For", "Cookie"} {
			if r.headers.Get(k) != "" {
				t.Errorf("client header %s forwarded", k)
			}
		}
	}
	for _, s := range h.stores {
		if s != "tenant-a-store" {
			t.Errorf("store = %s, want the resolved one", s)
		}
	}
}

func TestServerUpstreamErrors(t *testing.T) {
	h := newHarness(t, Config{}, func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "cannot parse query", http.StatusBadRequest)
	})
	if got := h.status(http.MethodGet, logsRoot+"query?query=|", "good"); got != http.StatusBadRequest {
		t.Fatalf("upstream 400 answered as %d", got)
	}
	h.upstream.Close()
	if got := h.status(http.MethodGet, logsRoot+"query", "good"); got != http.StatusBadGateway {
		t.Fatalf("unreachable upstream answered %d, want 502", got)
	}
}

func TestServerLimits(t *testing.T) {
	release := make(chan struct{})
	var once sync.Once
	t.Cleanup(func() { once.Do(func() { close(release) }) })
	h := newHarness(t, Config{Limiter: LimiterConfig{MaxConcurrentPerUser: 1}}, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.(http.Flusher).Flush()
		<-release
	})
	first := h.do(http.MethodGet, logsRoot+"query", "good", nil, nil)
	defer func() { _ = first.Body.Close() }()
	if got := h.status(http.MethodGet, logsRoot+"query", "good"); got != http.StatusTooManyRequests {
		t.Fatalf("concurrent request answered %d, want 429", got)
	}
	once.Do(func() { close(release) })

	h2 := newHarness(t, Config{Limiter: LimiterConfig{RatePerUser: 0.001, RateBurst: 1}}, nil)
	if got := h2.status(http.MethodGet, logsRoot+"query", "good"); got != http.StatusOK {
		t.Fatalf("first request answered %d", got)
	}
	limited := h2.do(http.MethodGet, logsRoot+"query", "good", nil, nil)
	_ = limited.Body.Close()
	if limited.StatusCode != http.StatusTooManyRequests || limited.Header.Get("Retry-After") != "1000" {
		t.Fatalf("rate-limited request answered %d with Retry-After %q, want 429 with 1000",
			limited.StatusCode, limited.Header.Get("Retry-After"))
	}
}

// tailUpstream sends one line per value received on lines and holds the
// stream open until done is closed.
func tailUpstream(lines <-chan string, done <-chan struct{}) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/stream+json")
		w.WriteHeader(http.StatusOK)
		w.(http.Flusher).Flush()
		for {
			select {
			case l := <-lines:
				_, _ = io.WriteString(w, l+"\n")
				w.(http.Flusher).Flush()
			case <-done:
				return
			case <-r.Context().Done():
				return
			}
		}
	}
}

func TestServerTailFlushesEveryLine(t *testing.T) {
	lines := make(chan string)
	done := make(chan struct{})
	t.Cleanup(func() { close(done) })
	h := newHarness(t, Config{TailIdleTimeout: time.Minute, RequestTimeout: 50 * time.Millisecond}, tailUpstream(lines, done))

	resp := h.do(http.MethodGet, logsRoot+"tail?query=*&since=2026-11-04T08:00:00Z", "good", nil, nil)
	defer func() { _ = resp.Body.Close() }()
	rd := bufio.NewReader(resp.Body)
	for i, l := range []string{`{"_msg":"one"}`, `{"_msg":"two"}`} {
		select {
		case lines <- l:
		case <-time.After(2 * time.Second):
			t.Fatalf("upstream tail closed before line %d", i)
		}
		got := make(chan string, 1)
		go func() {
			s, _ := rd.ReadString('\n')
			got <- s
		}()
		select {
		case s := <-got:
			if s != l+"\n" {
				t.Fatalf("line %d = %q", i, s)
			}
		case <-time.After(2 * time.Second):
			t.Fatalf("line %d not flushed to the client", i)
		}
		// Past the request timeout: a tail must not be cut by it.
		time.Sleep(60 * time.Millisecond)
	}
	if r := h.requests()[0]; r.path != "/select/logsql/tail" || r.form.Get("start_offset") != "900000ms" {
		t.Fatalf("tail upstream = %s %v, want start_offset capped at 15m", r.path, r.form)
	}
}

// TestServerTailHeadersBeforeFirstLine mimics vlselect, which holds even the
// response headers of a gzip-negotiated tail until the first line arrives.
func TestServerTailHeadersBeforeFirstLine(t *testing.T) {
	done := make(chan struct{})
	h := newHarness(t, Config{TailIdleTimeout: time.Minute}, func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.Header.Get("Accept-Encoding"), "gzip") {
			select {
			case <-done:
			case <-r.Context().Done():
			}
			return
		}
		tailUpstream(nil, done)(w, r)
	})
	// Registered after the harness so it runs before the servers close and
	// releases a held upstream when the assertion below fails.
	t.Cleanup(func() { close(done) })

	req, err := http.NewRequest(http.MethodGet, h.proxy.URL+logsRoot+"tail?query=*", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer good")
	got := make(chan *http.Response, 1)
	go func() {
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			close(got)
			return
		}
		got <- resp
	}()
	select {
	case resp, ok := <-got:
		if !ok {
			t.Fatal("tail request failed")
		}
		defer func() { _ = resp.Body.Close() }()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("status = %d, want 200", resp.StatusCode)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("tail response headers did not reach the client before the first line")
	}
	reqs := h.requests()
	if len(reqs) != 1 {
		t.Fatalf("upstream saw %d requests, want 1", len(reqs))
	}
	if ae := reqs[0].headers.Get("Accept-Encoding"); ae != "identity" {
		t.Fatalf("upstream tail Accept-Encoding = %q, want identity", ae)
	}
}

func TestServerTailCapsAndIdle(t *testing.T) {
	done := make(chan struct{})
	t.Cleanup(func() { close(done) })
	h := newHarness(t, Config{
		Limiter:         LimiterConfig{MaxTailsPerUser: 1, MaxTails: 2},
		TailIdleTimeout: 500 * time.Millisecond,
	}, tailUpstream(make(chan string), done))

	a := h.do(http.MethodGet, logsRoot+"tail", "good", nil, nil)
	defer func() { _ = a.Body.Close() }()
	if a.StatusCode != http.StatusOK {
		t.Fatalf("first tail answered %d", a.StatusCode)
	}
	if got := h.status(http.MethodGet, logsRoot+"tail", "good"); got != http.StatusTooManyRequests {
		t.Fatalf("second tail of one user answered %d, want 429", got)
	}
	b := h.do(http.MethodGet, logsRoot+"tail", "bob-token", nil, nil)
	defer func() { _ = b.Body.Close() }()
	if b.StatusCode != http.StatusOK {
		t.Fatalf("bob's tail answered %d", b.StatusCode)
	}
	if got := h.server.limits.openTails(); got != 2 {
		t.Fatalf("open tails = %d, want 2", got)
	}

	closed := make(chan error, 1)
	go func() {
		_, err := io.Copy(io.Discard, a.Body)
		closed <- err
	}()
	select {
	case <-closed:
	case <-time.After(3 * time.Second):
		t.Fatal("idle tail not closed")
	}
	deadline := time.Now().Add(2 * time.Second)
	for h.server.limits.openTails() > 1 {
		if time.Now().After(deadline) {
			t.Fatal("closed tail still holds its slot")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestServerUpstreamDeadline(t *testing.T) {
	release := make(chan struct{})
	t.Cleanup(func() { close(release) })
	h := newHarness(t, Config{RequestTimeout: 50 * time.Millisecond}, func(_ http.ResponseWriter, r *http.Request) {
		select {
		case <-release:
		case <-r.Context().Done():
		}
	})
	if got := h.status(http.MethodGet, logsRoot+"query", "good"); got != http.StatusGatewayTimeout {
		t.Fatalf("slow upstream answered %d, want 504", got)
	}
}

func TestServerTailLongLine(t *testing.T) {
	long := `{"_msg":"` + strings.Repeat("x", 3*tailChunkBytes) + `"}`
	h := newHarness(t, Config{}, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, long+"\n"+`{"_msg":"short"}`+"\n")
	})
	resp := h.do(http.MethodGet, logsRoot+"tail", "good", nil, nil)
	body, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if string(body) != long+"\n"+`{"_msg":"short"}`+"\n" {
		t.Fatalf("long line not streamed intact: got %d bytes", len(body))
	}
}

// The auth scheme is case-insensitive (RFC 9110, section 11.1), as
// kube-apiserver reads it.
func TestServerBearerSchemeCase(t *testing.T) {
	h := newHarness(t, Config{}, nil)
	for _, auth := range []string{"Bearer good", "bearer good", "BEARER good"} {
		resp := h.do(http.MethodGet, logsRoot+"query", "", nil, map[string]string{"Authorization": auth})
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Errorf("Authorization %q answered %d, want 200", auth, resp.StatusCode)
		}
	}
	resp := h.do(http.MethodGet, logsRoot+"query", "", nil, map[string]string{"Authorization": "Basic good"})
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("Basic scheme answered %d, want 401", resp.StatusCode)
	}
}
