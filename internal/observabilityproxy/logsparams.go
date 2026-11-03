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
	"fmt"
	"math"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

// LogsLimits bound what one logs request may make vlselect do.
type LogsLimits struct {
	DefaultRange  time.Duration
	MaxRange      time.Duration
	MaxBuckets    int
	DefaultLimit  int
	MaxLimit      int
	MaxTailResume time.Duration
}

// tailOffset is vlselect's default tail offset. The proxy never forwards a
// client offset, so the resume arithmetic can rely on this value.
const tailOffset = 5 * time.Second

const maxHitsFields = 10

var logsEndpoints = map[string]bool{
	"query": true, "hits": true, "stats_query_range": true, "tail": true,
	"field_values": true, "stream_field_values": true, "facets": true,
}

// BuildLogsParams rebuilds the upstream parameters from scratch: only the
// allowlisted parameters of the endpoint are read from the client, and the
// namespace scope is added as the only extra_stream_filters value.
// VictoriaLogs ANDs extra_stream_filters into the query and every subquery
// (union, join, in()), which is what lets the query itself pass through
// unparsed.
func BuildLogsParams(endpoint string, in url.Values, namespace string, now time.Time, lim LogsLimits) (url.Values, *HTTPError) {
	if !logsEndpoints[endpoint] {
		return nil, errorf(http.StatusNotFound, "not found")
	}
	query := in.Get("query")
	if query == "" {
		query = "*"
	}
	// ignore_global_time_filter lifts the time window this function
	// enforces, and on tail rescans the whole retention every tick. A
	// substring match for it is bypassable (VictoriaLogs unquotes a quoted
	// option name and joins words split by a comment), and no option is
	// needed by a tenant, so any options(...) block is refused.
	if hasOptionsBlock(query) {
		return nil, errorf(http.StatusBadRequest, "query options are not allowed")
	}
	out := url.Values{}
	out.Set("query", query)
	out.Set("extra_stream_filters", "{kubernetes_namespace_name="+strconv.Quote(namespace)+"}")

	if endpoint == "tail" {
		return out, tailParams(in, out, now, lim)
	}

	start, end, herr := window(in, now, lim)
	if herr != nil {
		return nil, herr
	}
	out.Set("start", start.Format(time.RFC3339Nano))
	out.Set("end", end.Format(time.RFC3339Nano))

	switch endpoint {
	case "query":
		herr = setLimit(out, in, "limit", lim.DefaultLimit, lim.MaxLimit)
	case "hits":
		if herr = setStep(out, in, start, end, lim); herr != nil {
			return nil, herr
		}
		fields := in["field"]
		if len(fields) > maxHitsFields {
			return nil, errorf(http.StatusBadRequest, "at most %d field parameters are allowed", maxHitsFields)
		}
		for _, f := range fields {
			if f != "" {
				out.Add("field", f)
			}
		}
		if in.Has("fields_limit") {
			herr = setLimit(out, in, "fields_limit", 0, lim.MaxLimit)
		}
	case "stats_query_range":
		herr = setStep(out, in, start, end, lim)
	case "field_values", "stream_field_values":
		if in.Get("field") == "" {
			return nil, errorf(http.StatusBadRequest, "field is required")
		}
		out.Set("field", in.Get("field"))
		if f := in.Get("filter"); f != "" {
			out.Set("filter", f)
		}
		herr = setLimit(out, in, "limit", lim.DefaultLimit, lim.MaxLimit)
	case "facets":
		for _, k := range []string{"limit", "max_values_per_field", "max_value_len"} {
			if k == "limit" || in.Has(k) {
				if herr = setLimit(out, in, k, lim.DefaultLimit, lim.MaxLimit); herr != nil {
					return nil, herr
				}
			}
		}
		if v := in.Get("keep_const_fields"); v == "1" || v == "true" {
			out.Set("keep_const_fields", "1")
		}
	}
	if herr != nil {
		return nil, herr
	}
	return out, nil
}

