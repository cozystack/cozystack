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
	"testing"

	authorizationv1 "k8s.io/api/authorization/v1"
)

func TestParseRoute(t *testing.T) {
	tests := []struct {
		name     string
		path     string
		wantCode int
		want     Route
	}{
		{
			name: "namespace logs",
			path: "/apis/cozystack.io/v1alpha1/namespaces/tenant-a/logs/select/logsql/query",
			want: Route{Kind: NamespaceLogs, Namespace: "tenant-a", Endpoint: "select/logsql/query"},
		},
		{
			name: "namespace logs root without endpoint",
			path: "/apis/cozystack.io/v1alpha1/namespaces/tenant-a/logs/",
			want: Route{Kind: NamespaceLogs, Namespace: "tenant-a"},
		},
		{
			name: "namespace metrics",
			path: "/apis/cozystack.io/v1alpha1/namespaces/tenant-a/metrics/api/v1/query",
			want: Route{Kind: NamespaceMetrics, Namespace: "tenant-a", Endpoint: "api/v1/query"},
		},
		{
			name: "application log",
			path: "/apis/apps.cozystack.io/v1alpha1/namespaces/tenant-a/postgreses/db.main/log/select/logsql/tail",
			want: Route{Kind: ApplicationLog, Namespace: "tenant-a", Plural: "postgreses", Name: "db.main", Endpoint: "select/logsql/tail"},
		},
		{
			name: "application metrics",
			path: "/apis/apps.cozystack.io/v1alpha1/namespaces/tenant-a/postgreses/db/metrics/api/v1/query_range",
			want: Route{Kind: ApplicationMetrics, Namespace: "tenant-a", Plural: "postgreses", Name: "db", Endpoint: "api/v1/query_range"},
		},
		{name: "unknown group", path: "/apis/core.cozystack.io/v1alpha1/namespaces/a/logs/x", wantCode: http.StatusNotFound},
		{name: "unknown version", path: "/apis/cozystack.io/v1/namespaces/a/logs/x", wantCode: http.StatusNotFound},
		{name: "unknown namespace resource", path: "/apis/cozystack.io/v1alpha1/namespaces/a/events/x", wantCode: http.StatusNotFound},
		{name: "unknown app subresource", path: "/apis/apps.cozystack.io/v1alpha1/namespaces/a/postgreses/db/exec/x", wantCode: http.StatusNotFound},
		{name: "core api", path: "/api/v1/namespaces/a/pods", wantCode: http.StatusNotFound},
		{name: "truncated", path: "/apis/cozystack.io/v1alpha1/namespaces/a", wantCode: http.StatusNotFound},
		{name: "empty segment", path: "/apis/cozystack.io/v1alpha1/namespaces/a/logs/select//logsql/query", wantCode: http.StatusBadRequest},
		{name: "leading double slash", path: "//apis/cozystack.io/v1alpha1/namespaces/a/logs/x", wantCode: http.StatusBadRequest},
		{name: "dot dot segment", path: "/apis/cozystack.io/v1alpha1/namespaces/b/../a/logs/x", wantCode: http.StatusBadRequest},
		{name: "dot dot under root", path: "/apis/cozystack.io/v1alpha1/namespaces/a/logs/../metrics/x", wantCode: http.StatusBadRequest},
		{name: "dot dot in unknown prefix", path: "/foo/../apis/cozystack.io/v1alpha1/namespaces/a/logs/x", wantCode: http.StatusBadRequest},
		{name: "dot segment", path: "/apis/cozystack.io/v1alpha1/namespaces/a/./logs/x", wantCode: http.StatusBadRequest},
		{name: "encoded slash", path: "/apis/cozystack.io/v1alpha1/namespaces/a%2Fb/logs/x", wantCode: http.StatusBadRequest},
		{name: "encoded slash lowercase", path: "/apis/cozystack.io/v1alpha1/namespaces/a%2fb/logs/x", wantCode: http.StatusBadRequest},
		{name: "encoded dot dot", path: "/apis/cozystack.io/v1alpha1/namespaces/b/%2e%2e/a/logs/x", wantCode: http.StatusBadRequest},
		{name: "encoded dot dot uppercase", path: "/apis/cozystack.io/v1alpha1/namespaces/b/%2E%2E/a/logs/x", wantCode: http.StatusBadRequest},
		{name: "backslash", path: `/apis/cozystack.io/v1alpha1/namespaces/b\..\a/logs/x`, wantCode: http.StatusBadRequest},
		{name: "uppercase namespace", path: "/apis/cozystack.io/v1alpha1/namespaces/Tenant/logs/x", wantCode: http.StatusBadRequest},
		{name: "namespace with dot", path: "/apis/cozystack.io/v1alpha1/namespaces/a.b/logs/x", wantCode: http.StatusBadRequest},
		{name: "namespace with quote", path: `/apis/cozystack.io/v1alpha1/namespaces/a"b/logs/x`, wantCode: http.StatusBadRequest},
		{name: "name with logsql metacharacters", path: `/apis/apps.cozystack.io/v1alpha1/namespaces/a/postgreses/db"|union/log/x`, wantCode: http.StatusBadRequest},
		{name: "name with braces", path: "/apis/apps.cozystack.io/v1alpha1/namespaces/a/postgreses/{db}/log/x", wantCode: http.StatusBadRequest},
		{name: "plural uppercase", path: "/apis/apps.cozystack.io/v1alpha1/namespaces/a/Postgreses/db/log/x", wantCode: http.StatusBadRequest},
		{name: "plural with dot", path: "/apis/apps.cozystack.io/v1alpha1/namespaces/a/postgres.es/db/log/x", wantCode: http.StatusBadRequest},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ParseRoute(tt.path)
			if tt.wantCode != 0 {
				if err == nil {
					t.Fatalf("ParseRoute(%q) = %+v, want error with code %d", tt.path, got, tt.wantCode)
				}
				if err.Code != tt.wantCode {
					t.Fatalf("ParseRoute(%q) code = %d (%s), want %d", tt.path, err.Code, err.Message, tt.wantCode)
				}
				return
			}
			if err != nil {
				t.Fatalf("ParseRoute(%q) error = %v", tt.path, err)
			}
			if got != tt.want {
				t.Fatalf("ParseRoute(%q) = %+v, want %+v", tt.path, got, tt.want)
			}
		})
	}
}

