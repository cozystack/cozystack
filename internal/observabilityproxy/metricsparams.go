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
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// MetricsLimits bound what one metrics request may make vmselect do.
type MetricsLimits struct {
	MaxRange   time.Duration
	MaxPoints  int
	MaxTimeout time.Duration
}

// MetricsConfig names the vmselect stores of the root tenant and the
// retention that decides between them.
type MetricsConfig struct {
	RootTenant         string
	ShortTermStore     string
	LongTermStore      string
	ShortTermRetention time.Duration
	Limits             MetricsLimits
	// UpstreamURL maps a store name to its Prometheus API base URL, without
	// a trailing slash; nil means the in-cluster vmselect Service.
	UpstreamURL func(store string) string
}

// store picks the short-term store when everything from start on is still
// within its retention, so a live graph never waits on the downsampled one.
func (c MetricsConfig) store(start, now time.Time) string {
	if now.Sub(start) <= c.ShortTermRetention {
		return c.ShortTermStore
	}
	return c.LongTermStore
}

// maxMetricsMatchers bounds match[]: vmselect runs one index search per
// matcher and has no cap of its own.
const maxMetricsMatchers = 20

// labelsDefaultWindow is vmselect's own default for the label and series
// APIs (defaultStep in app/vmselect/prometheus); the proxy sends it
// explicitly so the store choice and the range check see the same window.
const labelsDefaultWindow = 5 * time.Minute

var labelName = regexp.MustCompile(`^[a-zA-Z_][a-zA-Z0-9_]*$`)

type metricsRequest struct {
	path   string
	params url.Values
	// start is the earliest instant the request reads, for the store choice.
	start time.Time
}

// namespaceSelector quotes ns the way MetricsQL unquotes a label value
// (strconv.Unquote in metricsql's extractStringValue).
func namespaceSelector(ns string) string {
	return "{namespace=" + strconv.Quote(ns) + "}"
}

// BuildMetricsParams rebuilds the upstream parameters from an allowlist and
// adds the namespace scope as the only extra_filters[] value. vmselect ORs
// repeated extra_filters, so a client value would widen the scope; it ANDs
// the single one into every selector of the query and every match[], which
// is what lets the query pass through unparsed.
func BuildMetricsParams(endpoint string, in url.Values, namespace string, now time.Time, lim MetricsLimits) (metricsRequest, *HTTPError) {
	req := metricsRequest{path: endpoint, params: url.Values{}}
	out := req.params
	out.Set("extra_filters[]", namespaceSelector(namespace))
	timeout := lim.MaxTimeout
	if v := in.Get("timeout"); v != "" {
		d, herr := parseMetricsDuration("timeout", v)
		if herr != nil {
			return req, herr
		}
		timeout = min(d, lim.MaxTimeout)
	}
	out.Set("timeout", formatSeconds(timeout))

	switch endpoint {
	case "api/v1/query":
		if herr := copyQuery(out, in); herr != nil {
			return req, herr
		}
		req.start = now
		if v := in.Get("time"); v != "" {
			t, herr := parseMetricsTime("time", v)
			if herr != nil {
				return req, herr
			}
			req.start = t
			out.Set("time", formatTime(t))
		}
		if v := in.Get("step"); v != "" {
			step, herr := parseStep(v, lim)
			if herr != nil {
				return req, herr
			}
			out.Set("step", formatSeconds(step))
		}
		return req, nil

	case "api/v1/query_range":
		if herr := copyQuery(out, in); herr != nil {
			return req, herr
		}
		for _, k := range []string{"start", "end", "step"} {
			if in.Get(k) == "" {
				return req, errorf(http.StatusBadRequest, "%s is required", k)
			}
		}
		start, end, herr := metricsWindow(in, now, lim)
		if herr != nil {
			return req, herr
		}
		step, herr := parseStep(in.Get("step"), lim)
		if herr != nil {
			return req, herr
		}
		if points := end.Sub(start)/step + 1; points > time.Duration(lim.MaxPoints) {
			return req, errorf(http.StatusUnprocessableEntity, "%d points per series exceed the limit of %d; raise step", points, lim.MaxPoints)
		}
		req.start = start
		out.Set("start", formatTime(start))
		out.Set("end", formatTime(end))
		out.Set("step", formatSeconds(step))
		return req, nil
	}

	name, isLabelValues := strings.CutPrefix(endpoint, "api/v1/label/")
	if isLabelValues {
		var ok bool
		if name, ok = strings.CutSuffix(name, "/values"); !ok || strings.Contains(name, "/") {
			return req, errorf(http.StatusNotFound, "not found")
		}
		if !labelName.MatchString(name) {
			return req, errorf(http.StatusBadRequest, "invalid label name %q", name)
		}
	} else if endpoint != "api/v1/series" && endpoint != "api/v1/labels" {
		return req, errorf(http.StatusNotFound, "not found")
	}
	start, end, herr := metricsWindow(in, now, lim)
	if herr != nil {
		return req, herr
	}
	req.start = start
	out.Set("start", formatTime(start))
	out.Set("end", formatTime(end))

	matchers := in["match[]"]
	if len(matchers) > maxMetricsMatchers {
		return req, errorf(http.StatusUnprocessableEntity, "at most %d match[] are allowed", maxMetricsMatchers)
	}
	for _, m := range matchers {
		if m == "" {
			return req, errorf(http.StatusBadRequest, "empty match[]")
		}
		out.Add("match[]", m)
	}
	if len(matchers) == 0 {
		if endpoint == "api/v1/series" {
			return req, errorf(http.StatusBadRequest, "match[] is required")
		}
		// vmselect started with -search.ignoreExtraFiltersAtLabelsAPI drops
		// extra_filters[] on the label APIs unless a match[] is present
		// (getCommonParamsInternal), so the scope is repeated here.
		out.Set("match[]", namespaceSelector(namespace))
	}
	return req, nil
}

