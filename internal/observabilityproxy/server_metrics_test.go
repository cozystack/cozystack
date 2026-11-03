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
	"io"
	"net/http"
	"net/url"
	"sync"
	"testing"
	"time"

	authorizationv1 "k8s.io/api/authorization/v1"
)

const metricsRoot = "/apis/cozystack.io/v1alpha1/namespaces/tenant-a/metrics/"

func promUpstream(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	_, _ = io.WriteString(w, `{"status":"success","data":{"resultType":"vector","result":[]}}`)
}

func TestServerMetricsRebuildsUpstreamRequest(t *testing.T) {
	h := newHarness(t, Config{}, promUpstream)
	hostile := url.Values{
		"query":           {"sum(rate(container_cpu_usage_seconds_total[5m]))"},
		"start":           {unix(testNow.Add(-time.Hour))},
		"end":             {unix(testNow)},
		"step":            {"60"},
		"extra_filters":   {`{namespace=~".+"}`},
		"extra_filters[]": {`{namespace="tenant-b"}`, `{namespace=~".*"}`},
		"extra_label":     {"namespace=tenant-b"},
		"nocache":         {"1"},
	}
	headers := map[string]string{"AccountID": "9", "X-Forwarded-For": "1.2.3.4", "Cookie": "kc-access=secret"}

	resp := h.do(http.MethodGet, metricsRoot+"api/v1/query_range?"+hostile.Encode(), "good", nil, headers)
	_ = resp.Body.Close()
	resp = h.do(http.MethodPost, metricsRoot+"api/v1/query_range?extra_filters[]="+url.QueryEscape(`{namespace="x"}`), "good", hostile, headers)
	body, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK || resp.Header.Get("Content-Type") != "application/json" {
		t.Fatalf("POST answered %d %s %q", resp.StatusCode, resp.Header.Get("Content-Type"), body)
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
	want := authorizationv1.ResourceAttributes{Verb: "list", Group: "cozystack.io", Resource: "metrics", Namespace: "tenant-a"}
	if got := h.kube.lastSAR.Load().ResourceAttributes; *got != want {
		t.Fatalf("SubjectAccessReview = %+v, want %+v", *got, want)
	}
	for _, r := range reqs {
		if r.path != "/api/v1/query_range" {
			t.Errorf("upstream path = %s", r.path)
		}
		if got := r.form["extra_filters[]"]; len(got) != 1 || got[0] != wantMetricsScope {
			t.Errorf("extra_filters[] = %q", got)
		}
		if r.form.Has("extra_filters") || r.form.Has("extra_label") || r.form.Has("nocache") {
			t.Errorf("client parameter forwarded: %v", r.form)
		}
		for _, k := range []string{"Authorization", "AccountID", "X-Forwarded-For", "Cookie"} {
			if r.headers.Get(k) != "" {
				t.Errorf("client header %s forwarded", k)
			}
		}
	}
}

func TestServerMetricsStoreByRange(t *testing.T) {
	h := newHarness(t, Config{}, promUpstream)
	for _, path := range []string{
		"api/v1/query?query=up",
		"api/v1/query_range?query=up&step=600&start=" + unix(testNow.Add(-7*24*time.Hour)) + "&end=" + unix(testNow),
		"api/v1/label/pod/values",
	} {
		if got := h.status(http.MethodGet, metricsRoot+path, "good"); got != http.StatusOK {
			t.Fatalf("%s answered %d", path, got)
		}
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if want := []string{"shortterm", "longterm", "shortterm"}; len(h.stores) != 3 || h.stores[0] != want[0] || h.stores[1] != want[1] || h.stores[2] != want[2] {
		t.Fatalf("stores = %v, want %v", h.stores, want)
	}
	if r := h.got[2]; r.path != "/api/v1/label/pod/values" || r.form.Get("match[]") != wantMetricsScope {
		t.Fatalf("label values upstream = %s %v", r.path, r.form)
	}
}

func TestServerMetricsStatusCodes(t *testing.T) {
	h := newHarness(t, Config{}, promUpstream)
	tooLong := "api/v1/query_range?query=up&step=3600&start=" + unix(testNow.Add(-31*24*time.Hour)) + "&end=" + unix(testNow)
	if got := h.status(http.MethodGet, metricsRoot+tooLong, "good"); got != http.StatusUnprocessableEntity {
		t.Errorf("range above the limit answered %d, want 422", got)
	}
	if got := h.status(http.MethodGet, metricsRoot+"api/v1/series", "good"); got != http.StatusBadRequest {
		t.Errorf("series without match[] answered %d, want 400", got)
	}
	if got := h.status(http.MethodPut, metricsRoot+"api/v1/query?query=up", "good"); got != http.StatusMethodNotAllowed {
		t.Errorf("PUT answered %d, want 405", got)
	}
	if n := len(h.requests()); n != 0 {
		t.Errorf("upstream saw %d requests for refused ones", n)
	}

	h2 := newHarness(t, Config{}, func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, `{"status":"error","errorType":"422"}`, http.StatusUnprocessableEntity)
	})
	if got := h2.status(http.MethodGet, metricsRoot+"api/v1/query?query=up", "good"); got != http.StatusUnprocessableEntity {
		t.Errorf("upstream 422 answered as %d", got)
	}
	h2.upstream.Close()
	if got := h2.status(http.MethodGet, metricsRoot+"api/v1/query?query=up", "good"); got != http.StatusBadGateway {
		t.Errorf("unreachable upstream answered %d, want 502", got)
	}

	release := make(chan struct{})
	t.Cleanup(func() { close(release) })
	h3 := newHarness(t, Config{RequestTimeout: 50 * time.Millisecond}, func(_ http.ResponseWriter, r *http.Request) {
		select {
		case <-release:
		case <-r.Context().Done():
		}
	})
	if got := h3.status(http.MethodGet, metricsRoot+"api/v1/query?query=up", "good"); got != http.StatusGatewayTimeout {
		t.Errorf("slow upstream answered %d, want 504", got)
	}

	h4 := newHarness(t, Config{Limiter: LimiterConfig{RatePerUser: 0.001, RateBurst: 1}}, promUpstream)
	h4.status(http.MethodGet, metricsRoot+"api/v1/query?query=up", "good")
	limited := h4.do(http.MethodGet, metricsRoot+"api/v1/query?query=up", "good", nil, nil)
	_ = limited.Body.Close()
	if limited.StatusCode != http.StatusTooManyRequests || limited.Header.Get("Retry-After") != "1000" {
		t.Errorf("rate-limited request answered %d with Retry-After %q, want 429 with 1000",
			limited.StatusCode, limited.Header.Get("Retry-After"))
	}
}