// window derives a finite [start, end]: VictoriaLogs otherwise starts at
// the oldest stored entry and scans the whole retention.
func window(in url.Values, now time.Time, lim LogsLimits) (time.Time, time.Time, *HTTPError) {
	end := now
	if v := in.Get("end"); v != "" {
		t, err := parseTime(v, now)
		if err != nil {
			return time.Time{}, time.Time{}, errorf(http.StatusBadRequest, "invalid end: %v", err)
		}
		end = t
	}
	start := end.Add(-lim.DefaultRange)
	if v := in.Get("start"); v != "" {
		t, err := parseTime(v, now)
		if err != nil {
			return time.Time{}, time.Time{}, errorf(http.StatusBadRequest, "invalid start: %v", err)
		}
		start = t
	}
	if start.After(end) {
		return time.Time{}, time.Time{}, errorf(http.StatusBadRequest, "start is after end")
	}
	if end.Sub(start) > lim.MaxRange {
		start = end.Add(-lim.MaxRange)
	}
	return start.UTC(), end.UTC(), nil
}

// setStep raises step so that hits and stats_query_range, which return one
// bucket per step, return at most MaxBuckets buckets, and caps it so the
// window cannot grow through it: vlselect widens [start, end] outward to
// step boundaries (alignStartEndToStep), by up to one step on each side,
// so a 100y step would otherwise scan the whole retention. The window is
// shrunk from the start until the widened span fits MaxRange.
func setStep(out, in url.Values, start, end time.Time, lim LogsLimits) *HTTPError {
	span := end.Sub(start)
	minStep := max((span+time.Duration(lim.MaxBuckets)-1)/time.Duration(lim.MaxBuckets), time.Second)
	step := minStep
	if v := in.Get("step"); v != "" {
		d, err := parseDuration(v)
		if err != nil || d <= 0 {
			return errorf(http.StatusBadRequest, "invalid step %q", v)
		}
		step = max(min(d, span, lim.MaxRange/3), minStep)
	}
	// vlselect gets whole milliseconds; rounding down would add a bucket.
	if r := step % time.Millisecond; r != 0 {
		step += time.Millisecond - r
	}
	if span+2*step > lim.MaxRange {
		start = end.Add(-max(lim.MaxRange-2*step, 0))
	}
	out.Set("step", formatDuration(step))
	out.Set("start", start.Format(time.RFC3339Nano))
	return nil
}

func setLimit(out, in url.Values, key string, def, maxv int) *HTTPError {
	n := def
	if v := in.Get(key); v != "" {
		parsed, err := strconv.Atoi(v)
		if err != nil || parsed <= 0 {
			return errorf(http.StatusBadRequest, "invalid %s %q", key, v)
		}
		n = parsed
	}
	out.Set(key, strconv.Itoa(min(n, maxv)))
	return nil
}

// tailParams turns a resume point into a capped start_offset. A reconnect
// older than the cap resumes at the cap; the gap needs a historical query.
func tailParams(in, out url.Values, now time.Time, lim LogsLimits) *HTTPError {
	var start, end time.Time
	for _, k := range []string{"start", "end"} {
		v := in.Get(k)
		if v == "" {
			continue
		}
		t, err := parseTime(v, now)
		if err != nil {
			return errorf(http.StatusBadRequest, "invalid %s: %v", k, err)
		}
		out.Set(k, t.UTC().Format(time.RFC3339Nano))
		if k == "start" {
			start = t
		} else {
			end = t
		}
	}
	if !start.IsZero() && !end.IsZero() && start.After(end) {
		return errorf(http.StatusBadRequest, "start is after end")
	}

	since, offset := in.Get("since"), in.Get("start_offset")
	var d time.Duration
	switch {
	case since != "" && offset != "":
		return errorf(http.StatusBadRequest, "since and start_offset are mutually exclusive")
	case since != "":
		t, err := parseTime(since, now)
		if err != nil {
			return errorf(http.StatusBadRequest, "invalid since: %v", err)
		}
		d = now.Add(-tailOffset).Sub(t)
	case offset != "":
		parsed, err := parseDuration(offset)
		if err != nil {
			return errorf(http.StatusBadRequest, "invalid start_offset %q", offset)
		}
		d = parsed
	default:
		return nil
	}
	out.Set("start_offset", formatDuration(min(max(d, 0), lim.MaxTailResume)))
	return nil
}

// formatDuration writes whole milliseconds, a form every VictoriaLogs
// duration parser accepts.
func formatDuration(d time.Duration) string {
	return fmt.Sprintf("%dms", d.Milliseconds())
}

var durationToken = regexp.MustCompile(`^(\d+(?:\.\d+)?)(ms|s|m|h|d|w|y)`)

