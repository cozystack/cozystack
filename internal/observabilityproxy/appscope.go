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
	"errors"
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
// Within the namespace this narrows, it does not isolate: the identity and
// the pod and PVC lists come from lineage labels that the lineage webhook
// keeps as set on an object it finds no owner for, and the pod and PVC
// branches match by name, so a name another application held within the
// lookup window pulls that application's series in. Route.Reviews makes
// the application roots require read access to the namespace root too.
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
// the samples the request reads: back by the query's own lookback, and
// forward to the latest sample a negative offset or a subquery's
// evaluation past its end reads. A query that reads
// further back than the configured lookback, or whose earliest or latest
// sample is unknown, a failed lookup, and a list over its cap all fail the
// request, because a successful answer would silently miss the
// application's series.
func (s *Server) applicationScope(ctx context.Context, r *http.Request, store string, req metricsRequest, ns, kind, name string) ([]string, *HTTPError) {
	if req.last.IsZero() {
		return nil, errorf(http.StatusUnprocessableEntity, "the query reads samples at a time the application scope cannot cover (an @ anywhere in the query, a backslash outside a string, rollup_candlestick, WITH with a negative offset, an offset that is not a plain duration, or a subquery step that is not one); use the namespace root")
	}
	lim := s.cfg.Metrics.App
	if req.start.IsZero() || req.start.Before(req.from.Add(-lim.Lookback)) {
		return nil, errorf(http.StatusUnprocessableEntity, "the query reads further back than the application scope looks up pods (%s before the window, or an unknown lookback); use a shorter window or the namespace root", lim.Lookback)
	}
	start := req.start
	// vmselect evaluates an instant query whose time is within 30s of its
	// own clock at that clock minus 30s (queryOffset in prometheus.go), so
	// the evaluation instant lies anywhere in (time-60s, time], while
	// req.start assumes time-30s. One more latency offset covers it without
	// trusting the proxy's clock to match vmselect's.
	if req.path == "api/v1/query" {
		start = start.Add(-latencyOffset)
	}
	var lists [2][]string
	for i, l := range []struct {
		label, metric string
		max           int
	}{{"pod", "kube_pod_labels", lim.MaxPods}, {"persistentvolumeclaim", "kube_persistentvolumeclaim_labels", lim.MaxPVCs}} {
		match := l.metric + identitySelector(ns, ksmKindLabel, ksmNameLabel, kind, name)
		names, err := s.lookupNames(ctx, r, store, l.label, match, ns, start, req.last, l.max)
		if err != nil {
			slog.Warn("application lookup failed", "label", l.label, "namespace", ns, "kind", kind, "name", name, "error", err)
			// The deadline can also expire while the answer is read, after
			// post has handed it over.
			if errors.Is(context.Cause(ctx), context.DeadlineExceeded) {
				return nil, errorf(http.StatusGatewayTimeout, "the application's %s lookup did not answer in time", l.label)
			}
			if herr, ok := err.(*HTTPError); ok && (herr.Code == http.StatusUnprocessableEntity || herr.Code == http.StatusGatewayTimeout) {
				return nil, herr
			}
			return nil, errorf(http.StatusBadGateway, "the application's %s lookup failed", l.label)
		}
		if len(names) > l.max {
			return nil, errorf(http.StatusUnprocessableEntity, "the application has more than %d %ss in this window; use a shorter range", l.max, l.label)
		}
		lists[i] = names
	}
	return applicationSelectors(ns, kind, name, lists[0], lists[1]), nil
}

// lookupNames asks for at most limit+1 values, enough to tell a list over
// the cap without reading all of it.
func (s *Server) lookupNames(ctx context.Context, r *http.Request, store, label, match, ns string, start, end time.Time, limit int) ([]string, error) {
	params := url.Values{}
	params.Set("match[]", match)
	params.Set("extra_filters[]", namespaceSelector(ns))
	params.Set("start", formatTime(start))
	params.Set("end", formatTime(end))
	params.Set("limit", strconv.Itoa(limit+1))
	resp, herr := s.post(ctx, r, s.cfg.Metrics.UpstreamURL(store)+"/api/v1/label/"+label+"/values", params, "metrics store", nil)
	if herr != nil {
		return nil, herr
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode == http.StatusUnprocessableEntity {
		return nil, lookupRefusal(label, resp.Body)
	}
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
	if len(body.Data) > limit {
		// Over the cap before dropping empty values: the store cut the
		// list at limit+1, so a shorter filtered list could hide names.
		return body.Data, nil
	}
	names := body.Data[:0]
	for _, n := range body.Data {
		if n != "" {
			names = append(names, n)
		}
	}
	return names, nil
}

// seriesLimitError is the message vmstorage gives when a search matches
// more series than the -search.max* flags allow (lib/storage/index_db.go);
// cluster vmselect wraps it in its own context, hence a substring match.
const seriesLimitError = "the number of matching timeseries exceeds"

// lookupRefusal reads a vmselect 422. vmselect answers 422 to any error
// without a status of its own (httpserver.SendPrometheusError), storage
// failures and its own timeout included, so only the series limit, which
// a narrower window fixes, stays a 422 for the client.
func lookupRefusal(label string, body io.Reader) *HTTPError {
	var answer struct {
		Error string `json:"error"`
	}
	_ = json.NewDecoder(io.LimitReader(body, 64<<10)).Decode(&answer)
	switch {
	case strings.Contains(answer.Error, seriesLimitError):
		// From the limit on: what precedes it is cluster vmselect's context,
		// which can name vmstorage nodes.
		limit := answer.Error[strings.Index(answer.Error, seriesLimitError):]
		return errorf(http.StatusUnprocessableEntity, "the application's %s lookup: %s", label, limit)
	case strings.Contains(answer.Error, "timeout exceeded"):
		return errorf(http.StatusGatewayTimeout, "the application's %s lookup did not answer in time", label)
	default:
		// The text may name vmstorage nodes, so it stays in the log.
		slog.Warn("application lookup refused by the store", "label", label, "error", answer.Error)
		return errorf(http.StatusBadGateway, "the application's %s lookup failed", label)
	}
}
