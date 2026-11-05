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

// appPolicy lets alice read every postgres in tenant-a and the namespace
// roots, as the view role does. bob and carol may read only postgres "a",
// as a grant narrowed with resourceNames does; carol may also read the
// namespace roots.
func appPolicy(user string, a *authorizationv1.ResourceAttributes) bool {
	if a.Namespace != "tenant-a" {
		return false
	}
	if a.Verb == "list" && a.Group == "cozystack.io" && (a.Resource == "logs" || a.Resource == "metrics") && a.Name == "" && a.Subresource == "" {
		return user == "alice" || user == "carol"
	}
	if a.Verb != "get" || a.Group != "apps.cozystack.io" || a.Resource != "postgreses" ||
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
		// One step past end, which vmselect rounds up to a whole step, and
		// two steps for each "(" that may be an implicit subquery.
		if r.form.Get("end") != formatTime(testNow.Add(5*time.Minute)) || r.form.Get("limit") != "101" {
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
		// A failed lookup would leave the application's own series out of
		// a successful answer, so it fails the request instead.
		{name: "pod lookup fails", pods: []string{"p"}, pvcs: []string{"v"}, podStatus: http.StatusServiceUnavailable, path: "api/v1/query?query=up", wantCode: http.StatusBadGateway},
		{name: "pod lookup times out at the store", pods: []string{"p"}, podStatus: http.StatusGatewayTimeout, path: "api/v1/query?query=up", wantCode: http.StatusGatewayTimeout},
		{name: "pods over the cap", pods: []string{"p", "q"}, cfg: Config{Metrics: MetricsConfig{App: AppScopeLimits{Lookback: time.Hour, MaxPods: 1, MaxPVCs: 1}}},
			path: "api/v1/query?query=up", wantCode: http.StatusUnprocessableEntity},
		{name: "over the cap with an empty name", pods: []string{"", "p"}, cfg: Config{Metrics: MetricsConfig{App: AppScopeLimits{Lookback: time.Hour, MaxPods: 1, MaxPVCs: 1}}},
			path: "api/v1/query?query=up", wantCode: http.StatusUnprocessableEntity},
		{name: "pvcs over the cap", pods: []string{"p"}, pvcs: []string{"v", "w"}, cfg: Config{Metrics: MetricsConfig{App: AppScopeLimits{Lookback: time.Hour, MaxPods: 1, MaxPVCs: 1}}},
			path: "api/v1/query?query=up", wantCode: http.StatusUnprocessableEntity},
		{name: "labels get the branches as match[]", pods: []string{"p"}, path: "api/v1/labels", wantCode: http.StatusOK, want: []string{wantIdentityBranch, podBranch}},
		{name: "series needs match[]", path: "api/v1/series", wantCode: http.StatusBadRequest},
		// An empty name would make pod=~"^(|p)$", which matches every
		// series without a pod label.
		{name: "empty pod name dropped", pods: []string{"", "p"}, path: "api/v1/query?query=up", wantCode: http.StatusOK, want: []string{wantIdentityBranch, podBranch}},
		{name: "only empty pod names", pods: []string{""}, path: "api/v1/query?query=up", wantCode: http.StatusOK, want: []string{wantIdentityBranch}},
		{name: "lookup status error", podBody: `{"status":"error","data":["p"]}`, path: "api/v1/query?query=up", wantCode: http.StatusBadGateway},
		{name: "truncated lookup answer", podBody: `{"status":"success","data":["p"`, path: "api/v1/query?query=up", wantCode: http.StatusBadGateway},
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
// through any endpoint of either root. Without read access to the
// namespace roots it does not reach "a" either: the pod and PVC branches
// match by name, and a name another application used earlier in the
// window would pull its series in.
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
		for _, denied := range []struct{ app, token string }{{"b", "bob-token"}, {"b", "carol-token"}, {"a", "bob-token"}} {
			if got := h.status(http.MethodGet, appRoot+denied.app+"/"+p, denied.token); got != http.StatusForbidden {
				t.Errorf("%s on %s/%s answered %d, want 403", denied.token, denied.app, p, got)
			}
		}
	}
	if n := len(h.requests()); n != 0 {
		t.Fatalf("upstream saw %d requests for a denied application", n)
	}
	for _, p := range []string{"metrics/api/v1/labels", "log/select/logsql/query"} {
		if got := h.status(http.MethodGet, appRoot+"a/"+p, "carol-token"); got != http.StatusOK {
			t.Errorf("a/%s answered %d, want 200", p, got)
		}
	}
}

