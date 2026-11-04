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
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/prometheus/client_golang/prometheus"
)

// tailChunkBytes bounds the memory one tail holds: a longer line is passed
// on in pieces and flushed once its end arrives.
const tailChunkBytes = 64 << 10

// errTailIdle cancels a tail that received nothing for the idle timeout;
// it wraps DeadlineExceeded so a store that never answered reads as 504.
var errTailIdle = fmt.Errorf("tail idle: %w", context.DeadlineExceeded)

// errAborted ends a response whose status line already went out by
// cutting the connection, so a client sees a broken body instead of a
// complete one with lines missing.
var errAborted = &HTTPError{Code: http.StatusBadGateway, Message: "response aborted"}

// maxFormBytes bounds a POST body; LogsQL queries are a few KiB at most.
const maxFormBytes = 1 << 20

// Config is the runtime configuration of the proxy handler.
type Config struct {
	ClusterDomain string
	// UpstreamURL maps a store namespace to the vlselect base URL; nil
	// means the in-cluster Service of the "generic" logs storage.
	UpstreamURL     func(store string) string
	Logs            LogsLimits
	Metrics         MetricsConfig
	Limiter         LimiterConfig
	RequestTimeout  time.Duration
	TailIdleTimeout time.Duration
}

// StoreLookup resolves a request namespace to the namespace of its store.
type StoreLookup interface {
	Resolve(namespace string) (string, error)
}

// Server serves the tenant observability roots.
type Server struct {
	cfg      Config
	auth     *Authorizer
	stores   StoreLookup
	kinds    KindLookup
	client   *http.Client
	limits   *limiter
	now      func() time.Time
	requests *prometheus.CounterVec
}

func NewServer(cfg Config, auth *Authorizer, stores StoreLookup, kinds KindLookup, reg prometheus.Registerer) *Server {
	if cfg.UpstreamURL == nil {
		domain := cfg.ClusterDomain
		cfg.UpstreamURL = func(store string) string {
			return fmt.Sprintf("http://vlselect-generic.%s.svc.%s:9471", store, domain)
		}
	}
	if cfg.Metrics.UpstreamURL == nil {
		domain, root := cfg.ClusterDomain, cfg.Metrics.RootTenant
		cfg.Metrics.UpstreamURL = func(store string) string {
			return fmt.Sprintf("http://vmselect-%s.%s.svc.%s:8481/select/0/prometheus", store, root, domain)
		}
	}
	s := &Server{
		cfg:    cfg,
		auth:   auth,
		stores: stores,
		kinds:  kinds,
		// No client timeout: it would cut live tails. Non-tail requests are
		// bounded by a context deadline instead.
		client: &http.Client{Transport: http.DefaultTransport.(*http.Transport).Clone()},
		limits: newLimiter(cfg.Limiter),
		now:    time.Now,
		requests: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "observability_proxy_requests_total",
			Help: "Requests served, by route kind and status code.",
		}, []string{"route", "code"}),
	}
	if reg != nil {
		reg.MustRegister(s.requests, prometheus.NewGaugeFunc(prometheus.GaugeOpts{
			Name: "observability_proxy_open_tails",
			Help: "Live tails currently open.",
		}, func() float64 { return float64(s.limits.openTails()) }))
	}
	return s
}

type statusRecorder struct {
	http.ResponseWriter
	code int
	// bodyRead is set once readForm has read the whole body.
	bodyRead bool
}

func (r *statusRecorder) WriteHeader(code int) {
	if r.code == 0 {
		r.code = code
	}
	r.ResponseWriter.WriteHeader(code)
}

func (r *statusRecorder) Write(b []byte) (int, error) {
	if r.code == 0 {
		r.code = http.StatusOK
	}
	return r.ResponseWriter.Write(b)
}

func (r *statusRecorder) Unwrap() http.ResponseWriter { return r.ResponseWriter }

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	rec := &statusRecorder{ResponseWriter: w}
	// The listener has no ReadTimeout, which would cut tails, so the body
	// is bounded from here; readForm lifts the deadline once it has read
	// the body. A refusal sent before that leaves the deadline in place.
	if s.cfg.RequestTimeout > 0 {
		_ = http.NewResponseController(w).SetReadDeadline(time.Now().Add(s.cfg.RequestTimeout))
	}
	route, herr := ParseRoute(r.URL.EscapedPath())
	if herr == nil {
		herr = s.serve(rec, r, route)
	}
	if herr != nil && herr != errAborted {
		// net/http drains up to 256 KiB of an unread body before it sends
		// the headers; closing the connection instead answers a refusal at
		// once rather than after a body that may never come.
		if r.ContentLength != 0 && !rec.bodyRead {
			rec.Header().Set("Connection", "close")
		}
		writeError(rec, herr)
	}
	s.requests.WithLabelValues(route.Kind.String(), strconv.Itoa(rec.code)).Inc()
	if herr == errAborted {
		panic(http.ErrAbortHandler)
	}
}