func copyQuery(out, in url.Values) *HTTPError {
	q := in.Get("query")
	if q == "" {
		return errorf(http.StatusBadRequest, "query is required")
	}
	out.Set("query", q)
	return nil
}

// metricsWindow reads start and end with vmselect's label API defaults:
// end is now and start is five minutes before end.
func metricsWindow(in url.Values, now time.Time, lim MetricsLimits) (time.Time, time.Time, *HTTPError) {
	end := now
	if v := in.Get("end"); v != "" {
		t, herr := parseMetricsTime("end", v)
		if herr != nil {
			return time.Time{}, time.Time{}, herr
		}
		end = t
	}
	start := end.Add(-labelsDefaultWindow)
	if v := in.Get("start"); v != "" {
		t, herr := parseMetricsTime("start", v)
		if herr != nil {
			return time.Time{}, time.Time{}, herr
		}
		start = t
	}
	if start.After(end) {
		return time.Time{}, time.Time{}, errorf(http.StatusBadRequest, "start is after end")
	}
	if end.Sub(start) > lim.MaxRange {
		return time.Time{}, time.Time{}, errorf(http.StatusUnprocessableEntity, "range %s exceeds the limit of %s", end.Sub(start), lim.MaxRange)
	}
	return start, end, nil
}

// parseStep refuses a step above the range limit: vmselect aligns the
// window outward to whole steps, and an implicit rollup window is one step,
// so a huge step reads far more than the range it was sent with.
func parseStep(v string, lim MetricsLimits) (time.Duration, *HTTPError) {
	step, herr := parseMetricsDuration("step", v)
	if herr != nil {
		return 0, herr
	}
	if step > lim.MaxRange {
		return 0, errorf(http.StatusUnprocessableEntity, "step %s exceeds the limit of %s", step, lim.MaxRange)
	}
	return step, nil
}

// parseMetricsDuration takes Prometheus float seconds or a duration such as
// 1m30s. vmselect works in milliseconds and refuses anything shorter.
func parseMetricsDuration(key, v string) (time.Duration, *HTTPError) {
	d, err := parseDuration(v)
	if err != nil || d < time.Millisecond {
		return 0, errorf(http.StatusBadRequest, "invalid %s %q", key, v)
	}
	return d, nil
}

// parseMetricsTime takes the two forms the Prometheus API defines: float
// unix seconds and RFC3339. vmselect would also guess milliseconds from the
// digit count and accept relative times; a guessed unit silently moves the
// window, so those are refused.
func parseMetricsTime(key, v string) (time.Time, *HTTPError) {
	if sec, frac, ok := strings.Cut(v, "."); isDigits(sec) && (!ok || isDigits(frac)) {
		if len(sec) > 10 {
			return time.Time{}, errorf(http.StatusBadRequest, "invalid %s %q: not unix seconds", key, v)
		}
		secs, _ := strconv.ParseInt(sec, 10, 64)
		var nanos int64
		if ok {
			nanos, _ = strconv.ParseInt((frac + "000000000")[:9], 10, 64)
		}
		return time.Unix(secs, nanos).UTC(), nil
	}
	t, err := time.Parse(time.RFC3339Nano, v)
	if err != nil || t.Before(time.Unix(0, 0)) {
		return time.Time{}, errorf(http.StatusBadRequest, "invalid %s %q", key, v)
	}
	return t.UTC(), nil
}

func formatTime(t time.Time) string {
	ms := t.UnixMilli()
	return fmt.Sprintf("%d.%03d", ms/1000, ms%1000)
}

func formatSeconds(d time.Duration) string {
	return strconv.FormatFloat(float64(d.Milliseconds())/1000, 'f', -1, 64)
}