// TestServerApplicationLookbackBeyondCap: the lookup reaches back at most
// --metrics-app-lookback, so a query that reads further back, or whose
// lookback is unknown, would miss pods that existed only earlier; it is
// refused rather than answered without their series.
func TestServerApplicationLookbackBeyondCap(t *testing.T) {
	cfg := Config{Metrics: MetricsConfig{App: AppScopeLimits{Lookback: time.Hour, MaxPods: 100, MaxPVCs: 100}}}
	h := newPolicyHarness(t, cfg, appMetricsUpstream(nil, nil, http.StatusOK), appPolicy, nil)
	// max_over_time(x[2h]) reads just past the 1h cap; rate(x[5i]) has an
	// unknown lookback.
	for _, query := range []string{"rate(x[7d])", "max_over_time(x[2h])", "rate(x[5i])"} {
		q := url.Values{"query": {query}, "time": {unix(testNow)}}
		if got := h.status(http.MethodGet, appRoot+"db.main/metrics/api/v1/query?"+q.Encode(), "good"); got != http.StatusUnprocessableEntity {
			t.Errorf("%s answered %d, want 422", query, got)
		}
	}
	if n := len(h.requests()); n != 0 {
		t.Fatalf("upstream saw %d requests for refused queries", n)
	}
	q := url.Values{"query": {"rate(x[5m])"}, "time": {unix(testNow)}}
	if got := h.status(http.MethodGet, appRoot+"db.main/metrics/api/v1/query?"+q.Encode(), "good"); got != http.StatusOK {
		t.Fatalf("rate(x[5m]) answered %d, want 200", got)
	}
	r := h.requests()[0]
	want := formatTime(buildMetrics(t, "api/v1/query", q).start)
	if r.path != "/api/v1/label/pod/values" || r.form.Get("start") != want {
		t.Fatalf("lookup %s start = %s, want the query's earliest read %s", r.path, r.form.Get("start"), want)
	}
}

// TestServerApplicationLookupCoversNegativeOffset evaluates two hours ago
// a selector shifted an hour forward: the pod lookup must reach the time
// of the samples, or a pod created in between is left out.
func TestServerApplicationLookupCoversNegativeOffset(t *testing.T) {
	h := newPolicyHarness(t, Config{}, appMetricsUpstream(nil, nil, http.StatusOK), appPolicy, nil)
	at := testNow.Add(-2 * time.Hour)
	q := url.Values{"query": {"container_cpu_usage_seconds_total offset -1h"}, "time": {unix(at)}}
	if got := h.status(http.MethodGet, appRoot+"db.main/metrics/api/v1/query?"+q.Encode(), "good"); got != http.StatusOK {
		t.Fatalf("status = %d", got)
	}
	r := h.requests()[0]
	if want := formatTime(at.Add(time.Hour)); r.path != "/api/v1/label/pod/values" || r.form.Get("end") != want {
		t.Fatalf("lookup %s end = %s, want %s", r.path, r.form.Get("end"), want)
	}
}

// TestServerApplicationRefusesUnboundedSampleTime refuses a query whose
// samples may lie at a time the lookup cannot be sized for, rather than
// answer with series silently left out.
func TestServerApplicationRefusesUnboundedSampleTime(t *testing.T) {
	h := newPolicyHarness(t, Config{}, appMetricsUpstream(nil, nil, http.StatusOK), appPolicy, nil)
	for _, query := range []string{"up @ 1700000000", "up offset -5i"} {
		q := url.Values{"query": {query}}
		if got := h.status(http.MethodGet, appRoot+"db.main/metrics/api/v1/query?"+q.Encode(), "good"); got != http.StatusUnprocessableEntity {
			t.Errorf("%s answered %d, want 422", query, got)
		}
	}
	if n := len(h.requests()); n != 0 {
		t.Fatalf("upstream saw %d requests for a refused query", n)
	}
	// The namespace root has no lookup to size and still serves it.
	if got := h.status(http.MethodGet, metricsRoot+"api/v1/query?"+url.Values{"query": {"up @ 1700000000"}}.Encode(), "good"); got != http.StatusOK {
		t.Fatalf("namespace root answered %d", got)
	}
}

