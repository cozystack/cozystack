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
	"slices"
	"testing"

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