func writeError(w http.ResponseWriter, e *HTTPError) {
	if e.Code == http.StatusUnauthorized {
		w.Header().Set("WWW-Authenticate", `Bearer realm="cozystack"`)
	}
	http.Error(w, e.Message, e.Code)
}

func (s *Server) serve(w http.ResponseWriter, r *http.Request, route Route) *HTTPError {
	user, herr := s.authorize(r, route)
	if herr != nil {
		return herr
	}
	if r.Method != http.MethodGet && r.Method != http.MethodPost {
		w.Header().Set("Allow", "GET, POST")
		return errorf(http.StatusMethodNotAllowed, "method not allowed")
	}
	if wait, ok := s.limits.allowRate(user); !ok {
		w.Header().Set("Retry-After", strconv.FormatInt(int64(math.Ceil(wait.Seconds())), 10))
		return errorf(http.StatusTooManyRequests, "request rate limit exceeded")
	}
	var kind string
	if route.Kind == ApplicationLog || route.Kind == ApplicationMetrics {
		if kind, herr = s.applicationKind(route.Plural); herr != nil {
			return herr
		}
	}
	if route.Kind == NamespaceMetrics || route.Kind == ApplicationMetrics {
		return s.serveMetrics(w, r, route, user, kind)
	}
	endpoint, ok := strings.CutPrefix(route.Endpoint, "select/logsql/")
	if !ok {
		return errorf(http.StatusNotFound, "not found")
	}
	tail := endpoint == "tail"
	acquire := s.limits.acquire
	if tail {
		acquire = s.limits.acquireTail
	}
	release, ok := acquire(user)
	if !ok {
		return tooManyConcurrent(w)
	}
	defer release()
	if herr := s.readForm(w, r); herr != nil {
		return herr
	}
	params, herr := BuildLogsParams(endpoint, r.Form, route.Namespace, s.now(), s.cfg.Logs)
	if herr != nil {
		return herr
	}
	if kind != "" {
		params.Add("extra_stream_filters", applicationStreamFilter(kind, route.Name))
	}

	store, err := s.stores.Resolve(route.Namespace)
	if err != nil {
		slog.Error("resolving log store", "namespace", route.Namespace, "error", err)
		return errorf(http.StatusServiceUnavailable, "cannot resolve the log store")
	}

	ctx, cancel := context.WithCancelCause(r.Context())
	defer cancel(nil)
	limit := s.cfg.RequestTimeout
	var idle *time.Timer
	if tail {
		// Started before the request, so a store that never sends the
		// headers is bounded as well as one that stops sending lines.
		if limit = s.cfg.TailIdleTimeout; limit > 0 {
			idle = time.AfterFunc(limit, func() { cancel(errTailIdle) })
			defer idle.Stop()
		}
	} else if limit > 0 {
		var stop context.CancelFunc
		ctx, stop = context.WithTimeout(ctx, limit)
		defer stop()
	}
	tenant := http.Header{}
	tenant.Set("AccountID", "0")
	tenant.Set("ProjectID", "0")
	if tail {
		// vlselect holds even the headers of a gzip-encoded tail until the
		// first line; an explicit value stops the transport from asking for gzip.
		tenant.Set("Accept-Encoding", "identity")
	}
	resp, herr := s.post(ctx, r, s.cfg.UpstreamURL(store)+"/select/logsql/"+endpoint, params, "log store", tenant)
	if herr != nil {
		return herr
	}
	defer func() { _ = resp.Body.Close() }()

	if ct := resp.Header.Get("Content-Type"); ct != "" {
		w.Header().Set("Content-Type", ct)
	}
	// Cancelling the upstream request does not interrupt a write to a
	// client that stopped reading; only a write deadline does. A tail
	// renews it before every line, so only a stalled client is cut; the
	// server clears it once the response is finished.
	rc := http.NewResponseController(w)
	arm := func() {}
	if tail && s.cfg.TailIdleTimeout > 0 {
		arm = func() { _ = rc.SetWriteDeadline(time.Now().Add(s.cfg.TailIdleTimeout)) }
	} else if deadline, ok := ctx.Deadline(); ok {
		_ = rc.SetWriteDeadline(deadline)
	}
	arm()
	w.WriteHeader(resp.StatusCode)
	if rc.Flush() != nil {
		return nil
	}
	if tail {
		s.streamLines(w, rc, resp.Body, idle, arm)
	} else if _, err := io.Copy(w, resp.Body); err != nil {
		slog.Debug("copying upstream response", "error", err)
		return errAborted
	}
	return nil
}

