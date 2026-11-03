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

// Command observability-proxy serves tenant logs (and, in later waves,
// metrics) from the VictoriaLogs and VictoriaMetrics stores behind a
// Kubernetes TokenReview and SubjectAccessReview, with the tenant's
// namespace scope forced into every upstream request.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	"github.com/prometheus/client_golang/prometheus/promhttp"
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
	fs.DurationVar(&o.requestTimeout, "request-timeout", time.Minute, "upstream deadline for every endpoint but tail")
	fs.DurationVar(&o.tailIdleTimeout, "tail-idle-timeout", 5*time.Minute, "close a tail after this long without a line")
	fs.DurationVar(&o.shutdownTimeout, "shutdown-timeout", 15*time.Second, "grace period for in-flight requests on shutdown")
	fs.DurationVar(&o.logs.DefaultRange, "logs-default-range", time.Hour, "window used when a logs request has no start")
	// One week covers an incident investigation and keeps a single query
	// to a fraction of the default one-month log retention.
	fs.DurationVar(&o.logs.MaxRange, "logs-max-range", 7*24*time.Hour, "longest [start, end] a logs request may span; wider windows are clamped")
	// The same cap on points as the metrics proxy.
	fs.IntVar(&o.logs.MaxBuckets, "logs-max-buckets", 11000, "most buckets hits and stats_query_range may return over a window")
	fs.IntVar(&o.logs.DefaultLimit, "logs-default-limit", 1000, "entries returned when a request sets no limit")
	fs.IntVar(&o.logs.MaxLimit, "logs-max-limit", 10000, "largest limit a request may set")
	fs.DurationVar(&o.logs.MaxTailResume, "logs-max-tail-resume", 15*time.Minute, "how far back a tail may resume with since or start_offset")
	fs.IntVar(&o.limiter.MaxConcurrentPerUser, "max-concurrent-per-user", 10, "concurrent non-tail requests per user; 0 disables")
	fs.Float64Var(&o.limiter.RatePerUser, "rate-limit-per-user", 10, "sustained requests per second per user; 0 disables")
	fs.IntVar(&o.limiter.RateBurst, "rate-limit-burst", 20, "request burst per user")
	fs.IntVar(&o.limiter.MaxTailsPerUser, "max-tails-per-user", 5, "open tails per user; 0 disables")
	fs.IntVar(&o.limiter.MaxTails, "max-tails", 200, "open tails in total; 0 disables")
	if err := fs.Parse(args); err != nil {
		return o, err
	}
	switch {
	case o.rootTenant == "":
		return o, errors.New("--root-tenant must not be empty")
	case o.clusterDomain == "":
		return o, errors.New("--cluster-domain must not be empty")
	case o.logs.DefaultRange <= 0 || o.logs.MaxRange < o.logs.DefaultRange:
		return o, errors.New("--logs-default-range must be positive and not above --logs-max-range")
	case o.logs.MaxBuckets <= 0:
		return o, errors.New("--logs-max-buckets must be positive")
	case o.logs.DefaultLimit <= 0 || o.logs.MaxLimit < o.logs.DefaultLimit:
		return o, errors.New("--logs-default-limit must be positive and not above --logs-max-limit")
	case o.logs.MaxTailResume < 0:
		return o, errors.New("--logs-max-tail-resume must not be negative")
	case o.requestTimeout < 0 || o.tailIdleTimeout < 0 || o.authCacheTTL < 0 || o.shutdownTimeout < 0:
		return o, errors.New("timeouts and the auth cache TTL must not be negative")
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
		Limiter:         o.limiter,
		RequestTimeout:  o.requestTimeout,
		TailIdleTimeout: o.tailIdleTimeout,
	}, observabilityproxy.NewAuthorizer(client, o.authCacheTTL), observabilityproxy.NewStoreResolver(lister, o.rootTenant), reg)

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
