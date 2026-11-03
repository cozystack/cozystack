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
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"testing"
	"time"

	authorizationv1 "k8s.io/api/authorization/v1"
)

const appRoot = "/apis/apps.cozystack.io/v1alpha1/namespaces/tenant-a/postgreses/"

// appPolicy lets alice read every postgres in tenant-a, as the view role's
// apps.cozystack.io wildcard does, and bob only "a", as a grant narrowed
// with resourceNames does.
func appPolicy(user string, a *authorizationv1.ResourceAttributes) bool {
	if a.Namespace != "tenant-a" || a.Verb != "get" || a.Group != "apps.cozystack.io" || a.Resource != "postgreses" ||
		(a.Subresource != "log" && a.Subresource != "metrics") {
		return false
	}
	return user == "alice" || a.Name == "a"
}

func TestServerApplicationLogsScope(t *testing.T) {
	h := newPolicyHarness(t, Config{}, nil, appPolicy, nil)
	hostile := url.Values{
		"query":                {"error"},
		"field":                {"level"},
		"extra_filters":        {`{kubernetes_namespace_name="tenant-b"}`},
		"extra_stream_filters": {`{apps_cozystack_io_application_name=~".*"}`},
		"AccountID":            {"9"},
	}
	headers := map[string]string{"AccountID": "9", "ProjectID": "9"}
	endpoints := []string{"query", "hits", "stats_query_range", "tail", "field_values", "stream_field_values", "facets"}
	for _, ep := range endpoints {
		resp := h.do(http.MethodPost, appRoot+"db.main/log/select/logsql/"+ep+"?extra_stream_filters=x", "good", hostile, headers)
		_, _ = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("%s answered %d", ep, resp.StatusCode)
		}
	}
	want := authorizationv1.ResourceAttributes{Verb: "get", Group: "apps.cozystack.io", Resource: "postgreses", Subresource: "log", Name: "db.main", Namespace: "tenant-a"}
	if got := h.kube.lastSAR.Load().ResourceAttributes; *got != want {
		t.Fatalf("SubjectAccessReview = %+v, want %+v", *got, want)
	}
	reqs := h.requests()
	if len(reqs) != len(endpoints) {
		t.Fatalf("upstream saw %d requests, want %d", len(reqs), len(endpoints))
	}
	// Two values: vlselect ANDs every extra_stream_filters value into the
	// query and each of its subqueries.
	wantFilters := []string{wantScope, `{apps_cozystack_io_application_kind="Postgres",apps_cozystack_io_application_name="db.main"}`}
	for i, r := range reqs {
		if r.path != "/select/logsql/"+endpoints[i] {
			t.Errorf("upstream path = %s, want %s", r.path, endpoints[i])
		}
		if got := r.form["extra_stream_filters"]; !slices.Equal(got, wantFilters) {
			t.Errorf("%s: extra_stream_filters = %q, want %q", endpoints[i], got, wantFilters)
		}
		if r.form.Has("extra_filters") || r.form.Has("AccountID") || r.headers.Get("AccountID") != "0" || r.headers.Get("ProjectID") != "0" {
			t.Errorf("%s: client scope reached the store: %v %v", endpoints[i], r.form, r.headers)
		}
	}
}