// TestServerApplicationLookupCoversAlignedEnd: vmselect rounds a range
// query's end up to a whole step, so the lookup reaches one step past it.
func TestServerApplicationLookupCoversAlignedEnd(t *testing.T) {
	h := newPolicyHarness(t, Config{}, appMetricsUpstream(nil, nil, http.StatusOK), appPolicy, nil)
	end := testNow.Add(-90 * time.Minute)
	q := url.Values{"query": {"up"}, "start": {unix(end.Add(-6 * time.Hour))}, "end": {unix(end)}, "step": {"3600"}}
	if got := h.status(http.MethodGet, appRoot+"db.main/metrics/api/v1/query_range?"+q.Encode(), "good"); got != http.StatusOK {
		t.Fatalf("status = %d", got)
	}
	r := h.requests()[0]
	if want := formatTime(end.Add(time.Hour)); r.path != "/api/v1/label/pod/values" || r.form.Get("end") != want {
		t.Fatalf("lookup %s end = %s, want %s", r.path, r.form.Get("end"), want)
	}
}

// TestServerApplicationLookupDeadline: a lookup that misses the request
// deadline answers 504, whether the store sent nothing or stalled after
// its headers, and the query itself is not sent.
func TestServerApplicationLookupDeadline(t *testing.T) {
	for _, headers := range []bool{false, true} {
		h := newPolicyHarness(t, Config{RequestTimeout: 50 * time.Millisecond}, func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path != "/api/v1/label/pod/values" {
				appMetricsUpstream(nil, nil, http.StatusOK)(w, r)
				return
			}
			if headers {
				w.Header().Set("Content-Type", "application/json")
				_, _ = io.WriteString(w, `{"status":"success","data":[`)
				w.(http.Flusher).Flush()
			}
			<-r.Context().Done()
		}, appPolicy, nil)
		if got := h.status(http.MethodGet, appRoot+"db.main/metrics/api/v1/query?query=up", "good"); got != http.StatusGatewayTimeout {
			t.Fatalf("headers sent %v: status = %d, want 504", headers, got)
		}
		for _, r := range h.requests() {
			if !strings.HasPrefix(r.path, "/api/v1/label/") {
				t.Fatalf("headers sent %v: the query reached the store after a failed lookup", headers)
			}
		}
	}
}

// TestServerApplicationDefaultLookbackServesLongPanels: the lookback is
// only the refusal threshold, and the lookback estimate is a loose upper
// bound, so the default must serve a day's increase and a month-long
// panel at the step a dashboard picks for it.
func TestServerApplicationDefaultLookbackServesLongPanels(t *testing.T) {
	cfg := Config{Metrics: MetricsConfig{App: AppScopeLimits{Lookback: 30 * 24 * time.Hour, MaxPods: 100, MaxPVCs: 100}}}
	h := newPolicyHarness(t, cfg, appMetricsUpstream(nil, nil, http.StatusOK), appPolicy, nil)
	month := url.Values{"start": {unix(testNow.Add(-29 * 24 * time.Hour))}, "end": {unix(testNow)}}
	for _, tc := range []struct{ path, query, step string }{
		{"api/v1/query", "increase(x[24h])", ""},
		{"api/v1/query_range", "rate(x[5m])", "21600"},
		{"api/v1/query_range", "histogram_quantile(0.99, sum by (le) (rate(x[5m])))", "7200"},
	} {
		q := url.Values{"query": {tc.query}}
		if tc.step != "" {
			q.Set("step", tc.step)
			q.Set("start", month.Get("start"))
			q.Set("end", month.Get("end"))
		}
		if got := h.status(http.MethodGet, appRoot+"db.main/metrics/"+tc.path+"?"+q.Encode(), "good"); got != http.StatusOK {
			t.Errorf("%s step %s answered %d, want 200", tc.query, tc.step, got)
		}
	}
}

