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
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/prometheus/client_golang/prometheus"
)

// tailChunkBytes bounds the memory one tail holds: a longer line is passed
// on in pieces and flushed once its end arrives.
const tailChunkBytes = 64 << 10

// maxFormBytes bounds a POST body; LogsQL queries are a few KiB at most.
const maxFormBytes = 1 << 20

// Config is the runtime configuration of the proxy handler.
type Config struct {
	ClusterDomain string
	// UpstreamURL maps a store namespace to the vlselect base URL; nil
	// means the in-cluster Service of the "generic" logs storage.
	UpstreamURL     func(store string) string
	Logs            LogsLimits
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
	client   *http.Client
	limits   *limiter
	now      func() time.Time
	requests *prometheus.CounterVec
}

func NewServer(cfg Config, auth *Authorizer, stores StoreLookup, reg prometheus.Registerer) *Server {
	if cfg.UpstreamURL == nil {
		domain := cfg.ClusterDomain
		cfg.UpstreamURL = func(store string) string {
			return fmt.Sprintf("http://vlselect-generic.%s.svc.%s:9471", store, domain)
		}
	}
	s := &Server{
		cfg:    cfg,
		auth:   auth,
		stores: stores,
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
	route, herr := ParseRoute(r.URL.EscapedPath())
	if herr == nil {
		herr = s.serve(rec, r, route)
	}
	if herr != nil {
		writeError(rec, herr)
	}
	s.requests.WithLabelValues(route.Kind.String(), strconv.Itoa(rec.code)).Inc()
}

func writeError(w http.ResponseWriter, e *HTTPError) {
	if e.Code == http.StatusUnauthorized {
		w.Header().Set("WWW-Authenticate", `Bearer realm="cozystack"`)
	}
	http.Error(w, e.Message, e.Code)
}

// serve answers an unimplemented root before authenticating, so later
// waves only add a case here.
func (s *Server) serve(w http.ResponseWriter, r *http.Request, route Route) *HTTPError {
	if route.Kind != NamespaceLogs {
		return errorf(http.StatusNotFound, "not found")
	}
	user, herr := s.authorize(r, route)
	if herr != nil {
		return herr
	}
	if r.Method != http.MethodGet && r.Method != http.MethodPost {
		w.Header().Set("Allow", "GET, POST")
		return errorf(http.StatusMethodNotAllowed, "method not allowed")
	}
	if !s.limits.allowRate(user) {
		return errorf(http.StatusUnprocessableEntity, "request rate limit exceeded")
	}
	endpoint, ok := strings.CutPrefix(route.Endpoint, "select/logsql/")
	if !ok {
		return errorf(http.StatusNotFound, "not found")
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxFormBytes)
	if err := r.ParseForm(); err != nil {
		return errorf(http.StatusBadRequest, "invalid form: %v", err)
	}
	params, herr := BuildLogsParams(endpoint, r.Form, route.Namespace, s.now(), s.cfg.Logs)
	if herr != nil {
		return herr
	}

	tail := endpoint == "tail"
	acquire := s.limits.acquire
	if tail {
		acquire = s.limits.acquireTail
	}
	release, ok := acquire(user)
	if !ok {
		return errorf(http.StatusUnprocessableEntity, "too many concurrent requests")
	}
	defer release()

	store, err := s.stores.Resolve(route.Namespace)
	if err != nil {
		slog.Error("resolving log store", "namespace", route.Namespace, "error", err)
		return errorf(http.StatusServiceUnavailable, "cannot resolve the log store")
	}

	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()
	if !tail && s.cfg.RequestTimeout > 0 {
		ctx, cancel = context.WithTimeout(ctx, s.cfg.RequestTimeout)
		defer cancel()
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		s.cfg.UpstreamURL(store)+"/select/logsql/"+endpoint, strings.NewReader(params.Encode()))
	if err != nil {
		return errorf(http.StatusInternalServerError, "building upstream request: %v", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("AccountID", "0")
	req.Header.Set("ProjectID", "0")
	resp, err := s.client.Do(req)
	switch {
	case err == nil:
	case r.Context().Err() != nil:
		return errorf(http.StatusBadGateway, "client went away")
	case errors.Is(err, context.DeadlineExceeded):
		return errorf(http.StatusGatewayTimeout, "log store did not answer within %s", s.cfg.RequestTimeout)
	default:
		slog.Warn("log store unreachable", "store", store, "error", err)
		return errorf(http.StatusBadGateway, "log store unreachable")
	}
	defer func() { _ = resp.Body.Close() }()

	if ct := resp.Header.Get("Content-Type"); ct != "" {
		w.Header().Set("Content-Type", ct)
	}
	w.WriteHeader(resp.StatusCode)
	rc := http.NewResponseController(w)
	_ = rc.Flush()
	if tail {
		s.streamLines(w, rc, resp.Body, cancel)
		return nil
	}
	if _, err := io.Copy(w, resp.Body); err != nil && !errors.Is(err, context.Canceled) {
		slog.Debug("copying upstream response", "error", err)
	}
	return nil
}

func (s *Server) authorize(r *http.Request, route Route) (string, *HTTPError) {
	token, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	if !ok {
		return "", errorf(http.StatusUnauthorized, "missing bearer token")
	}
	user, err := s.auth.Authenticate(r.Context(), strings.TrimSpace(token))
	if errors.Is(err, ErrUnauthenticated) {
		return "", errorf(http.StatusUnauthorized, "invalid bearer token")
	}
	if err != nil {
		slog.Error("token review failed", "error", err)
		return "", errorf(http.StatusServiceUnavailable, "authentication unavailable")
	}
	allowed, err := s.auth.Authorize(r.Context(), user, route.ResourceAttributes())
	if err != nil {
		slog.Error("subject access review failed", "error", err)
		return "", errorf(http.StatusServiceUnavailable, "authorization unavailable")
	}
	if !allowed {
		return "", errorf(http.StatusForbidden, "forbidden")
	}
	return user.Username, nil
}

// streamLines flushes after every line so a live tail reaches the browser
// as it happens, and cancels the upstream stream once no line has arrived
// for the idle timeout.
func (s *Server) streamLines(w http.ResponseWriter, rc *http.ResponseController, body io.Reader, cancel context.CancelFunc) {
	var idle *time.Timer
	if s.cfg.TailIdleTimeout > 0 {
		idle = time.AfterFunc(s.cfg.TailIdleTimeout, cancel)
		defer idle.Stop()
	}
	rd := bufio.NewReaderSize(body, tailChunkBytes)
	for {
		chunk, err := rd.ReadSlice('\n')
		if len(chunk) > 0 {
			if idle != nil {
				idle.Reset(s.cfg.TailIdleTimeout)
			}
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