func TestServerApplicationKinds(t *testing.T) {
	tests := []struct {
		name  string
		kinds KindLookup
		path  string
		want  int
	}{
		{"unknown plural logs", nil, "/apis/apps.cozystack.io/v1alpha1/namespaces/tenant-a/mysqls/a/log/select/logsql/query", http.StatusNotFound},
		{"unknown plural metrics", nil, "/apis/apps.cozystack.io/v1alpha1/namespaces/tenant-a/mysqls/a/metrics/api/v1/query?query=up", http.StatusNotFound},
		{"not synced logs", newTestKinds(t, false), appRoot + "a/log/select/logsql/query", http.StatusServiceUnavailable},
		{"not synced metrics", newTestKinds(t, false), appRoot + "a/metrics/api/v1/query?query=up", http.StatusServiceUnavailable},
		{"ambiguous plural", newTestKinds(t, true, appDefinition("x", "Postgres", "postgreses"), appDefinition("y", "Pg", "postgreses")), appRoot + "a/log/select/logsql/query", http.StatusServiceUnavailable},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := newPolicyHarness(t, Config{}, nil, func(string, *authorizationv1.ResourceAttributes) bool { return true }, tt.kinds)
			if got := h.status(http.MethodGet, tt.path, "good"); got != tt.want {
				t.Fatalf("status = %d, want %d", got, tt.want)
			}
			if n := len(h.requests()); n != 0 {
				t.Fatalf("upstream saw %d requests", n)
			}
		})
	}
	// The kind is looked up only for an authorized caller.
	h := newPolicyHarness(t, Config{}, nil, appPolicy, newTestKinds(t, false))
	if got := h.status(http.MethodGet, appRoot+"b/log/select/logsql/query", "bob-token"); got != http.StatusForbidden {
		t.Fatalf("denied caller answered %d, want 403", got)
	}
}

const wantPodLookup = `kube_pod_labels{namespace="tenant-a",label_apps_cozystack_io_application_kind="Postgres",label_apps_cozystack_io_application_name="db.main"}`

// appMetricsUpstream answers the pod and PVC lookups with the given names,
// or with status when it is not 200, and every other path like vmselect.
func appMetricsUpstream(pods, pvcs []string, podStatus int) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		names := map[string][]string{"/api/v1/label/pod/values": pods, "/api/v1/label/persistentvolumeclaim/values": pvcs}
		list, lookup := names[r.URL.Path]
		if !lookup {
			promUpstream(w, r)
			return
		}
		if r.URL.Path == "/api/v1/label/pod/values" && podStatus != http.StatusOK {
			http.Error(w, "unavailable", podStatus)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"status": "success", "data": list})
	}
}

func TestServerApplicationMetricsScope(t *testing.T) {
	h := newPolicyHarness(t, Config{}, appMetricsUpstream([]string{"db-main-0", "db.main-1"}, []string{"data-db-main-0"}, http.StatusOK), appPolicy, nil)
	hostile := url.Values{
		"query":           {"sum(rate(container_cpu_usage_seconds_total[5m]))"},
		"start":           {unix(testNow.Add(-time.Hour))},
		"end":             {unix(testNow)},
		"step":            {"60"},
		"extra_filters":   {`{namespace=~".+"}`},
		"extra_filters[]": {`{namespace="tenant-a"}`},
		"extra_label":     {"apps_cozystack_io_application_name=b"},
	}
	resp := h.do(http.MethodPost, appRoot+"db.main/metrics/api/v1/query_range", "good", hostile, nil)
	body, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("answered %d %q", resp.StatusCode, body)
	}
	want := authorizationv1.ResourceAttributes{Verb: "get", Group: "apps.cozystack.io", Resource: "postgreses", Subresource: "metrics", Name: "db.main", Namespace: "tenant-a"}
	if got := h.kube.lastSAR.Load().ResourceAttributes; *got != want {
		t.Fatalf("SubjectAccessReview = %+v, want %+v", *got, want)
	}
	reqs := h.requests()
	if len(reqs) != 3 {
		t.Fatalf("upstream saw %d requests, want two lookups and the query", len(reqs))
	}
	lookups := map[string]string{
		"/api/v1/label/pod/values":                   wantPodLookup,
		"/api/v1/label/persistentvolumeclaim/values": `kube_persistentvolumeclaim_labels{namespace="tenant-a",label_apps_cozystack_io_application_kind="Postgres",label_apps_cozystack_io_application_name="db.main"}`,
	}
	for _, r := range reqs[:2] {
		if got := r.form["match[]"]; len(got) != 1 || got[0] != lookups[r.path] {
			t.Errorf("%s match[] = %q", r.path, got)
		}
		if got := r.form["extra_filters[]"]; !slices.Equal(got, []string{wantMetricsScope}) {
			t.Errorf("%s extra_filters[] = %q", r.path, got)
		}
		// The query's own lookback is known and shorter than the 24h cap.
		if want := formatTime(buildMetrics(t, "api/v1/query_range", hostile).start); r.form.Get("start") != want {
			t.Errorf("%s start = %s, want the query's earliest read %s", r.path, r.form.Get("start"), want)
		}
		if r.form.Get("end") != formatTime(testNow) || r.form.Get("limit") != "101" {
			t.Errorf("%s end/limit = %s/%s", r.path, r.form.Get("end"), r.form.Get("limit"))
		}
	}
	main := reqs[2]
	wantBranches := []string{
		wantIdentityBranch,
		`{namespace="tenant-a",pod=~"^(db-main-0|db\\.main-1)$"}`,
		`{namespace="tenant-a",persistentvolumeclaim=~"^(data-db-main-0)$"}`,
	}
	if main.path != "/api/v1/query_range" || !slices.Equal(main.form["extra_filters[]"], wantBranches) {
		t.Fatalf("query %s extra_filters[] = %q, want %q", main.path, main.form["extra_filters[]"], wantBranches)
	}
	if main.form.Has("extra_filters") || main.form.Has("extra_label") {
		t.Fatalf("client scope reached the store: %v", main.form)
	}
	if !slices.Equal(h.stores, []string{"shortterm", "shortterm", "shortterm"}) {
		t.Fatalf("stores = %q, want the lookups on the query's store", h.stores)
	}
}

