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
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"testing"
	"time"
)

const wantIdentityBranch = `{namespace="tenant-a",apps_cozystack_io_application_kind="Postgres",apps_cozystack_io_application_name="db.main"}`

func TestApplicationSelectors(t *testing.T) {
	tests := []struct {
		name       string
		pods, pvcs []string
		want       []string
	}{
		{"identity only", nil, nil, []string{wantIdentityBranch}},
		{"pods", []string{"db-main-1", "db.main-2"}, nil, []string{
			wantIdentityBranch,
			`{namespace="tenant-a",pod=~"^(db-main-1|db\\.main-2)$"}`,
		}},
		{"pvcs", nil, []string{"data-db-main-1"}, []string{
			wantIdentityBranch,
			`{namespace="tenant-a",persistentvolumeclaim=~"^(data-db-main-1)$"}`,
		}},
		{"all three", []string{"p"}, []string{"v"}, []string{
			wantIdentityBranch,
			`{namespace="tenant-a",pod=~"^(p)$"}`,
			`{namespace="tenant-a",persistentvolumeclaim=~"^(v)$"}`,
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := applicationSelectors("tenant-a", "Postgres", "db.main", tt.pods, tt.pvcs); !slices.Equal(got, tt.want) {
				t.Fatalf("got  %q\nwant %q", got, tt.want)
			}
		})
	}
}

// DNS-1123 keeps most metacharacters out of names, but the names come
// back from the store, so the encoder must not rely on that.
func TestNameBranchEscapes(t *testing.T) {
	names := []string{`a.b`, `x|.*`, `)|(.*`, `q"} or {namespace=~".+`, `back\slash`, `$^+?[]{}`}
	sel := nameBranch("tenant-a", "pod", names)
	m := regexp.MustCompile(`^\{namespace="tenant-a",pod=~(".*")\}$`).FindStringSubmatch(sel)
	if m == nil {
		t.Fatalf("selector %s is not one namespace and one pod matcher", sel)
	}
	pattern, err := strconv.Unquote(m[1])
	if err != nil {
		t.Fatalf("pod matcher %s does not unquote: %v", m[1], err)
	}
	re := regexp.MustCompile(pattern)
	for _, n := range names {
		if !re.MatchString(n) {
			t.Errorf("%q does not match its own branch", n)
		}
	}
	for _, other := range []string{"axb", "x", "anything", "tenant-b", "", `back\\slash`} {
		if re.MatchString(other) {
			t.Errorf("branch matches %q", other)
		}
	}
	if nameBranch("tenant-a", "pod", nil) != "" {
		t.Error("an empty list produced a branch, which would match series without the label")
	}
}

func TestMetricsRequestScope(t *testing.T) {
	branches := []string{"{a}", "{b}"}
	labels := buildMetrics(t, "api/v1/labels", url.Values{})
	labels.setScope(branches)
	if got := labels.params["extra_filters[]"]; !slices.Equal(got, branches) {
		t.Errorf("extra_filters[] = %q", got)
	}
	if got := labels.params["match[]"]; !slices.Equal(got, branches) {
		t.Errorf("default match[] = %q, want the branches", got)
	}
	client := buildMetrics(t, "api/v1/labels", url.Values{"match[]": {"up"}})
	client.setScope(branches)
	if got := client.params["match[]"]; !slices.Equal(got, []string{"up"}) {
		t.Errorf("client match[] replaced: %q", got)
	}
	query := buildMetrics(t, "api/v1/query", url.Values{"query": {"up"}})
	query.setScope(branches)
	if query.params.Has("match[]") || !slices.Equal(query.params["extra_filters[]"], branches) {
		t.Errorf("query params = %v", query.params)
	}
	if !query.from.Equal(testNow) || !query.to.Equal(testNow) {
		t.Errorf("instant window = [%s, %s], want now", query.from, query.to)
	}
	rng := buildMetrics(t, "api/v1/query_range", url.Values{"query": {"up"}, "start": {unix(testNow.Add(-time.Hour))}, "end": {unix(testNow)}, "step": {"60"}})
	if !rng.from.Equal(testNow.Add(-time.Hour)) || !rng.to.Equal(testNow) {
		t.Errorf("range window = [%s, %s]", rng.from, rng.to)
	}
}
