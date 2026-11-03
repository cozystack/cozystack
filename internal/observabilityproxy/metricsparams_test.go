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
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"
)

func testMetricsLimits() MetricsLimits {
	return MetricsLimits{MaxRange: 30 * 24 * time.Hour, MaxPoints: 11000, MaxTimeout: 30 * time.Second}
}

const wantMetricsScope = `{namespace="tenant-a"}`

func unix(t time.Time) string { return strconv.FormatInt(t.Unix(), 10) }

func buildMetrics(t *testing.T, endpoint string, in url.Values) metricsRequest {
	t.Helper()
	req, err := BuildMetricsParams(endpoint, in, "tenant-a", testNow, testMetricsLimits())
	if err != nil {
		t.Fatalf("BuildMetricsParams(%s, %v): %v", endpoint, in, err)
	}
	return req
}

func metricsStatus(endpoint string, in url.Values) int {
	_, err := BuildMetricsParams(endpoint, in, "tenant-a", testNow, testMetricsLimits())
	if err == nil {
		return http.StatusOK
	}
	return err.Code
}

func TestBuildMetricsParamsAllowlist(t *testing.T) {
	hostile := url.Values{
		"extra_filters":   {`{namespace=~".+"}`},
		"extra_filters[]": {`{namespace="tenant-b"}`, `{namespace=~".*"}`},
		"extra_label":     {"namespace=tenant-b"},
		"match":           {`{namespace="tenant-b"}`},
		"nocache":         {"1"},
		"limit":           {"1"},
		"AccountID":       {"7"},
		"latency_offset":  {"1h"},
		"round_digits":    {"2"},
	}
	cases := map[string]url.Values{
		"api/v1/query":            {"query": {"up"}, "time": {unix(testNow)}, "step": {"30s"}},
		"api/v1/query_range":      {"query": {"up"}, "start": {unix(testNow.Add(-time.Hour))}, "end": {unix(testNow)}, "step": {"60"}},
		"api/v1/series":           {"match[]": {"up"}},
		"api/v1/labels":           {"match[]": {"up"}},
		"api/v1/label/pod/values": {"match[]": {"up"}},
	}
	allowed := map[string]bool{"query": true, "start": true, "end": true, "step": true, "time": true, "match[]": true, "timeout": true, "extra_filters[]": true}
	for endpoint, in := range cases {
		t.Run(endpoint, func(t *testing.T) {
			for k, v := range hostile {
				in[k] = v
			}
			req := buildMetrics(t, endpoint, in)
			if req.path != endpoint {
				t.Errorf("path = %q", req.path)
			}
			for k := range req.params {
				if !allowed[k] {
					t.Errorf("parameter %q forwarded: %v", k, req.params)
				}
			}
			if got := req.params["extra_filters[]"]; len(got) != 1 || got[0] != wantMetricsScope {
				t.Errorf("extra_filters[] = %q, want exactly [%s]", got, wantMetricsScope)
			}
			for _, m := range req.params["match[]"] {
				if strings.Contains(m, "tenant-b") {
					t.Errorf("client match without brackets forwarded as match[]: %q", m)
				}
			}
		})
	}
}

func TestNamespaceSelectorEscapes(t *testing.T) {
	for _, ns := range []string{"tenant-a", `a"} or {namespace=~".+`, `back\slash`, "new\nline"} {
		sel := namespaceSelector(ns)
		lit, ok := strings.CutPrefix(sel, "{namespace=")
		lit, ok2 := strings.CutSuffix(lit, "}")
		if !ok || !ok2 {
			t.Fatalf("selector %q has the wrong shape", sel)
		}
		// MetricsQL reads a double-quoted label value with strconv.Unquote
		// (metricsql extractStringValue).
		got, err := strconv.Unquote(lit)
		if err != nil || got != ns {
			t.Errorf("selector %q decodes to %q, %v; want %q", sel, got, err, ns)
		}
	}
}