func TestServerApplicationMetricsBranches(t *testing.T) {
	podBranch := `{namespace="tenant-a",pod=~"^(p)$"}`
	tests := []struct {
		name       string
		pods, pvcs []string
		podStatus  int
		podBody    string
		cfg        Config
		path       string
		wantCode   int
		want       []string
	}{
		{name: "no pods or pvcs", path: "api/v1/query?query=up", wantCode: http.StatusOK, want: []string{wantIdentityBranch}},
		{name: "pod lookup fails", pods: []string{"p"}, pvcs: []string{"v"}, podStatus: http.StatusServiceUnavailable, path: "api/v1/query?query=up", wantCode: http.StatusOK,
			want: []string{wantIdentityBranch, `{namespace="tenant-a",persistentvolumeclaim=~"^(v)$"}`}},
		{name: "pods over the cap", pods: []string{"p", "q"}, cfg: Config{Metrics: MetricsConfig{App: AppScopeLimits{Lookback: time.Hour, MaxPods: 1, MaxPVCs: 1}}},
			path: "api/v1/query?query=up", wantCode: http.StatusUnprocessableEntity},
		{name: "pvcs over the cap", pods: []string{"p"}, pvcs: []string{"v", "w"}, cfg: Config{Metrics: MetricsConfig{App: AppScopeLimits{Lookback: time.Hour, MaxPods: 1, MaxPVCs: 1}}},
			path: "api/v1/query?query=up", wantCode: http.StatusUnprocessableEntity},
		{name: "labels get the branches as match[]", pods: []string{"p"}, path: "api/v1/labels", wantCode: http.StatusOK, want: []string{wantIdentityBranch, podBranch}},
		{name: "series needs match[]", path: "api/v1/series", wantCode: http.StatusBadRequest},
		// An empty name would make pod=~"^(|p)$", which matches every
		// series without a pod label.
		{name: "empty pod name dropped", pods: []string{"", "p"}, path: "api/v1/query?query=up", wantCode: http.StatusOK, want: []string{wantIdentityBranch, podBranch}},
		{name: "only empty pod names", pods: []string{""}, path: "api/v1/query?query=up", wantCode: http.StatusOK, want: []string{wantIdentityBranch}},
		{name: "lookup status error", podBody: `{"status":"error","data":["p"]}`, path: "api/v1/query?query=up", wantCode: http.StatusOK, want: []string{wantIdentityBranch}},
		{name: "truncated lookup answer", podBody: `{"status":"success","data":["p"`, path: "api/v1/query?query=up", wantCode: http.StatusOK, want: []string{wantIdentityBranch}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.podStatus == 0 {
				tt.podStatus = http.StatusOK
			}
			upstream := appMetricsUpstream(tt.pods, tt.pvcs, tt.podStatus)
			if tt.podBody != "" {
				upstream = func(w http.ResponseWriter, r *http.Request) {
					if r.URL.Path == "/api/v1/label/pod/values" {
						_, _ = io.WriteString(w, tt.podBody)
						return
					}
					appMetricsUpstream(nil, nil, http.StatusOK)(w, r)
				}
			}
			h := newPolicyHarness(t, tt.cfg, upstream, appPolicy, nil)
			if got := h.status(http.MethodGet, appRoot+"db.main/metrics/"+tt.path, "good"); got != tt.wantCode {
				t.Fatalf("status = %d, want %d", got, tt.wantCode)
			}
			var main *upstreamRequest
			for _, r := range h.requests() {
				if !strings.HasPrefix(r.path, "/api/v1/label/pod/") && !strings.HasPrefix(r.path, "/api/v1/label/persistentvolumeclaim/") {
					main = &r
				}
			}
			if tt.want == nil {
				if main != nil {
					t.Fatalf("query reached the store: %s %v", main.path, main.form)
				}
				return
			}
			if main == nil || !slices.Equal(main.form["extra_filters[]"], tt.want) {
				t.Fatalf("extra_filters[] = %v, want %q", main, tt.want)
			}
			if strings.HasSuffix(tt.path, "labels") && !slices.Equal(main.form["match[]"], tt.want) {
				t.Fatalf("match[] = %q, want the branches", main.form["match[]"])
			}
		})
	}
}