// TestServerApplicationLookupStoreErrors: vmselect answers 422 to any error
// without a status of its own (SendPrometheusError), so only its series
// limit, which a narrower window fixes, reaches the client as 422.
func TestServerApplicationLookupStoreErrors(t *testing.T) {
	for _, tc := range []struct {
		label, message string
		want           int
	}{
		{"pod", "the number of matching timeseries exceeds 300000; either narrow down the search or increase -search.max* command-line flag values", http.StatusUnprocessableEntity},
		{"persistentvolumeclaim", "the number of matching timeseries exceeds 300000; either narrow down the search", http.StatusUnprocessableEntity},
		{"pod", "cannot fetch label values from vmstorage nodes: the number of matching timeseries exceeds 300000", http.StatusUnprocessableEntity},
		{"pod", "timeout exceeded before starting the query processing: 30s", http.StatusGatewayTimeout},
		{"pod", "error when searching for label values: cannot obtain data from vmstorage", http.StatusBadGateway},
	} {
		h := newPolicyHarness(t, Config{}, func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/api/v1/label/"+tc.label+"/values" {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusUnprocessableEntity)
				_ = json.NewEncoder(w).Encode(map[string]string{"status": "error", "errorType": "422", "error": tc.message})
				return
			}
			appMetricsUpstream([]string{"p"}, nil, http.StatusOK)(w, r)
		}, appPolicy, nil)
		resp := h.do(http.MethodGet, appRoot+"db.main/metrics/api/v1/query?query=up", "good", nil, nil)
		body, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		if resp.StatusCode != tc.want {
			t.Errorf("%s lookup %q answered %d, want %d", tc.label, tc.message, resp.StatusCode, tc.want)
		}
		if tc.want == http.StatusUnprocessableEntity && !strings.Contains(string(body), "the number of matching timeseries exceeds 300000") {
			t.Errorf("the store's limit message did not reach the client: %q", body)
		}
		if strings.Contains(string(body), `"errorType"`) {
			t.Errorf("the store's raw JSON reached the client: %q", body)
		}
		// Other store errors may name vmstorage nodes; only the limit
		// message, which the client can act on, is passed on.
		if strings.Contains(string(body), "vmstorage") {
			t.Errorf("the store's error text reached the client: %q", body)
		}
	}
}

// TestServerApplicationRefusesEveryAt: no @ form can be bounded, and the
// answer says so rather than naming forms that would pass.
func TestServerApplicationRefusesEveryAt(t *testing.T) {
	h := newPolicyHarness(t, Config{}, appMetricsUpstream(nil, nil, http.StatusOK), appPolicy, nil)
	for _, query := range []string{"up @ end()", "up @ start()", "up @ 1700000000"} {
		resp := h.do(http.MethodGet, appRoot+"db.main/metrics/api/v1/query?"+url.Values{"query": {query}}.Encode(), "good", nil, nil)
		body, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusUnprocessableEntity {
			t.Errorf("%s answered %d, want 422", query, resp.StatusCode)
		}
		if strings.Contains(string(body), "start() or end()") {
			t.Errorf("%s: the refusal names @ forms as allowed: %q", query, body)
		}
	}
	if n := len(h.requests()); n != 0 {
		t.Fatalf("upstream saw %d requests for refused queries", n)
	}
}

// TestServerApplicationRefusesForwardReads: queries that read past the
// evaluation time by an amount the scan cannot bound answer 422 and send
// nothing upstream, rather than answer 200 without pods created in that
// time.
func TestServerApplicationRefusesForwardReads(t *testing.T) {
	h := newPolicyHarness(t, Config{}, appMetricsUpstream(nil, nil, http.StatusOK), appPolicy, nil)
	for _, query := range []string{
		"container_cpu_usage_seconds_total offset -1h-30m",
		"container_cpu_usage_seconds_total offset 1h-2h",
		"container_cpu_usage_seconds_total offset -1h$__interval",
		"rollup_candlestick(container_cpu_usage_seconds_total)",
	} {
		q := url.Values{"query": {query}, "time": {unix(testNow.Add(-2 * time.Hour))}}
		resp := h.do(http.MethodGet, appRoot+"db.main/metrics/api/v1/query?"+q.Encode(), "good", nil, nil)
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusUnprocessableEntity {
			t.Errorf("%s answered %d, want 422", query, resp.StatusCode)
		}
	}
	if n := len(h.requests()); n != 0 {
		t.Fatalf("upstream saw %d requests for refused queries", n)
	}
}