// serveMetrics scopes to the namespace, or with kind set to the application
// route.Name of that kind within it.
func (s *Server) serveMetrics(w http.ResponseWriter, r *http.Request, route Route, user, kind string) *HTTPError {
	release, ok := s.limits.acquire(user)
	if !ok {
		return tooManyConcurrent(w)
	}
	defer release()
	if herr := s.readForm(w, r); herr != nil {
		return herr
	}
	now := s.now()
	req, herr := BuildMetricsParams(route.Endpoint, r.Form, route.Namespace, now, s.cfg.Metrics.Limits)
	if herr != nil {
		return herr
	}

	ctx := r.Context()
	if s.cfg.RequestTimeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, s.cfg.RequestTimeout)
		defer cancel()
	}
	store := s.cfg.Metrics.store(req.start, now)
	if kind != "" {
		selectors, herr := s.applicationScope(ctx, r, store, req, route.Namespace, kind, route.Name)
		if herr != nil {
			return herr
		}
		req.setScope(selectors)
	}
	resp, herr := s.post(ctx, r, s.cfg.Metrics.UpstreamURL(store)+"/"+req.path, req.params, "metrics store", nil)
	if herr != nil {
		return herr
	}
	defer func() { _ = resp.Body.Close() }()
	if ct := resp.Header.Get("Content-Type"); ct != "" {
		w.Header().Set("Content-Type", ct)
	}
	// As for a bounded logs answer: the client must take it by the request
	// deadline, and a break-off cuts the connection.
	if deadline, ok := ctx.Deadline(); ok {
		_ = http.NewResponseController(w).SetWriteDeadline(deadline)
	}
	w.WriteHeader(resp.StatusCode)
	if _, err := io.Copy(w, resp.Body); err != nil {
		slog.Debug("copying upstream response", "error", err)
		return errAborted
	}
	return nil
}

func (s *Server) applicationKind(plural string) (string, *HTTPError) {
	kind, err := s.kinds.Kind(plural)
	if err != nil {
		slog.Error("mapping application plural to kind", "plural", plural, "error", err)
		return "", errorf(http.StatusServiceUnavailable, "cannot resolve the application kind")
	}
	if kind == "" {
		return "", errorf(http.StatusNotFound, "unknown application resource %q", plural)
	}
	return kind, nil
}

// applicationStreamFilter selects the streams Fluent Bit labels with the
// pod's lineage. vlselect ANDs each extra_stream_filters value into the
// query and every subquery (Query.AddExtraFilters in lib/logstorage), so
// it narrows the namespace filter sent next to it.
func applicationStreamFilter(kind, name string) string {
	return "{apps_cozystack_io_application_kind=" + strconv.Quote(kind) + ",apps_cozystack_io_application_name=" + strconv.Quote(name) + "}"
}

// post sends params as a form with only the given headers, so nothing the
// client sent reaches the store, and tells an unreachable store (502) from
// one that missed its deadline or, for a tail, its idle timeout (504).
func (s *Server) post(ctx context.Context, r *http.Request, target string, params url.Values, what string, header http.Header) (*http.Response, *HTTPError) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, target, strings.NewReader(params.Encode()))
	if err != nil {
		return nil, errorf(http.StatusInternalServerError, "building upstream request: %v", err)
	}
	for k, v := range header {
		req.Header[k] = v
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := s.client.Do(req)
	switch {
	case err == nil:
		return resp, nil
	case r.Context().Err() != nil:
		return nil, errorf(http.StatusBadGateway, "client went away")
	case errors.Is(context.Cause(ctx), context.DeadlineExceeded):
		return nil, errorf(http.StatusGatewayTimeout, "%s did not answer in time", what)
	default:
		slog.Warn(what+" unreachable", "url", target, "error", err)
		return nil, errorf(http.StatusBadGateway, "%s unreachable", what)
	}
}

