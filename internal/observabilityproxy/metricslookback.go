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
	"regexp"
	"strconv"
	"strings"
	"time"
)

// instantDefaultStep is the step vmselect uses for an instant query without
// one (defaultStep in app/vmselect/prometheus); it is also the implicit
// rollup window of a bare selector.
const instantDefaultStep = 5 * time.Minute

// silenceInterval is how far before a rollup or subquery window vmselect
// starts reading (maxSilenceInterval in app/vmselect/promql: the value of
// -search.minStalenessInterval, 5m when unset; cozystack does not set it).
const silenceInterval = 5 * time.Minute

// queryDurationPart is the subset of the MetricsQL duration grammar
// (scanSingleDuration in metricsql) the scan understands. Step multiples
// (5i, $__interval), negative parts and upper-case units are left out on
// purpose: anything else makes the bound unknown.
var (
	queryDurationPart = regexp.MustCompile(`(\d+(?:\.\d+)?)(ms|s|m|h|d|w|y)`)
	queryDuration     = regexp.MustCompile(`^(?:\d+(?:\.\d+)?(?:ms|s|m|h|d|w|y))+$`)
	querySeconds      = regexp.MustCompile(`^\d+(?:\.\d+)?$`)
)

var queryDurationUnits = map[string]time.Duration{
	"ms": time.Millisecond, "s": time.Second, "m": time.Minute, "h": time.Hour,
	"d": 24 * time.Hour, "w": 7 * 24 * time.Hour, "y": 365 * 24 * time.Hour,
}

// queryStart is the earliest instant a query evaluated from start may read;
// zero when the bound is unknown.
func queryStart(query string, start time.Time, step time.Duration) time.Time {
	lookback, ok := queryLookback(query, step)
	if !ok {
		return time.Time{}
	}
	return start.Add(-lookback)
}

// queryLookback is an upper bound on how far before its evaluation window
// a query reads, from a lexical scan that never parses the query. Every
// [...] and offset counts, including ones inside quoted label values: an
// extra hit only makes the bound larger or unknown, never smaller. Windows
// and offsets are summed rather than maxed, which also covers nesting.
// vmselect starts a rollup max(window, step) plus the silence interval
// before the first point, a subquery window+step+silence before it and
// aligns both down to a step; each step term uses the largest step seen.
func queryLookback(query string, step time.Duration) (time.Duration, bool) {
	if strings.Contains(query, "@") {
		return 0, false
	}
	var sum float64
	subqueries := 0
	maxStep := float64(step)
	for rest := query; ; {
		open := strings.IndexByte(rest, '[')
		if open < 0 {
			break
		}
		closing := strings.IndexByte(rest[open:], ']')
		if closing < 0 {
			return 0, false
		}
		window, subStep, isSubquery := strings.Cut(rest[open+1:open+closing], ":")
		rest = rest[open+closing+1:]
		if isSubquery {
			subqueries++
			if strings.TrimSpace(window) == "" {
				window = "0s"
			}
			if strings.TrimSpace(subStep) != "" {
				d, ok := parseQueryDuration(subStep)
				if !ok {
					return 0, false
				}
				maxStep = max(maxStep, d)
			}
		}
		d, ok := parseQueryDuration(window)
		if !ok {
			return 0, false
		}
		sum += d
	}
	lower := strings.ToLower(query)
	for i := 0; ; {
		k := strings.Index(lower[i:], "offset")
		if k < 0 {
			break
		}
		k += i
		i = k + len("offset")
		if k > 0 && continuesIdent(lower[k-1], false) || i < len(lower) && continuesIdent(lower[i], true) {
			continue
		}
		arg := strings.TrimLeft(lower[i:], " \t\n\r")
		negative := strings.HasPrefix(arg, "-")
		if negative {
			arg = strings.TrimLeft(arg[1:], " \t\n\r")
		}
		end := strings.IndexFunc(arg, func(r rune) bool {
			return !(r >= '0' && r <= '9' || r >= 'a' && r <= 'z' || r == '.')
		})
		if end < 0 {
			end = len(arg)
		}
		d, ok := parseQueryDuration(arg[:end])
		if !ok {
			return 0, false
		}
		if !negative {
			sum += d
		}
	}
	n := float64(subqueries)
	sum += (2+3*n)*maxStep + (1+n)*float64(silenceInterval)
	if sum > float64(maxMetricsDuration) {
		return 0, false
	}
	return time.Duration(sum), true
}

// continuesIdent reports whether c, next to "offset", makes it part of an
// identifier rather than the keyword. Digits and dots only continue an
// identifier after it, so before it they still count as a keyword hit.
func continuesIdent(c byte, after bool) bool {
	return c >= 'a' && c <= 'z' || c == '_' || c == ':' || c >= 0x80 ||
		after && (c >= '0' && c <= '9' || c == '.' || c == '\\')
}

// parseQueryDuration returns nanoseconds as a float so that a sum of huge
// durations cannot wrap; the caller compares it against the bound.
func parseQueryDuration(s string) (float64, bool) {
	s = strings.TrimSpace(s)
	if querySeconds.MatchString(s) {
		f, err := strconv.ParseFloat(s, 64)
		return f * float64(time.Second), err == nil
	}
	if !queryDuration.MatchString(s) {
		return 0, false
	}
	var d float64
	for _, m := range queryDurationPart.FindAllStringSubmatch(s, -1) {
		f, err := strconv.ParseFloat(m[1], 64)
		if err != nil {
			return 0, false
		}
		d += f * float64(queryDurationUnits[m[2]])
	}
	return d, true
}
