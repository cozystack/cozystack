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
	"sort"
	"strings"
	"testing"
	"time"
)

var testNow = time.Date(2026, 11, 4, 12, 0, 0, 0, time.UTC)

func testLimits() LogsLimits {
	return LogsLimits{
		DefaultRange:  time.Hour,
		MaxRange:      7 * 24 * time.Hour,
		MaxBuckets:    1000,
		DefaultLimit:  1000,
		MaxLimit:      5000,
		MaxTailResume: 15 * time.Minute,
	}
}

const wantScope = `{kubernetes_namespace_name="tenant-a"}`

func build(t *testing.T, endpoint string, in url.Values) url.Values {
	t.Helper()
	out, err := BuildLogsParams(endpoint, in, "tenant-a", testNow, testLimits())
	if err != nil {
		t.Fatalf("BuildLogsParams(%s, %v) error: %v", endpoint, in, err)
	}
	return out
}

func buildErr(t *testing.T, endpoint string, in url.Values) *HTTPError {
	t.Helper()
	out, err := BuildLogsParams(endpoint, in, "tenant-a", testNow, testLimits())
	if err == nil {
		t.Fatalf("BuildLogsParams(%s, %v) = %v, want error", endpoint, in, out)
	}
	return err
}

func TestBuildLogsParamsAllowlist(t *testing.T) {
	allowed := map[string][]string{
		"query":               {"query", "start", "end", "limit"},
		"hits":                {"query", "start", "end", "step", "field", "fields_limit"},
		"stats_query_range":   {"query", "start", "end", "step"},
		"field_values":        {"query", "start", "end", "field", "limit", "filter"},
		"stream_field_values": {"query", "start", "end", "field", "limit", "filter"},
		"facets":              {"query", "start", "end", "limit", "max_values_per_field", "max_value_len", "keep_const_fields"},
		"tail":                {"query", "start", "end", "start_offset"},
	}
	hostile := url.Values{
		"extra_filters":        {`{kubernetes_namespace_name="tenant-b"}`},
		"extra_filters[]":      {`kubernetes_namespace_name:~".*"`},
		"extra_stream_filters": {`{kubernetes_namespace_name=~".*"}`},
		"AccountID":            {"7"},
		"ProjectID":            {"7"},
		"tenant":               {"tenant-b"},
		"offset":               {"1h"},
		"refresh_interval":     {"1ms"},
		"ignore_pipes":         {"1"},
		"format":               {"csv"},
		"timeout":              {"1h"},
		"query":                {"error"},
		"field":                {"kubernetes_pod_name"},
		"filter":               {"api"},
		"keep_const_fields":    {"1"},
		"since":                {"2026-11-04T11:59:00Z"},
	}
	for endpoint, keys := range allowed {
		t.Run(endpoint, func(t *testing.T) {
			out := build(t, endpoint, hostile)
			want := map[string]bool{"extra_stream_filters": true}
			for _, k := range keys {
				want[k] = true
			}
			for k := range out {
				if !want[k] {
					t.Errorf("forwarded non-allowlisted parameter %q", k)
				}
			}
			if got := out["extra_stream_filters"]; len(got) != 1 || got[0] != wantScope {
				t.Errorf("extra_stream_filters = %q, want exactly [%s]", got, wantScope)
			}
			if _, ok := out["extra_filters"]; ok {
				t.Error("extra_filters forwarded")
			}
		})
	}
}