func TestBuildMetricsParamsLabelEndpointsAlwaysMatch(t *testing.T) {
	for _, endpoint := range []string{"api/v1/labels", "api/v1/label/__name__/values"} {
		req := buildMetrics(t, endpoint, url.Values{})
		if got := req.params["match[]"]; len(got) != 1 || got[0] != wantMetricsScope {
			t.Errorf("%s without match[] sends %q, want the namespace selector", endpoint, got)
		}
		req = buildMetrics(t, endpoint, url.Values{"match[]": {"up", `{job="x"}`}})
		if got := req.params["match[]"]; len(got) != 2 || got[0] != "up" || got[1] != `{job="x"}` {
			t.Errorf("%s client match[] = %q, want them kept", endpoint, got)
		}
	}
	if got := metricsStatus("api/v1/series", url.Values{}); got != http.StatusBadRequest {
		t.Errorf("series without match[] = %d, want 400", got)
	}
	if got := metricsStatus("api/v1/series", url.Values{"match[]": {""}}); got != http.StatusBadRequest {
		t.Errorf("empty match[] = %d, want 400", got)
	}
	many := url.Values{}
	for i := range maxMetricsMatchers + 1 {
		many.Add("match[]", "up"+strconv.Itoa(i))
	}
	if got := metricsStatus("api/v1/labels", many); got != http.StatusUnprocessableEntity {
		t.Errorf("%d match[] = %d, want 422", maxMetricsMatchers+1, got)
	}
}

func TestBuildMetricsParamsLimits(t *testing.T) {
	ago := func(d time.Duration) string { return unix(testNow.Add(-d)) }
	day := 24 * time.Hour
	rng := func(start, end, step string) url.Values {
		return url.Values{"query": {"up"}, "start": {start}, "end": {end}, "step": {step}}
	}
	tests := []struct {
		name     string
		endpoint string
		in       url.Values
		want     int
	}{
		{"range at the limit", "api/v1/query_range", rng(ago(30*day), ago(0), "1h"), http.StatusOK},
		{"range above the limit", "api/v1/query_range", rng(ago(31*day), ago(0), "1h"), http.StatusUnprocessableEntity},
		{"points at the limit", "api/v1/query_range", rng(ago(10999*time.Minute), ago(0), "1m"), http.StatusOK},
		{"points above the limit", "api/v1/query_range", rng(ago(11000*time.Minute), ago(0), "1m"), http.StatusUnprocessableEntity},
		{"points above the limit after rounding the step up", "api/v1/query_range", rng(ago(22*time.Second), ago(0), "0.0019"), http.StatusUnprocessableEntity},
		{"step above the range limit", "api/v1/query_range", rng(ago(time.Hour), ago(0), "31d"), http.StatusUnprocessableEntity},
		{"zero step", "api/v1/query_range", rng(ago(time.Hour), ago(0), "0"), http.StatusBadRequest},
		{"sub-millisecond step", "api/v1/query_range", rng(ago(time.Hour), ago(0), "0.0001"), http.StatusBadRequest},
		{"missing step", "api/v1/query_range", url.Values{"query": {"up"}, "start": {ago(time.Hour)}, "end": {ago(0)}}, http.StatusBadRequest},
		{"missing start", "api/v1/query_range", url.Values{"query": {"up"}, "end": {ago(0)}, "step": {"1m"}}, http.StatusBadRequest},
		{"missing query", "api/v1/query_range", url.Values{"start": {ago(time.Hour)}, "end": {ago(0)}, "step": {"1m"}}, http.StatusBadRequest},
		{"inverted range", "api/v1/query_range", rng(ago(0), ago(time.Hour), "1m"), http.StatusBadRequest},
		{"millisecond timestamp", "api/v1/query_range", rng(ago(time.Hour)+"000", ago(0)+"000", "1m"), http.StatusBadRequest},
		{"relative time", "api/v1/query_range", rng("-1h", "now", "1m"), http.StatusBadRequest},
		{"rfc3339", "api/v1/query_range", rng("2026-11-04T11:00:00Z", "2026-11-04T12:00:00.5Z", "15s"), http.StatusOK},
		{"instant without time", "api/v1/query", url.Values{"query": {"up"}}, http.StatusOK},
		{"instant step above the range limit", "api/v1/query", url.Values{"query": {"up"}, "step": {"31d"}}, http.StatusUnprocessableEntity},
		{"instant bad time", "api/v1/query", url.Values{"query": {"up"}, "time": {"yesterday"}}, http.StatusBadRequest},
		{"negative timeout", "api/v1/query", url.Values{"query": {"up"}, "timeout": {"-1s"}}, http.StatusBadRequest},
		{"series range above the limit", "api/v1/series", url.Values{"match[]": {"up"}, "start": {ago(31 * day)}}, http.StatusUnprocessableEntity},
		{"labels inverted range", "api/v1/labels", url.Values{"start": {ago(0)}, "end": {ago(time.Hour)}}, http.StatusBadRequest},
		{"invalid label name", "api/v1/label/a-b/values", url.Values{}, http.StatusBadRequest},
		{"unknown endpoint", "api/v1/status/tsdb", url.Values{}, http.StatusNotFound},
		{"export", "api/v1/export", url.Values{"match[]": {"up"}}, http.StatusNotFound},
		{"label without values", "api/v1/label/pod", url.Values{}, http.StatusNotFound},
		{"root", "", url.Values{}, http.StatusNotFound},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := metricsStatus(tt.endpoint, tt.in); got != tt.want {
				t.Fatalf("status = %d, want %d", got, tt.want)
			}
		})
	}
}