func TestRouteResourceAttributes(t *testing.T) {
	tests := []struct {
		route Route
		want  authorizationv1.ResourceAttributes
	}{
		{
			route: Route{Kind: NamespaceLogs, Namespace: "a"},
			want:  authorizationv1.ResourceAttributes{Verb: "list", Group: "cozystack.io", Resource: "logs", Namespace: "a"},
		},
		{
			route: Route{Kind: NamespaceMetrics, Namespace: "a"},
			want:  authorizationv1.ResourceAttributes{Verb: "list", Group: "cozystack.io", Resource: "metrics", Namespace: "a"},
		},
		{
			route: Route{Kind: ApplicationLog, Namespace: "a", Plural: "postgreses", Name: "db"},
			want:  authorizationv1.ResourceAttributes{Verb: "get", Group: "apps.cozystack.io", Resource: "postgreses", Subresource: "log", Name: "db", Namespace: "a"},
		},
		{
			route: Route{Kind: ApplicationMetrics, Namespace: "a", Plural: "postgreses", Name: "db"},
			want:  authorizationv1.ResourceAttributes{Verb: "get", Group: "apps.cozystack.io", Resource: "postgreses", Subresource: "metrics", Name: "db", Namespace: "a"},
		},
	}
	for _, tt := range tests {
		if got := tt.route.ResourceAttributes(); got != tt.want {
			t.Errorf("%v.ResourceAttributes() = %+v, want %+v", tt.route.Kind, got, tt.want)
		}
	}
}