func TestBuildLogsParamsRejects(t *testing.T) {
	tests := []struct {
		name     string
		endpoint string
		in       url.Values
		code     int
	}{
		{"unknown endpoint", "tenant_ids", url.Values{}, http.StatusNotFound},
		{"stats_query is not proxied", "stats_query", url.Values{}, http.StatusNotFound},
		{"ignore_global_time_filter", "query", url.Values{"query": {`* options(ignore_global_time_filter=true)`}}, http.StatusBadRequest},
		{"ignore_global_time_filter in subquery", "hits", url.Values{"query": {`* | union (options(ignore_global_time_filter=true) error)`}}, http.StatusBadRequest},
		{"ignore_global_time_filter on tail", "tail", url.Values{"query": {`options(IGNORE_GLOBAL_TIME_FILTER=true) *`}}, http.StatusBadRequest},
		{"options with a quoted escaped name", "query", url.Values{"query": {`options("ignore_global_time_filte\x72"=true) *`}}, http.StatusBadRequest},
		{"options with a comment inside the name", "query", url.Values{"query": {"options(ignore_global#c\n_time_filter=true) *"}}, http.StatusBadRequest},
		{"options after a comment and unicode space", "tail", url.Values{"query": {"OPTIONS #c\n\u00a0(concurrency=1000) *"}}, http.StatusBadRequest},
		{"inverted range", "query", url.Values{"start": {"2026-11-04T11:00:00Z"}, "end": {"2026-11-04T10:00:00Z"}}, http.StatusBadRequest},
		{"inverted range on tail", "tail", url.Values{"start": {"2026-11-04T11:00:00Z"}, "end": {"2026-11-04T10:00:00Z"}}, http.StatusBadRequest},
		{"bad start", "query", url.Values{"start": {"yesterday"}}, http.StatusBadRequest},
		{"millisecond timestamp", "query", url.Values{"start": {"1793786400000"}}, http.StatusBadRequest},
		{"year-like number", "query", url.Values{"start": {"2026"}}, http.StatusBadRequest},
		{"exponent time", "query", url.Values{"start": {"1.7937864e9"}}, http.StatusBadRequest},
		{"signed time", "query", url.Values{"start": {"+1793786400"}}, http.StatusBadRequest},
		{"exponent duration", "query", url.Values{"start": {"3e2"}}, http.StatusBadRequest},
		{"trailing dot", "query", url.Values{"start": {"300."}}, http.StatusBadRequest},
		{"infinite time", "query", url.Values{"end": {"Inf"}}, http.StatusBadRequest},
		{"NaN time", "query", url.Values{"start": {"NaN"}}, http.StatusBadRequest},
		{"NaN step", "hits", url.Values{"step": {"NaN"}}, http.StatusBadRequest},
		{"NaN start_offset", "tail", url.Values{"start_offset": {"NaN"}}, http.StatusBadRequest},
		{"bad limit", "query", url.Values{"limit": {"-1"}}, http.StatusBadRequest},
		{"zero limit", "query", url.Values{"limit": {"0"}}, http.StatusBadRequest},
		{"bad step", "hits", url.Values{"step": {"0s"}}, http.StatusBadRequest},
		{"field required", "field_values", url.Values{}, http.StatusBadRequest},
		{"too many hits fields", "hits", url.Values{"field": strings.Fields("a b c d e f g h i j k")}, http.StatusBadRequest},
		{"since and start_offset", "tail", url.Values{"since": {"2026-11-04T11:59:00Z"}, "start_offset": {"1m"}}, http.StatusBadRequest},
		{"bad since", "tail", url.Values{"since": {"soon"}}, http.StatusBadRequest},
		{"start_offset summing past int64", "tail", url.Values{"start_offset": {"99999999999h99999999999h1s"}}, http.StatusBadRequest},
		{"step summing past int64", "hits", url.Values{"step": {"2562047h2562047h"}}, http.StatusBadRequest},
		{"step token past int64", "hits", url.Values{"step": {"99999999999h"}}, http.StatusBadRequest},
		{"bare-seconds step past int64", "hits", url.Values{"step": {"99999999999999999999"}}, http.StatusBadRequest},
		{"relative start summing past int64", "query", url.Values{"start": {"106751d1d"}}, http.StatusBadRequest},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := buildErr(t, tt.endpoint, tt.in); err.Code != tt.code {
				t.Fatalf("code = %d (%s), want %d", err.Code, err.Message, tt.code)
			}
		})
	}
}

// VictoriaLogs propagates extra_stream_filters into every subquery
// (docs.victoriametrics.com/victorialogs/querying/), so union and in()
// queries are forwarded verbatim with the scope beside them.
func TestBuildLogsParamsSubqueriesKeepScope(t *testing.T) {
	for _, q := range []string{
		`* | union ({kubernetes_namespace_name="tenant-b"})`,
		`_stream_id:in({kubernetes_namespace_name="tenant-b"} | fields _stream_id)`,
	} {
		out := build(t, "query", url.Values{"query": {q}})
		if out.Get("query") != q || out.Get("extra_stream_filters") != wantScope {
			t.Errorf("query %q forwarded as %v", q, out)
		}
	}
}