func TestBuildMetricsParamsCanonicalForms(t *testing.T) {
	req := buildMetrics(t, "api/v1/query_range", url.Values{
		"query": {"up"}, "start": {"2026-11-04T11:00:00.25Z"}, "end": {unix(testNow)}, "step": {"1m30s"},
	})
	want := url.Values{
		"query": {"up"}, "start": {"1793790000.250"}, "end": {"1793793600.000"}, "step": {"90"},
		"timeout": {"30"}, "extra_filters[]": {wantMetricsScope},
	}
	if req.params.Encode() != want.Encode() {
		t.Errorf("params = %v\nwant     %v", req.params, want)
	}

	// vmselect drops a sub-millisecond remainder; the points check has to
	// see the step it will use.
	for in, want := range map[string]string{"60.0001": "60.001", "0.0019": "0.002"} {
		req = buildMetrics(t, "api/v1/query_range", url.Values{"query": {"up"}, "start": {unix(testNow.Add(-20 * time.Second))}, "end": {unix(testNow)}, "step": {in}})
		if got := req.params.Get("step"); got != want {
			t.Errorf("step %s forwarded as %q, want %s", in, got, want)
		}
	}

	for in, want := range map[string]string{"": "30", "10s": "10", "5m": "30", "1.5": "1.5"} {
		v := url.Values{"query": {"up"}}
		if in != "" {
			v.Set("timeout", in)
		}
		if got := buildMetrics(t, "api/v1/query", v).params.Get("timeout"); got != want {
			t.Errorf("timeout %q forwarded as %q, want %q", in, got, want)
		}
	}

	req = buildMetrics(t, "api/v1/labels", url.Values{})
	if req.params.Get("end") != "1793793600.000" || req.params.Get("start") != "1793793300.000" {
		t.Errorf("labels window = %s..%s, want the last 5m", req.params.Get("start"), req.params.Get("end"))
	}
	if q := buildMetrics(t, "api/v1/query", url.Values{"query": {"up"}}); q.params.Has("time") || !q.start.Equal(testNow) {
		t.Errorf("instant query without time: params %v, start %v", q.params, q.start)
	}
}

func TestMetricsStoreByRange(t *testing.T) {
	cfg := MetricsConfig{ShortTermStore: "shortterm", LongTermStore: "longterm", ShortTermRetention: 72 * time.Hour}
	tests := []struct {
		name     string
		endpoint string
		in       url.Values
		want     string
	}{
		{"instant now", "api/v1/query", url.Values{"query": {"up"}}, "shortterm"},
		{"instant within retention", "api/v1/query", url.Values{"query": {"up"}, "time": {unix(testNow.Add(-71 * time.Hour))}}, "shortterm"},
		{"instant past retention", "api/v1/query", url.Values{"query": {"up"}, "time": {unix(testNow.Add(-73 * time.Hour))}}, "longterm"},
		{"live graph", "api/v1/query_range", url.Values{"query": {"up"}, "start": {unix(testNow.Add(-6 * time.Hour))}, "end": {unix(testNow)}, "step": {"60"}}, "shortterm"},
		{"range fits exactly", "api/v1/query_range", url.Values{"query": {"up"}, "start": {unix(testNow.Add(-72 * time.Hour))}, "end": {unix(testNow)}, "step": {"600"}}, "shortterm"},
		{"range past retention", "api/v1/query_range", url.Values{"query": {"up"}, "start": {unix(testNow.Add(-7 * 24 * time.Hour))}, "end": {unix(testNow)}, "step": {"600"}}, "longterm"},
		{"old window ending long ago", "api/v1/query_range", url.Values{"query": {"up"}, "start": {unix(testNow.Add(-5 * 24 * time.Hour))}, "end": {unix(testNow.Add(-4 * 24 * time.Hour))}, "step": {"600"}}, "longterm"},
		{"labels default window", "api/v1/labels", url.Values{}, "shortterm"},
		{"series past retention", "api/v1/series", url.Values{"match[]": {"up"}, "start": {unix(testNow.Add(-4 * 24 * time.Hour))}}, "longterm"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := buildMetrics(t, tt.endpoint, tt.in)
			if got := cfg.store(req.start, testNow); got != tt.want {
				t.Fatalf("store = %s, want %s", got, tt.want)
			}
		})
	}
}