func TestServerMetricsConcurrency(t *testing.T) {
	entered, release := make(chan struct{}, 3), make(chan struct{})
	h := newHarness(t, Config{Limiter: LimiterConfig{MaxConcurrentPerUser: 1}}, func(w http.ResponseWriter, _ *http.Request) {
		entered <- struct{}{}
		<-release
		promUpstream(w, nil)
	})
	// Registered after the harness so it runs before the servers close.
	var once sync.Once
	t.Cleanup(func() { once.Do(func() { close(release) }) })
	held := make(chan int, 2)
	hold := func(token string) {
		go func() { held <- h.status(http.MethodGet, metricsRoot+"api/v1/query?query=up", token) }()
		select {
		case <-entered:
		case <-time.After(2 * time.Second):
			t.Fatalf("request of %s did not reach the upstream", token)
		}
	}
	hold("good")
	refused := make(chan int, 1)
	go func() { refused <- h.status(http.MethodPost, metricsRoot+"api/v1/query?query=up", "good") }()
	select {
	case got := <-refused:
		if got != http.StatusTooManyRequests {
			t.Fatalf("concurrent request answered %d, want 429", got)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("concurrent request of the same user reached the upstream")
	}
	hold("bob-token")
	once.Do(func() { close(release) })
	for range 2 {
		if got := <-held; got != http.StatusOK {
			t.Fatalf("held request answered %d", got)
		}
	}
}

func TestDefaultMetricsUpstreamURL(t *testing.T) {
	s := NewServer(Config{ClusterDomain: "cozy.local", Metrics: MetricsConfig{RootTenant: "tenant-root"}}, nil, nil, nil)
	if got, want := s.cfg.Metrics.UpstreamURL("shortterm"), "http://vmselect-shortterm.tenant-root.svc.cozy.local:8481/select/0/prometheus"; got != want {
		t.Fatalf("upstream = %s, want %s", got, want)
	}
}