func TestBuildLogsParamsWindow(t *testing.T) {
	ts := func(s string) string {
		v, err := time.Parse(time.RFC3339, s)
		if err != nil {
			t.Fatal(err)
		}
		return v.UTC().Format(time.RFC3339Nano)
	}
	tests := []struct {
		name               string
		in                 url.Values
		wantStart, wantEnd string
	}{
		{"defaults", url.Values{}, ts("2026-11-04T11:00:00Z"), ts("2026-11-04T12:00:00Z")},
		{"start only", url.Values{"start": {"2026-11-04T09:00:00Z"}}, ts("2026-11-04T09:00:00Z"), ts("2026-11-04T12:00:00Z")},
		{"end only", url.Values{"end": {"2026-11-03T00:00:00Z"}}, ts("2026-11-02T23:00:00Z"), ts("2026-11-03T00:00:00Z")},
		{"unix seconds", url.Values{"start": {"1793786400"}, "end": {"1793788200.5"}}, "2026-11-04T10:00:00Z", "2026-11-04T10:30:00.5Z"},
		{"rfc3339 with offset", url.Values{"start": {"2026-11-04T13:00:00+03:00"}}, ts("2026-11-04T10:00:00Z"), ts("2026-11-04T12:00:00Z")},
		{"relative", url.Values{"start": {"3h"}, "end": {"now"}}, ts("2026-11-04T09:00:00Z"), ts("2026-11-04T12:00:00Z")},
		{"relative days", url.Values{"start": {"1d"}}, ts("2026-11-03T12:00:00Z"), ts("2026-11-04T12:00:00Z")},
		{"over-wide clamped", url.Values{"start": {"2026-01-01T00:00:00Z"}}, ts("2026-10-28T12:00:00Z"), ts("2026-11-04T12:00:00Z")},
		{"empty range allowed", url.Values{"start": {"2026-11-04T10:00:00Z"}, "end": {"2026-11-04T10:00:00Z"}}, ts("2026-11-04T10:00:00Z"), ts("2026-11-04T10:00:00Z")},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			for _, endpoint := range []string{"query", "field_values", "facets"} {
				in := url.Values{"field": {"f"}}
				for k, v := range tt.in {
					in[k] = v
				}
				out := build(t, endpoint, in)
				if out.Get("start") != tt.wantStart || out.Get("end") != tt.wantEnd {
					t.Errorf("%s: window = [%s, %s], want [%s, %s]", endpoint, out.Get("start"), out.Get("end"), tt.wantStart, tt.wantEnd)
				}
			}
		})
	}
}

func TestBuildLogsParamsStepAndLimit(t *testing.T) {
	day := url.Values{"start": {"2026-11-03T12:00:00Z"}}
	with := func(base url.Values, k, v string) url.Values {
		c := url.Values{}
		for key, vals := range base {
			c[key] = vals
		}
		c.Set(k, v)
		return c
	}
	// 24h over at most 1000 buckets is 86.4s.
	if got := build(t, "hits", with(day, "step", "1s")).Get("step"); got != "86400ms" {
		t.Errorf("tiny step = %s, want clamped to 86400ms", got)
	}
	if got := build(t, "stats_query_range", with(day, "step", "1h")).Get("step"); got != "3600000ms" {
		t.Errorf("1h step = %s", got)
	}
	if got := build(t, "stats_query_range", with(day, "step", "1w")).Get("step"); got != "86400000ms" {
		t.Errorf("1w step on a day window = %s, want capped at the window", got)
	}
	if got := build(t, "hits", with(day, "step", "3600")).Get("step"); got != "3600000ms" {
		t.Errorf("bare-seconds step = %s, want 3600000ms", got)
	}
	if got := build(t, "hits", with(day, "step", "5400.5")).Get("step"); got != "5400500ms" {
		t.Errorf("fractional bare-seconds step = %s, want 5400500ms", got)
	}
	if got := build(t, "hits", day).Get("step"); got != "86400ms" {
		t.Errorf("omitted step = %s, want the bucket-bounded minimum", got)
	}
	if got := build(t, "hits", url.Values{"start": {"2026-11-04T11:59:59Z"}}).Get("step"); got != "1000ms" {
		t.Errorf("omitted step on a tiny window = %s, want 1000ms floor", got)
	}

	if got := build(t, "query", url.Values{}).Get("limit"); got != "1000" {
		t.Errorf("default limit = %s", got)
	}
	if got := build(t, "query", url.Values{"limit": {"10"}}).Get("limit"); got != "10" {
		t.Errorf("limit = %s", got)
	}
	for _, endpoint := range []string{"query", "field_values", "facets"} {
		in := url.Values{"limit": {"1000000"}, "field": {"f"}}
		if got := build(t, endpoint, in).Get("limit"); got != "5000" {
			t.Errorf("%s: limit = %s, want clamped to 5000", endpoint, got)
		}
	}
	facets := build(t, "facets", url.Values{"max_values_per_field": {"99999"}, "max_value_len": {"99999"}})
	if facets.Get("max_values_per_field") != "5000" || facets.Get("max_value_len") != "5000" {
		t.Errorf("facets caps not clamped: %v", facets)
	}
	hits := build(t, "hits", url.Values{"field": {"b", "a"}, "fields_limit": {"99999"}})
	fields := hits["field"]
	sort.Strings(fields)
	if strings.Join(fields, ",") != "a,b" || hits.Get("fields_limit") != "5000" {
		t.Errorf("hits fields = %v", hits)
	}
}

