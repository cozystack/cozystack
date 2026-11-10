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

// Command observability-proxy serves tenant logs and metrics from the
// VictoriaLogs and VictoriaMetrics stores behind a Kubernetes TokenReview
// and SubjectAccessReview, with the tenant's namespace scope forced into
// every upstream request.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"math"
	"net/http"
	"os"
	"os/signal"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"k8s.io/apimachinery/pkg/util/validation"
	"k8s.io/client-go/informers"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/tools/clientcmd"

	"github.com/cozystack/cozystack/internal/observabilityproxy"
)

type options struct {
	listenAddress   string
	probeAddress    string
	clusterDomain   string
	rootTenant      string
	kubeconfig      string
	authCacheTTL    time.Duration
	requestTimeout  time.Duration
	tailIdleTimeout time.Duration
	shutdownTimeout time.Duration
	logs            observabilityproxy.LogsLimits
	metrics         observabilityproxy.MetricsConfig
	limiter         observabilityproxy.LimiterConfig
}

func parseFlags(args []string) (options, error) {
	var o options
	fs := flag.NewFlagSet("observability-proxy", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	fs.StringVar(&o.listenAddress, "listen-address", ":8080", "address of the API listener")
	fs.StringVar(&o.probeAddress, "probe-address", ":8081", "address serving /healthz, /readyz and /metrics")
	fs.StringVar(&o.clusterDomain, "cluster-domain", "cozy.local", "cluster DNS domain used to reach the stores")
	fs.StringVar(&o.rootTenant, "root-tenant", "tenant-root", "tenant namespace hosting the default store")
	fs.StringVar(&o.kubeconfig, "kubeconfig", "", "kubeconfig path; in-cluster config when empty")
	fs.DurationVar(&o.authCacheTTL, "auth-cache-ttl", 10*time.Second, "how long TokenReview and SubjectAccessReview answers are cached")
	fs.DurationVar(&o.requestTimeout, "request-timeout", time.Minute, "deadline for reading the request body, the token and access reviews, and every upstream endpoint but tail; 0 disables it")
	fs.DurationVar(&o.tailIdleTimeout, "tail-idle-timeout", 5*time.Minute, "close a tail after this long without a line")
	fs.DurationVar(&o.shutdownTimeout, "shutdown-timeout", 15*time.Second, "grace period for in-flight requests on shutdown")
	fs.DurationVar(&o.logs.DefaultRange, "logs-default-range", time.Hour, "window used when a logs request has no start")
	// One week covers an incident investigation and keeps a single query
	// to a fraction of the default one-month log retention.
	fs.DurationVar(&o.logs.MaxRange, "logs-max-range", 7*24*time.Hour, "longest [start, end] a logs request may span; wider windows are clamped")
	// Prometheus's cap on points per series in a range query.
	fs.IntVar(&o.logs.MaxBuckets, "logs-max-buckets", 11000, "most buckets hits and stats_query_range may return over a window")
	fs.IntVar(&o.logs.DefaultLimit, "logs-default-limit", 1000, "entries returned when a request sets no limit")
	fs.IntVar(&o.logs.MaxLimit, "logs-max-limit", 10000, "largest limit a request may set")
	fs.DurationVar(&o.logs.MaxTailResume, "logs-max-tail-resume", 15*time.Minute, "how far back a tail may resume with since or start_offset")
	// Store names and retention follow metricsStorages in
	// packages/system/monitoring/values.yaml.
	fs.StringVar(&o.metrics.ShortTermStore, "metrics-shortterm-store", "shortterm", "VMCluster name of the short-term metrics store of the root tenant")
	fs.StringVar(&o.metrics.LongTermStore, "metrics-longterm-store", "longterm", "VMCluster name of the long-term metrics store of the root tenant")
	fs.DurationVar(&o.metrics.ShortTermRetention, "metrics-shortterm-retention", 72*time.Hour, "retention of the short-term store; older windows are read from the long-term one")
	fs.DurationVar(&o.metrics.Limits.MaxRange, "metrics-max-range", 30*24*time.Hour, "longest [start, end] and step a metrics request may use")
	fs.IntVar(&o.metrics.Limits.MaxPoints, "metrics-max-points", 11000, "most points per series a range query may return")
	fs.DurationVar(&o.metrics.Limits.MaxTimeout, "metrics-max-timeout", 30*time.Second, "timeout forwarded to vmselect; a longer client timeout is clamped; must be below --request-timeout when that is set")
	fs.StringVar(&o.metrics.PlatformNamespacePrefix, "metrics-platform-namespace-prefix", "cozy-", "namespaces with this prefix run platform exporters, whose exported_namespace label adds series to that namespace's scope; empty disables")
	fs.IntVar(&o.limiter.MaxConcurrentPerUser, "max-concurrent-per-user", 10, "concurrent non-tail requests per user; 0 disables")
	fs.Float64Var(&o.limiter.RatePerUser, "rate-limit-per-user", 10, "sustained requests per second per user; 0 disables")
	fs.IntVar(&o.limiter.RateBurst, "rate-limit-burst", 20, "request burst per user")
	fs.IntVar(&o.limiter.MaxTailsPerUser, "max-tails-per-user", 5, "open tails per user; 0 disables")
	fs.IntVar(&o.limiter.MaxTails, "max-tails", 200, "open tails in total; 0 disables")
	if err := fs.Parse(args); err != nil {
		return o, err
	}
	// Parsing stops at the first non-flag, so every flag after it would
	// be dropped silently.
	if fs.NArg() > 0 {
		return o, fmt.Errorf("unexpected argument %q", fs.Arg(0))
	}
	o.metrics.RootTenant = o.rootTenant
	for _, store := range []string{o.metrics.ShortTermStore, o.metrics.LongTermStore} {
		// The name becomes part of a Service host name.
		if errs := validation.IsDNS1123Label("vmselect-" + store); store == "" || len(errs) > 0 {
			return o, fmt.Errorf("invalid metrics store name %q", store)
		}
	}
	// Anything but namespace name characters would make the prefix match
	// differently from the namespaces it is meant to name.
	if p := o.metrics.PlatformNamespacePrefix; p != "" && len(validation.IsDNS1123Label(p+"x")) > 0 {
		return o, fmt.Errorf("invalid metrics platform namespace prefix %q", p)
	}
	switch {
	case o.rootTenant == "":
		return o, errors.New("--root-tenant must not be empty")
	case o.clusterDomain == "":
		return o, errors.New("--cluster-domain must not be empty")
	case o.logs.DefaultRange <= 0 || o.logs.MaxRange < o.logs.DefaultRange:
		return o, errors.New("--logs-default-range must be positive and not above --logs-max-range")
	// Below four buckets (three steps once one is held back for
	// alignment), or a range under three seconds, the step cap that keeps
	// vlselect's step alignment inside the range cannot hold.
	case o.logs.MaxBuckets < 4:
		return o, errors.New("--logs-max-buckets must be at least 4")
	case o.logs.MaxRange < 3*time.Second:
		return o, errors.New("--logs-max-range must be at least 3s")
	case o.logs.DefaultLimit <= 0 || o.logs.MaxLimit < o.logs.DefaultLimit:
		return o, errors.New("--logs-default-limit must be positive and not above --logs-max-limit")
	case o.metrics.ShortTermRetention <= 0 || o.metrics.Limits.MaxRange <= 0 || o.metrics.Limits.MaxTimeout <= 0:
		return o, errors.New("--metrics-shortterm-retention, --metrics-max-range and --metrics-max-timeout must be positive")
	case o.metrics.Limits.MaxPoints < 2:
		return o, errors.New("--metrics-max-points must be at least 2")
	case o.logs.MaxTailResume < 0:
		return o, errors.New("--logs-max-tail-resume must not be negative")
	// The proxy's deadline starts before the request reaches vmselect, so
	// at an equal or longer timeout it cuts a query vmselect is still
	// allowed to run and answers 504 for a store that is within its time.
	case o.requestTimeout > 0 && o.metrics.Limits.MaxTimeout >= o.requestTimeout:
		return o, errors.New("--metrics-max-timeout must be below --request-timeout")
	case o.requestTimeout < 0 || o.tailIdleTimeout < 0 || o.authCacheTTL < 0 || o.shutdownTimeout < 0:
		return o, errors.New("timeouts and the auth cache TTL must not be negative")
	// NaN passes every comparison and +Inf never empties a bucket; either
	// would switch the rate limit off without saying so.
	case math.IsNaN(o.limiter.RatePerUser) || math.IsInf(o.limiter.RatePerUser, 0):
		return o, errors.New("--rate-limit-per-user must be a finite number")
	case o.limiter.RatePerUser < 0 || o.limiter.RateBurst < 0 || o.limiter.MaxConcurrentPerUser < 0 ||
		o.limiter.MaxTailsPerUser < 0 || o.limiter.MaxTails < 0:
		return o, errors.New("limits must not be negative; 0 disables a limit")
	}
	return o, nil
}

func main() {
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stderr, nil)))
	o, err := parseFlags(os.Args[1:])
	if err != nil {
		fmt.Fprintf(os.Stderr, "observability-proxy: %v\n", err)
		os.Exit(2)
	}
	if err := run(o); err != nil {
		slog.Error("exiting", "error", err)
		os.Exit(1)
	}
}

