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
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestNamespaceScope(t *testing.T) {
	tests := []struct {
		prefix string
		want   []string
	}{
		{"cozy-", []string{`{namespace="tenant-a"}`, `{namespace=~"cozy-.*",exported_namespace="tenant-a"}`}},
		{"", []string{`{namespace="tenant-a"}`}},
		// A prefix is literal: a dot in it must not match any character.
		{"sys.", []string{`{namespace="tenant-a"}`, `{namespace=~"sys\\..*",exported_namespace="tenant-a"}`}},
	}
	for _, tt := range tests {
		if got := (MetricsConfig{PlatformNamespacePrefix: tt.prefix}).namespaceScope("tenant-a"); !slices.Equal(got, tt.want) {
			t.Errorf("prefix %q: scope = %q, want %q", tt.prefix, got, tt.want)
		}
	}
}

func TestNamespaceScopeEscapesExportedNamespace(t *testing.T) {
	for _, ns := range []string{`a"} or {namespace=~".+`, `back\slash`, "new\nline"} {
		scope := MetricsConfig{PlatformNamespacePrefix: "cozy-"}.namespaceScope(ns)
		filters, ok := parseSelector(scope[1])
		if !ok || len(filters) != 2 || filters[1].label != "exported_namespace" || filters[1].value != ns {
			t.Errorf("scope %q does not decode to exported_namespace=%q: %+v", scope[1], ns, filters)
		}
	}
}

// TestBuildMetricsParamsScopeOnEveryEndpoint checks that every endpoint of
// the namespace root sends the whole scope and nothing a client adds.
func TestBuildMetricsParamsScopeOnEveryEndpoint(t *testing.T) {
	endpoints := map[string]url.Values{
		"api/v1/query":            {"query": {"up"}},
		"api/v1/query_range":      {"query": {"up"}, "start": {unix(testNow.Add(-5 * time.Minute))}, "end": {unix(testNow)}, "step": {"60"}},
		"api/v1/series":           {"match[]": {"up"}},
		"api/v1/labels":           {},
		"api/v1/label/pod/values": {},
	}
	for _, prefix := range []string{"cozy-", ""} {
		scope := MetricsConfig{PlatformNamespacePrefix: prefix}.namespaceScope("tenant-a")
		for endpoint, in := range endpoints {
			in = cloneValues(in)
			in["extra_filters[]"] = []string{`{namespace=~".+"}`}
			req, herr := BuildMetricsParams(endpoint, in, scope, testNow, testMetricsLimits())
			if herr != nil {
				t.Fatalf("%s: %v", endpoint, herr)
			}
			if got := req.params["extra_filters[]"]; !slices.Equal(got, scope) {
				t.Errorf("prefix %q, %s: extra_filters[] = %q, want %q", prefix, endpoint, got, scope)
			}
		}
	}
}

// An empty scope would send no extra_filters[] and read every namespace.
func TestBuildMetricsParamsRefusesEmptyScope(t *testing.T) {
	if _, herr := BuildMetricsParams("api/v1/query", url.Values{"query": {"up"}}, nil, testNow, testMetricsLimits()); herr == nil || herr.Code != http.StatusInternalServerError {
		t.Errorf("empty scope: %v, want 500", herr)
	}
}

// TestBuildMetricsParamsCopiesScope guards against the forwarded parameters
// sharing the caller's slice.
func TestBuildMetricsParamsCopiesScope(t *testing.T) {
	scope := testScope()
	req, herr := BuildMetricsParams("api/v1/labels", url.Values{}, scope, testNow, testMetricsLimits())
	if herr != nil {
		t.Fatal(herr)
	}
	scope[1] = `{namespace=~".+"}`
	if !slices.Equal(req.params["extra_filters[]"], wantMetricsScope) || !slices.Equal(req.params["match[]"], wantMetricsScope) {
		t.Errorf("params follow the caller's slice: %v", req.params)
	}
}