// TestServerApplicationLookupCoversSubquery: a pod created during the two
// subquery steps past the evaluation time can show up through range_last,
// so the lookup reaches that far.
func TestServerApplicationLookupCoversSubquery(t *testing.T) {
	h := newPolicyHarness(t, Config{}, appMetricsUpstream(nil, nil, http.StatusOK), appPolicy, nil)
	at := testNow.Add(-6 * time.Hour)
	q := url.Values{"query": {"max_over_time(range_last(container_cpu_usage_seconds_total)[1h:1h])"}, "time": {unix(at)}}
	if got := h.status(http.MethodGet, appRoot+"db.main/metrics/api/v1/query?"+q.Encode(), "good"); got != http.StatusOK {
		t.Fatalf("status = %d", got)
	}
	r := h.requests()[0]
	end, err := time.Parse(time.RFC3339, r.form.Get("end"))
	if err != nil || r.path != "/api/v1/label/pod/values" || end.Before(at.Add(2*time.Hour)) {
		t.Fatalf("lookup %s end = %s, want at least %s", r.path, r.form.Get("end"), formatTime(at.Add(2*time.Hour)))
	}
}

// TestServerApplicationNamesUnreadableSubqueryStep: the refusal names the
// cause when a subquery step is what the scan cannot read.
func TestServerApplicationNamesUnreadableSubqueryStep(t *testing.T) {
	h := newPolicyHarness(t, Config{}, appMetricsUpstream(nil, nil, http.StatusOK), appPolicy, nil)
	resp := h.do(http.MethodGet, appRoot+"db.main/metrics/api/v1/query?"+url.Values{"query": {"max_over_time(x[1h:5i])"}}.Encode(), "good", nil, nil)
	body, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusUnprocessableEntity || !strings.Contains(string(body), "subquery step") {
		t.Fatalf("answered %d %q, want 422 naming the subquery step", resp.StatusCode, body)
	}
}

// TestServerApplicationLookupCoversLatencyOffset: vmselect evaluates an
// instant query whose time is within 30s of its own clock at its clock
// minus 30s (prometheus.go, queryOffset), so for a time slightly in the
// future it reads from earlier than time minus 30s. The lookup starts
// early enough for any evaluation instant in (time-60s, time].
func TestServerApplicationLookupCoversLatencyOffset(t *testing.T) {
	h := newPolicyHarness(t, Config{}, appMetricsUpstream(nil, nil, http.StatusOK), appPolicy, nil)
	at := testNow.Add(29 * time.Second)
	q := url.Values{"query": {"container_cpu_usage_seconds_total"}, "time": {at.Format(time.RFC3339)}, "step": {"1s"}}
	if got := h.status(http.MethodGet, appRoot+"db.main/metrics/api/v1/query?"+q.Encode(), "good"); got != http.StatusOK {
		t.Fatalf("status = %d", got)
	}
	// The earliest read of the query evaluated at the earliest instant.
	lookback, _ := queryLookback("container_cpu_usage_seconds_total", time.Second)
	want := at.Add(-2*latencyOffset - lookback)
	r := h.requests()[0]
	start, err := time.Parse(time.RFC3339, r.form.Get("start"))
	if err != nil || start.After(want) {
		t.Fatalf("lookup start = %s, want at most %s", r.form.Get("start"), formatTime(want))
	}
}

// TestServerApplicationRefusesBackquoteEscapeTail: an escaped backquote
// inside a quoted label name does not end the string in metricsql, so the
// escaped rollup_candlestick after it must still be refused.
func TestServerApplicationRefusesBackquoteEscapeTail(t *testing.T) {
	h := newPolicyHarness(t, Config{}, appMetricsUpstream(nil, nil, http.StatusOK), appPolicy, nil)
	for _, query := range []string{
		"container_cpu_usage_seconds_total{`\\``=\"\"} or rollup\\_candlestick(container_cpu_usage_seconds_total offset -5m+1h)",
		"{`\\`a`} or rollup\\_candlestick(container_cpu_usage_seconds_total offset -5m+1h)",
	} {
		q := url.Values{"query": {query}, "time": {unix(testNow.Add(-6 * time.Hour))}}
		if got := h.status(http.MethodGet, appRoot+"db.main/metrics/api/v1/query?"+q.Encode(), "good"); got != http.StatusUnprocessableEntity {
			t.Errorf("%s answered %d, want 422", query, got)
		}
	}
	if n := len(h.requests()); n != 0 {
		t.Fatalf("upstream saw %d requests for refused queries", n)
	}
}