func run(o options) error {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	restConfig, err := clientcmd.BuildConfigFromFlags("", o.kubeconfig)
	if err != nil {
		return fmt.Errorf("loading kubeconfig: %w", err)
	}
	client, err := kubernetes.NewForConfig(restConfig)
	if err != nil {
		return fmt.Errorf("building client: %w", err)
	}

	factory := informers.NewSharedInformerFactory(client, 0)
	namespaces := factory.Core().V1().Namespaces()
	lister := namespaces.Lister()
	factory.Start(ctx.Done())
	var ready atomic.Bool
	go func() {
		if cache.WaitForCacheSync(ctx.Done(), namespaces.Informer().HasSynced) {
			ready.Store(true)
		}
	}()

	reg := prometheus.NewRegistry()
	reg.MustRegister(collectors.NewGoCollector(), collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}))
	proxy := observabilityproxy.NewServer(observabilityproxy.Config{
		ClusterDomain:   o.clusterDomain,
		Logs:            o.logs,
		Metrics:         o.metrics,
		Limiter:         o.limiter,
		RequestTimeout:  o.requestTimeout,
		TailIdleTimeout: o.tailIdleTimeout,
	}, observabilityproxy.NewAuthorizer(client, o.authCacheTTL), observabilityproxy.NewStoreResolver(lister, namespaces.Informer().HasSynced, o.rootTenant), reg)

	probes := http.NewServeMux()
	probes.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, "ok") })
	probes.HandleFunc("/readyz", func(w http.ResponseWriter, _ *http.Request) {
		if !ready.Load() {
			http.Error(w, "namespace cache not synced", http.StatusServiceUnavailable)
			return
		}
		_, _ = io.WriteString(w, "ok")
	})
	probes.Handle("/metrics", promhttp.HandlerFor(reg, promhttp.HandlerOpts{}))

	// No WriteTimeout on the API listener: it would cut live tails, which
	// the handler bounds with its own idle timeout instead.
	servers := []*http.Server{
		{Addr: o.listenAddress, Handler: proxy, ReadHeaderTimeout: 10 * time.Second, IdleTimeout: 2 * time.Minute},
		{Addr: o.probeAddress, Handler: probes, ReadHeaderTimeout: 10 * time.Second},
	}
	errs := make(chan error, len(servers))
	for _, srv := range servers {
		go func() {
			slog.Info("listening", "address", srv.Addr)
			if err := srv.ListenAndServe(); !errors.Is(err, http.ErrServerClosed) {
				errs <- err
			}
		}()
	}

	var runErr error
	select {
	case <-ctx.Done():
	case runErr = <-errs:
	}
	stop()
	shutdownCtx, cancel := context.WithTimeout(context.Background(), o.shutdownTimeout)
	defer cancel()
	for _, srv := range servers {
		if err := srv.Shutdown(shutdownCtx); err != nil {
			// Open tails never go idle on their own; cut them so clients
			// reconnect with since to another replica.
			_ = srv.Close()
		}
	}
	return runErr
}