// A grant narrowed to application "a" by resourceNames must not reach "b"
// through any endpoint of either root.
func TestServerApplicationNarrowedGrant(t *testing.T) {
	h := newPolicyHarness(t, Config{}, appMetricsUpstream([]string{"a-0"}, nil, http.StatusOK), appPolicy, nil)
	paths := []string{
		"metrics/api/v1/query?query=up",
		"metrics/api/v1/query_range?query=up&start=" + unix(testNow.Add(-time.Hour)) + "&end=" + unix(testNow) + "&step=60",
		"metrics/api/v1/series?match[]=up",
		"metrics/api/v1/labels",
		"metrics/api/v1/label/pod/values",
		"log/select/logsql/query",
		"log/select/logsql/hits",
		"log/select/logsql/stats_query_range?query=" + url.QueryEscape("* | stats count()"),
		"log/select/logsql/tail",
		"log/select/logsql/field_values?field=level",
		"log/select/logsql/stream_field_values?field=level",
		"log/select/logsql/facets",
	}
	for _, p := range paths {
		if got := h.status(http.MethodGet, appRoot+"b/"+p, "bob-token"); got != http.StatusForbidden {
			t.Errorf("b/%s answered %d, want 403", p, got)
		}
	}
	if n := len(h.requests()); n != 0 {
		t.Fatalf("upstream saw %d requests for a denied application", n)
	}
	for _, p := range []string{"metrics/api/v1/labels", "log/select/logsql/query"} {
		if got := h.status(http.MethodGet, appRoot+"a/"+p, "bob-token"); got != http.StatusOK {
			t.Errorf("a/%s answered %d, want 200", p, got)
		}
	}
}

// An expression whose lookback cannot be bounded looks pods up from the
// configured lookback before the window.
func TestServerApplicationLookupUnknownLookback(t *testing.T) {
	h := newPolicyHarness(t, Config{}, appMetricsUpstream(nil, nil, http.StatusOK), appPolicy, nil)
	q := url.Values{"query": {"up @ end()"}, "time": {unix(testNow)}}
	if got := h.status(http.MethodGet, appRoot+"db.main/metrics/api/v1/query?"+q.Encode(), "good"); got != http.StatusOK {
		t.Fatalf("status = %d", got)
	}
	r := h.requests()[0]
	if want := formatTime(testNow.Add(-24 * time.Hour)); r.path != "/api/v1/label/pod/values" || r.form.Get("start") != want {
		t.Fatalf("lookup %s start = %s, want %s", r.path, r.form.Get("start"), want)
	}
}
