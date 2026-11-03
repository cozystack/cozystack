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
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// AppScopeLimits bound the pod and PVC lookup of the application metrics
// root.
type AppScopeLimits struct {
	// Lookback is how far before the window the lookup reaches at most, so
	// pods gone before the window but inside an expression's range count.
	Lookback time.Duration
	MaxPods  int
	MaxPVCs  int
}

// maxLookupBytes bounds a lookup answer; the caps keep a valid one far
// below it.
const maxLookupBytes = 8 << 20

// The lineage labels as the scrape class writes them on series, and as
// kube-state-metrics writes them on kube_pod_labels and
// kube_persistentvolumeclaim_labels.
const (
	seriesKindLabel = "apps_cozystack_io_application_kind"
	seriesNameLabel = "apps_cozystack_io_application_name"
	ksmKindLabel    = "label_" + seriesKindLabel
	ksmNameLabel    = "label_" + seriesNameLabel
)

func identitySelector(ns, kindLabel, nameLabel, kind, name string) string {
	return "{namespace=" + strconv.Quote(ns) + "," + kindLabel + "=" + strconv.Quote(kind) + "," + nameLabel + "=" + strconv.Quote(name) + "}"
}

// nameBranch matches label against any of names within ns. With no names
// it is "": a branch of pod=~"^()$" would match every series without a pod
// label.
func nameBranch(ns, label string, names []string) string {
	if len(names) == 0 {
		return ""
	}
	quoted := make([]string, len(names))
	for i, n := range names {
		quoted[i] = regexp.QuoteMeta(n)
	}
	return "{namespace=" + strconv.Quote(ns) + "," + label + "=~" + strconv.Quote("^("+strings.Join(quoted, "|")+")$") + "}"
}

// applicationSelectors is the application scope: series stamped with the
// application's identity, plus series of its pods and PVCs for those not
// stamped (cAdvisor, kubelet volume stats). Every branch carries the
// namespace, so their OR stays inside it.
//
// Within the namespace, both the identity and the pod and PVC lists come
// from lineage labels on pods and PVCs. They separate applications for a
// grant narrowed by resourceNames only while whoever creates a pod or PVC
// cannot choose those labels; the lineage webhook keeps them as set on an
// object it finds no owner for, and the logs scope rests on the same.
func applicationSelectors(ns, kind, name string, pods, pvcs []string) []string {
	out := []string{identitySelector(ns, seriesKindLabel, seriesNameLabel, kind, name)}
	for _, b := range []string{nameBranch(ns, "pod", pods), nameBranch(ns, "persistentvolumeclaim", pvcs)} {
		if b != "" {
			out = append(out, b)
		}
	}
	return out
}

// applicationScope looks the application's pods and PVCs up in store over
// the request window, reaching back by the query's own lookback up to the
// configured limit. A failed lookup drops its branch, which only narrows
// the answer; a list over its cap is refused, because truncating it would
// silently drop series.
func (s *Server) applicationScope(ctx context.Context, r *http.Request, store string, req metricsRequest, ns, kind, name string) ([]string, *HTTPError) {
	lim := s.cfg.Metrics.App
	start := req.from.Add(-lim.Lookback)
	if req.start.After(start) {
		start = req.start
	}
	var lists [2][]string
	for i, l := range []struct {
		label, metric string
		max           int
	}{{"pod", "kube_pod_labels", lim.MaxPods}, {"persistentvolumeclaim", "kube_persistentvolumeclaim_labels", lim.MaxPVCs}} {
		match := l.metric + identitySelector(ns, ksmKindLabel, ksmNameLabel, kind, name)
		names, err := s.lookupNames(ctx, r, store, l.label, match, ns, start, req.to, l.max)
		if err != nil {
			slog.Warn("application lookup failed; its branch is left out", "label", l.label, "namespace", ns, "kind", kind, "name", name, "error", err)
			continue
		}
		if len(names) > l.max {
			return nil, errorf(http.StatusUnprocessableEntity, "the application has more than %d %ss in this window; use a shorter range", l.max, l.label)
		}
		lists[i] = names
	}
	return applicationSelectors(ns, kind, name, lists[0], lists[1]), nil
}

// lookupNames asks for at most max+1 values, enough to tell a list over
// the cap without reading all of it.
func (s *Server) lookupNames(ctx context.Context, r *http.Request, store, label, match, ns string, start, end time.Time, max int) ([]string, error) {
	params := url.Values{}
	params.Set("match[]", match)
	params.Set("extra_filters[]", namespaceSelector(ns))
	params.Set("start", formatTime(start))
	params.Set("end", formatTime(end))
	params.Set("limit", strconv.Itoa(max+1))
	resp, herr := s.post(ctx, r, s.cfg.Metrics.UpstreamURL(store)+"/api/v1/label/"+label+"/values", params, "metrics store", nil)
	if herr != nil {
		return nil, herr
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return nil, errorf(resp.StatusCode, "metrics store answered %s", resp.Status)
	}
	var body struct {
		Status string   `json:"status"`
		Data   []string `json:"data"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, maxLookupBytes)).Decode(&body); err != nil {
		return nil, err
	}
	if body.Status != "success" {
		return nil, errorf(http.StatusBadGateway, "metrics store answered status %q", body.Status)
	}
	names := body.Data[:0]
	for _, n := range body.Data {
		if n != "" {
			names = append(names, n)
		}
	}
	return names, nil
}