// TestVmselectSeesScopedSeries runs the parameters the proxy builds
// through a model of vmselect v1.136's series selection: repeated
// extra_filters[] are ORed (searchutil.GetExtraTagFilters), and every
// match[] or query selector is ANDed with each of them
// (searchutil.JoinTagFilterss, called from getCommonParamsInternal and
// promql eval). Regex filters match the whole value.
func TestVmselectSeesScopedSeries(t *testing.T) {
	series := []struct {
		labels  map[string]string
		visible bool
	}{
		{map[string]string{"namespace": "tenant-a"}, true},
		{map[string]string{"namespace": "cozy-clickhouse-operator", "exported_namespace": "tenant-a", "chi": "clickhouse-db"}, true},
		// A tenant pod claiming another namespace is not a platform exporter.
		{map[string]string{"namespace": "tenant-y", "exported_namespace": "tenant-a"}, false},
		{map[string]string{"namespace": "xcozy-a", "exported_namespace": "tenant-a"}, false},
		{map[string]string{"namespace": "cozy-clickhouse-operator", "exported_namespace": "tenant-b"}, false},
		{map[string]string{"namespace": "cozy-clickhouse-operator"}, false},
		{map[string]string{"namespace": "tenant-b"}, false},
	}
	for _, endpoint := range []string{"api/v1/series", "api/v1/labels"} {
		in := url.Values{}
		if endpoint == "api/v1/series" {
			in.Set("match[]", `{__name__=~".*"}`)
		}
		req := buildMetrics(t, endpoint, in)
		for _, s := range series {
			if got := vmselectSelects(t, req.params, s.labels); got != s.visible {
				t.Errorf("%s: series %v visible = %v, want %v", endpoint, s.labels, got, s.visible)
			}
		}
	}

	// Without a prefix the exporter's series stay out of the namespace.
	req, herr := BuildMetricsParams("api/v1/labels", url.Values{}, MetricsConfig{}.namespaceScope("tenant-a"), testNow, testMetricsLimits())
	if herr != nil {
		t.Fatal(herr)
	}
	if vmselectSelects(t, req.params, map[string]string{"namespace": "cozy-clickhouse-operator", "exported_namespace": "tenant-a"}) {
		t.Error("exporter series visible with the platform prefix disabled")
	}
}

func cloneValues(in url.Values) url.Values {
	out := url.Values{}
	for k, v := range in {
		out[k] = slices.Clone(v)
	}
	return out
}

type labelFilter struct {
	label, value string
	regex        bool
}

func (f labelFilter) matches(labels map[string]string) bool {
	v := labels[f.label]
	if f.regex {
		return regexp.MustCompile("^(?:" + f.value + ")$").MatchString(v)
	}
	return v == f.value
}

// parseSelector reads the selectors the proxy and these tests write:
// {name="value",name=~"regex",...} with values quoted as MetricsQL reads
// them (strconv.Unquote in metricsql's extractStringValue).
func parseSelector(s string) ([]labelFilter, bool) {
	body, ok := strings.CutPrefix(s, "{")
	if !ok {
		return nil, false
	}
	var out []labelFilter
	for body != "}" {
		i := strings.IndexAny(body, "=!")
		if i <= 0 {
			return nil, false
		}
		f := labelFilter{label: body[:i]}
		body = body[i:]
		switch {
		case strings.HasPrefix(body, "=~"):
			f.regex, body = true, body[2:]
		case strings.HasPrefix(body, "="):
			body = body[1:]
		default:
			return nil, false
		}
		lit, err := strconv.QuotedPrefix(body)
		if err != nil {
			return nil, false
		}
		if f.value, err = strconv.Unquote(lit); err != nil {
			return nil, false
		}
		out = append(out, f)
		body = strings.TrimPrefix(body[len(lit):], ",")
	}
	return out, true
}

func vmselectSelects(t *testing.T, params url.Values, labels map[string]string) bool {
	t.Helper()
	parse := func(sels []string) [][]labelFilter {
		var out [][]labelFilter
		for _, s := range sels {
			f, ok := parseSelector(s)
			if !ok {
				t.Fatalf("cannot parse selector %q", s)
			}
			out = append(out, f)
		}
		return out
	}
	matches, extra := parse(params["match[]"]), parse(params["extra_filters[]"])
	if len(matches) == 0 {
		matches = [][]labelFilter{nil}
	}
	for _, m := range matches {
		for _, e := range extra {
			all := true
			for _, f := range append(slices.Clone(m), e...) {
				if f.label == "__name__" {
					continue
				}
				all = all && f.matches(labels)
			}
			if all {
				return true
			}
		}
	}
	return false
}