func TestBuildLogsParamsTail(t *testing.T) {
	tests := []struct {
		name string
		in   url.Values
		want string
	}{
		{"live only", url.Values{}, ""},
		// now - 5s tail offset - since = 55s
		{"since within cap", url.Values{"since": {"2026-11-04T11:59:00Z"}}, "55000ms"},
		{"since older than cap", url.Values{"since": {"2026-11-04T08:00:00Z"}}, "900000ms"},
		{"since in the future", url.Values{"since": {"2026-11-04T13:00:00Z"}}, "0ms"},
		{"start_offset within cap", url.Values{"start_offset": {"2m"}}, "120000ms"},
		{"start_offset over cap", url.Values{"start_offset": {"30d"}}, "900000ms"},
		{"start_offset in bare seconds", url.Values{"start_offset": {"120"}}, "120000ms"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			out := build(t, "tail", tt.in)
			if got := out.Get("start_offset"); got != tt.want {
				t.Errorf("start_offset = %q, want %q", got, tt.want)
			}
			if out.Has("start") || out.Has("end") || out.Has("limit") {
				t.Errorf("tail got a derived window or limit: %v", out)
			}
		})
	}
	out := build(t, "tail", url.Values{"start": {"2026-11-04T11:00:00Z"}})
	if out.Get("start") != "2026-11-04T11:00:00Z" {
		t.Errorf("tail start = %q, want the client value ANDed into each tick", out.Get("start"))
	}
}

// alignedWindow repeats vlselect's alignStartEndToStep (offset 0): with a
// step it widens [start, end] to step boundaries, end always by at least
// one more step.
func alignedWindow(t *testing.T, out url.Values) time.Duration {
	t.Helper()
	parse := func(k string) int64 {
		v, err := time.Parse(time.RFC3339Nano, out.Get(k))
		if err != nil {
			t.Fatalf("%s: %v", k, err)
		}
		return v.UnixNano()
	}
	step, err := parseDuration(out.Get("step"))
	if err != nil {
		t.Fatal(err)
	}
	start, end, st := parse("start"), parse("end"), int64(step)
	start -= start % st
	end += st - end%st
	return time.Duration(end - start)
}

func TestBuildLogsParamsStepCannotWidenWindow(t *testing.T) {
	lim := testLimits()
	for _, endpoint := range []string{"hits", "stats_query_range"} {
		for _, in := range []url.Values{
			{"step": {"100y"}},
			{"step": {"100y"}, "start": {"2026-01-01T00:00:00Z"}},
			{"step": {"3d"}, "start": {"7d"}},
			{"start": {"7d"}},
			{"step": {"1s"}, "start": {"7d"}},
		} {
			out := build(t, endpoint, in)
			if got := alignedWindow(t, out); got > lim.MaxRange {
				t.Errorf("%s %v: vlselect would scan %s, more than %s", endpoint, in, got, lim.MaxRange)
			}
		}
	}
}

func TestBuildLogsParamsStepKeepsBucketCap(t *testing.T) {
	lim := testLimits()
	// Spans that MaxBuckets does not divide into whole milliseconds.
	for _, start := range []string{"2026-11-03T12:00:00.0007Z", "2026-11-04T10:00:00.0013Z", "2026-11-04T11:00:00.0005Z"} {
		out := build(t, "hits", url.Values{"start": {start}})
		step, err := parseDuration(out.Get("step"))
		if err != nil {
			t.Fatal(err)
		}
		s, _ := time.Parse(time.RFC3339Nano, out.Get("start"))
		e, _ := time.Parse(time.RFC3339Nano, out.Get("end"))
		if buckets := (e.Sub(s) + step - 1) / step; int(buckets) > lim.MaxBuckets {
			t.Errorf("start %s: step %s gives %d buckets, more than %d", start, out.Get("step"), buckets, lim.MaxBuckets)
		}
	}
}

func TestParseDurationBounds(t *testing.T) {
	for _, in := range []string{"2562047h", "106751d", "9223372036s", "292y"} {
		if d, err := parseDuration(in); err != nil || d <= 0 {
			t.Errorf("parseDuration(%q) = %s, %v; want a positive duration", in, d, err)
		}
	}
	for _, in := range []string{"2562048h", "106751d1d", "9223372037s", "9223372037", "293y", "1y292y"} {
		if d, err := parseDuration(in); err == nil {
			t.Errorf("parseDuration(%q) = %s; want an error past the int64 range", in, d)
		}
	}
}