// concurrencyRetryAfter is a hint only: a slot frees when another request
// of the user ends, which the proxy cannot predict, and one second keeps
// a polling client from retrying in a tight loop.
const concurrencyRetryAfter = "1"

func tooManyConcurrent(w http.ResponseWriter) *HTTPError {
	w.Header().Set("Retry-After", concurrencyRetryAfter)
	return errorf(http.StatusTooManyRequests, "too many concurrent requests")
}

// readForm reads the whole body under the request timeout, so a client
// that sends it slowly holds its slot only that long. ParseForm leaves the
// body of a GET or of a POST that is not a form unread, and net/http
// drains a leftover body with no deadline once the headers go out, so the
// rest is read here as well; draining rather than refusing a declared
// body keeps any client that sends one working. Once the body is read the
// deadline is lifted: the server's background read would take a later
// expiry for a client that went away and cancel a tail. After a failed
// read it stays, so the server's drain of the unread body is bounded too.
func (s *Server) readForm(w http.ResponseWriter, r *http.Request) *HTTPError {
	r.Body = http.MaxBytesReader(w, r.Body, maxFormBytes)
	rc := http.NewResponseController(w)
	if s.cfg.RequestTimeout > 0 {
		_ = rc.SetReadDeadline(time.Now().Add(s.cfg.RequestTimeout))
	}
	err := r.ParseForm()
	if err == nil {
		_, err = io.Copy(io.Discard, r.Body)
	}
	var tooLarge *http.MaxBytesError
	switch {
	case errors.As(err, &tooLarge):
		return errorf(http.StatusRequestEntityTooLarge, "request body larger than %d bytes", tooLarge.Limit)
	case err == nil:
		_ = rc.SetReadDeadline(time.Time{})
		if rec, ok := w.(*statusRecorder); ok {
			rec.bodyRead = true
		}
		return nil
	case errors.Is(err, os.ErrDeadlineExceeded):
		return errorf(http.StatusRequestTimeout, "request body not received within %s", s.cfg.RequestTimeout)
	default:
		return errorf(http.StatusBadRequest, "invalid form: %v", err)
	}
}

func (s *Server) authorize(r *http.Request, route Route) (string, *HTTPError) {
	scheme, token, _ := strings.Cut(r.Header.Get("Authorization"), " ")
	if !strings.EqualFold(scheme, "Bearer") {
		return "", errorf(http.StatusUnauthorized, "missing bearer token")
	}
	// The kube client sets no timeout of its own, and net/http cancels
	// r.Context on a dropped client only once the body is read, so a
	// stalled review would otherwise outlive every other limit.
	ctx := r.Context()
	if s.cfg.RequestTimeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, s.cfg.RequestTimeout)
		defer cancel()
	}
	user, err := s.auth.Authenticate(ctx, strings.TrimSpace(token))
	if errors.Is(err, ErrUnauthenticated) {
		return "", errorf(http.StatusUnauthorized, "invalid bearer token")
	}
	if err != nil {
		slog.Error("token review failed", "error", err)
		return "", errorf(http.StatusServiceUnavailable, "authentication unavailable")
	}
	for _, attrs := range route.Reviews() {
		allowed, err := s.auth.Authorize(ctx, user, attrs)
		if err != nil {
			slog.Error("subject access review failed", "error", err)
			return "", errorf(http.StatusServiceUnavailable, "authorization unavailable")
		}
		if !allowed {
			return "", errorf(http.StatusForbidden, "forbidden")
		}
	}
	return user.Username, nil
}

// streamLines flushes after every line so a live tail reaches the browser
// as it happens; each line pushes back idle, which cancels the upstream
// stream once no line has arrived for the idle timeout.
func (s *Server) streamLines(w http.ResponseWriter, rc *http.ResponseController, body io.Reader, idle *time.Timer, arm func()) {
	rd := bufio.NewReaderSize(body, tailChunkBytes)
	for {
		chunk, err := rd.ReadSlice('\n')
		if len(chunk) > 0 {
			if idle != nil {
				idle.Reset(s.cfg.TailIdleTimeout)
			}
			arm()
			if _, werr := w.Write(chunk); werr != nil {
				return
			}
			if errors.Is(err, bufio.ErrBufferFull) {
				continue
			}
			if ferr := rc.Flush(); ferr != nil {
				return
			}
		}
		if err != nil && !errors.Is(err, bufio.ErrBufferFull) {
			return
		}
	}
}
