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
	"math"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"
)

// vmselectTimeMillis repeats how vmselect v1.136.0-cluster reads a time
// argument (httputil.GetTime -> timeutil.ParseTimeMsec -> ParseTimeAt in
// lib/timeutil/time.go) for the forms a client or the proxy may send: a
// value without "-" is a number whose unit vmselect guesses from its size
// (getUnixTimestampNanoseconds), anything else with a zone is RFC3339 read
// through time.UnixNano. Relative and partial dates are left out; the
// proxy refuses them.
func vmselectTimeMillis(t *testing.T, s string) int64 {
	t.Helper()
	if strings.Contains(s, "-") {
		ts, err := time.Parse(time.RFC3339, s)
		if err != nil {
			t.Fatalf("vmselect cannot parse %q: %v", s, err)
		}
		return int64(math.Round(float64(ts.UnixNano()) / 1e6))
	}
	intPart, frac, _ := strings.Cut(s, ".")
	n, err := strconv.ParseInt(intPart+frac, 10, 64)
	if err != nil {
		t.Fatalf("vmselect cannot parse %q: %v", s, err)
	}
	for e := len(frac); e%3 != 0; e++ {
		n *= 10
	}
	var nanos int64
	switch {
	case n < 1<<31:
		nanos = n * 1e9
	case n < 1e3*(1<<31):
		nanos = n * 1e6
	case n < 1e6*(1<<31):
		nanos = n * 1e3
	default:
		nanos = n
	}
	return int64(math.Round(float64(nanos) / 1e6))
}

// TestVmselectReadsProxyTimes sends times across the range the proxy
// accepts, in both client forms, and checks that vmselect reads the
// forwarded value as the same instant the proxy validated. A number such
// as "1000000.000" is seconds to the proxy but milliseconds-as-seconds
// guesswork to vmselect, so the proxy must not forward one.
func TestVmselectReadsProxyTimes(t *testing.T) {
	for _, client := range []string{
		"0", "1000000", "1762400", "2147483", "2147484", "1000000.5",
		"1793793600", "1793793600.123", "2147483647", "2147483648", "9223372035",
		"1970-01-01T00:00:00Z", "1970-01-12T13:46:40.001Z", "2026-11-04T12:00:00.123Z", "2262-01-01T00:00:00Z",
	} {
		want, herr := parseMetricsTime("time", client)
		if herr != nil {
			t.Fatalf("%q refused: %v", client, herr)
		}
		req := buildMetrics(t, "api/v1/query", url.Values{"query": {"up"}, "time": {client}})
		sent := req.params.Get("time")
		if got := vmselectTimeMillis(t, sent); got != want.UnixMilli() {
			t.Errorf("client time %q forwarded as %q reaches vmselect as %dms, want %dms", client, sent, got, want.UnixMilli())
		}
	}
}

// TestVmselectReadsSecondsWithFraction is the window that passed the proxy as
// 8.8 days and 30 points while vmselect, reading "1000000.000" as a
// thousand times that many seconds, saw 2001..2025 and 30000 points.
func TestVmselectReadsSecondsWithFraction(t *testing.T) {
	in := url.Values{"query": {"up"}, "start": {"1000000"}, "end": {"1762400"}, "step": {"25414"}}
	req := buildMetrics(t, "api/v1/query_range", in)
	start, end := vmselectTimeMillis(t, req.params.Get("start")), vmselectTimeMillis(t, req.params.Get("end"))
	step := vmselectMillis(t, req.params.Get("step"))
	if start != 1000000*1000 || end != 1762400*1000 {
		t.Fatalf("vmselect reads %dms..%dms, want 1000000s..1762400s", start, end)
	}
	if points := (end-start)/step + 1; points != 30 {
		t.Fatalf("vmselect counts %d points, want the 30 the proxy checked", points)
	}
	labels := buildMetrics(t, "api/v1/labels", url.Values{"start": {"1000000"}, "end": {"1000300"}})
	if got := vmselectTimeMillis(t, labels.params.Get("start")); got != 1000000*1000 {
		t.Fatalf("label API start reaches vmselect as %dms, want 1000000s", got)
	}
}

func TestParseMetricsTimeRange(t *testing.T) {
	// vmselect reads an RFC3339 time through time.UnixNano, which is
	// undefined from 2262-04-11 on.
	for _, v := range []string{"9223372037", "9999999999", "2262-04-12T00:00:00Z", "9999-01-01T00:00:00Z"} {
		if _, herr := parseMetricsTime("time", v); herr == nil {
			t.Errorf("%q accepted, want refused", v)
		}
	}
}