var durationUnits = map[string]time.Duration{
	"ms": time.Millisecond, "s": time.Second, "m": time.Minute, "h": time.Hour,
	"d": 24 * time.Hour, "w": 7 * 24 * time.Hour, "y": 365 * 24 * time.Hour,
}

// parseDuration accepts the LogsQL duration forms (1h30m, 1d, 2w) and bare
// seconds; Go's parser knows neither d nor w.
func parseDuration(s string) (time.Duration, error) {
	// Bare seconds only as digits with an optional fraction: ParseFloat
	// would also take signs, exponents, Inf and NaN.
	if whole, frac, ok := strings.Cut(s, "."); isDigits(whole) && (!ok || isDigits(frac)) {
		f, err := strconv.ParseFloat(s, 64)
		if err != nil {
			return 0, fmt.Errorf("invalid duration %q", s)
		}
		d, ok := scaleDuration(f, time.Second)
		if !ok {
			return 0, fmt.Errorf("duration %q out of range", s)
		}
		return d, nil
	}
	if s == "" {
		return 0, fmt.Errorf("empty duration")
	}
	var total time.Duration
	for rest := s; rest != ""; {
		m := durationToken.FindStringSubmatch(rest)
		if m == nil {
			return 0, fmt.Errorf("invalid duration %q", s)
		}
		f, _ := strconv.ParseFloat(m[1], 64)
		d, ok := scaleDuration(f, durationUnits[m[2]])
		if !ok || total > math.MaxInt64-d {
			return 0, fmt.Errorf("duration %q out of range", s)
		}
		total += d
		rest = rest[len(m[0]):]
	}
	return total, nil
}

// scaleDuration refuses what does not fit a Duration: Go leaves an
// out-of-range float to int conversion implementation-defined.
func scaleDuration(f float64, unit time.Duration) (time.Duration, bool) {
	v := f * float64(unit)
	// float64(math.MaxInt64) rounds up to 2^63, which is already too large.
	if v >= float64(math.MaxInt64) {
		return 0, false
	}
	return time.Duration(v), true
}

// parseTime accepts "now", unix seconds with an optional fraction, RFC3339,
// and a duration meaning that long before now (VictoriaLogs' "start=3h").
// Anything else is refused rather than guessed, since a misread timestamp
// silently moves the window.
func parseTime(s string, now time.Time) (time.Time, error) {
	if s == "now" {
		return now, nil
	}
	if sec, frac, ok := strings.Cut(s, "."); isDigits(sec) && (!ok || isDigits(frac)) {
		// vlselect reads the unit from the digit count (four digits are a
		// year, 13 are milliseconds); only unambiguous seconds are taken.
		if len(sec) < 5 || len(sec) > 10 {
			return time.Time{}, fmt.Errorf("numeric time %q is not unix seconds", s)
		}
		secs, err := strconv.ParseInt(sec, 10, 64)
		if err != nil {
			return time.Time{}, err
		}
		var nanos int64
		if ok {
			frac = (frac + "000000000")[:9]
			nanos, _ = strconv.ParseInt(frac, 10, 64)
		}
		return time.Unix(secs, nanos), nil
	}
	if t, err := time.Parse(time.RFC3339Nano, s); err == nil {
		return t, nil
	}
	if d, err := parseDuration(s); err == nil {
		return now.Add(-d), nil
	}
	return time.Time{}, fmt.Errorf("unsupported time %q", s)
}

// hasOptionsBlock reports whether the options keyword is followed by "(",
// skipping whitespace and "#" comments the way the LogsQL lexer does. A
// quoted "options" is never the keyword, but matching it too only refuses
// a search for that literal phrase.
func hasOptionsBlock(query string) bool {
	lower := strings.ToLower(query)
	for i := strings.Index(lower, "options"); i >= 0; {
		rest := lower[i+len("options"):]
		for rest != "" {
			r, size := utf8.DecodeRuneInString(rest)
			switch {
			case unicode.IsSpace(r):
				rest = rest[size:]
				continue
			case r == '#':
				if n := strings.IndexByte(rest, '\n'); n >= 0 {
					rest = rest[n+1:]
				} else {
					rest = ""
				}
				continue
			}
			break
		}
		if strings.HasPrefix(rest, "(") {
			return true
		}
		next := strings.Index(lower[i+1:], "options")
		if next < 0 {
			return false
		}
		i += next + 1
	}
	return false
}

func isDigits(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}
