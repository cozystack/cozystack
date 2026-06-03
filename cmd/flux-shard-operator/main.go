/*
Copyright 2026.

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

// flux-shard-operator spreads tenant HelmReleases across multiple
// helm-controller shards so one noisy tenant cannot degrade the others. It
// provisions the shard Deployments (cloned from flux-aio), owns the
// tenant->shard placement and stamps the shard label on new HelmReleases at
// admission time.
package main

import (
	"crypto/tls"
	"flag"
	"os"

	// Import all Kubernetes client auth plugins (e.g. Azure, GCP, OIDC, etc.)
	// to ensure that exec-entrypoint and run can make use of them.
	_ "k8s.io/client-go/plugin/pkg/client/auth"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/selection"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/healthz"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
	"sigs.k8s.io/controller-runtime/pkg/metrics/filters"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"
	"sigs.k8s.io/controller-runtime/pkg/webhook"

	fso "github.com/cozystack/cozystack/internal/fluxshardoperator"
)

var (
	scheme   = runtime.NewScheme()
	setupLog = ctrl.Log.WithName("setup")
)

func init() {
	utilruntime.Must(clientgoscheme.AddToScheme(scheme))
}

func main() {
	var metricsAddr string
	var enableLeaderElection bool
	var probeAddr string
	var secureMetrics bool
	var enableHTTP2 bool
	var fluxNamespace string
	var shardCount string
	var shardConcurrent int
	var rebalanceThreshold float64
	var pinnedTenants string
	var shardCPURequest, shardCPULimit, shardMemoryRequest, shardMemoryLimit string
	var tlsOpts []func(*tls.Config)

	flag.StringVar(&metricsAddr, "metrics-bind-address", "0", "The address the metrics endpoint binds to. "+
		"Use :8443 for HTTPS or :8080 for HTTP, or leave as 0 to disable the metrics service.")
	flag.StringVar(&probeAddr, "health-probe-bind-address", ":8081", "The address the probe endpoint binds to.")
	flag.BoolVar(&enableLeaderElection, "leader-elect", false,
		"Enable leader election for controller manager. "+
			"Enabling this will ensure there is only one active controller manager.")
	flag.BoolVar(&secureMetrics, "metrics-secure", true,
		"If set, the metrics endpoint is served securely via HTTPS. Use --metrics-secure=false to use HTTP instead.")
	flag.BoolVar(&enableHTTP2, "enable-http2", false,
		"If set, HTTP/2 will be enabled for the metrics and webhook servers")
	flag.StringVar(&fluxNamespace, "flux-namespace", "cozy-fluxcd",
		"Namespace of the flux-aio Deployment and the shard Deployments.")
	flag.StringVar(&shardCount, "shard-count", fso.ShardCountAuto,
		"Number of helm-controller shards to provision and distribute tenants over: "+
			"\"auto\" (default) sizes from the tenant HelmRelease count with hysteresis, "+
			"or a positive integer pins it explicitly.")
	flag.IntVar(&shardConcurrent, "shard-concurrent", 5,
		"Value of --concurrent for each shard helm-controller.")
	flag.Float64Var(&rebalanceThreshold, "rebalance-threshold", 0.25,
		"Load spread ratio (maxLoad-minLoad)/avgLoad above which tenants are rebalanced between shards.")
	flag.StringVar(&pinnedTenants, "pinned-tenants", "",
		"Comma-separated tenant pins, e.g. tenant-bigone=shard3,tenant-other=shard0.")
	flag.StringVar(&shardCPURequest, "shard-cpu-request", "",
		"CPU request for shard helm-controllers; empty inherits the flux-aio value.")
	flag.StringVar(&shardCPULimit, "shard-cpu-limit", "",
		"CPU limit for shard helm-controllers; empty inherits the flux-aio value.")
	flag.StringVar(&shardMemoryRequest, "shard-memory-request", "",
		"Memory request for shard helm-controllers; empty inherits the flux-aio value.")
	flag.StringVar(&shardMemoryLimit, "shard-memory-limit", "",
		"Memory limit for shard helm-controllers; empty inherits the flux-aio value.")
	opts := zap.Options{
		Development: false,
	}
	opts.BindFlags(flag.CommandLine)
	flag.Parse()

	ctrl.SetLogger(zap.New(zap.UseFlagOptions(&opts)))

	pinned, err := fso.ParsePinnedTenants(pinnedTenants)
	if err != nil {
		setupLog.Error(err, "invalid --pinned-tenants")
		os.Exit(1)
	}
	explicitShards, autoShards, err := fso.ParseShardCount(shardCount)
	if err != nil {
		setupLog.Error(err, "invalid --shard-count")
		os.Exit(1)
	}
	shardResources, err := parseShardResources(shardCPURequest, shardCPULimit, shardMemoryRequest, shardMemoryLimit)
	if err != nil {
		setupLog.Error(err, "invalid shard resources")
		os.Exit(1)
	}
	cfg := &fso.Config{
		FluxNamespace:      fluxNamespace,
		ShardCount:         explicitShards,
		AutoShardCount:     autoShards,
		ShardConcurrent:    shardConcurrent,
		RebalanceThreshold: rebalanceThreshold,
		PinnedTenants:      pinned,
		ShardResources:     shardResources,
	}

	// if the enable-http2 flag is false (the default), http/2 should be disabled
	// due to its vulnerabilities. More specifically, disabling http/2 will
	// prevent from being vulnerable to the HTTP/2 Stream Cancellation and
	// Rapid Reset CVEs. For more information see:
	// - https://github.com/advisories/GHSA-qppj-fm5r-hxr3
	// - https://github.com/advisories/GHSA-4374-p667-p6c8
	disableHTTP2 := func(c *tls.Config) {
		setupLog.Info("disabling http/2")
		c.NextProtos = []string{"http/1.1"}
	}

	if !enableHTTP2 {
		tlsOpts = append(tlsOpts, disableHTTP2)
	}

	webhookServer := webhook.NewServer(webhook.Options{
		TLSOpts: tlsOpts,
	})

	metricsServerOptions := metricsserver.Options{
		BindAddress:   metricsAddr,
		SecureServing: secureMetrics,
		TLSOpts:       tlsOpts,
	}

	if secureMetrics {
		// FilterProvider is used to protect the metrics endpoint with authn/authz.
		metricsServerOptions.FilterProvider = filters.WithAuthenticationAndAuthorization
	}

	shardKeyExists, err := labels.NewRequirement(fso.ShardKeyLabel, selection.Exists, nil)
	if err != nil {
		setupLog.Error(err, "building HelmRelease label selector")
		os.Exit(1)
	}

	restConfig := ctrl.GetConfigOrDie()
	restConfig.QPS = 50.0  // Increased from default 5.0
	restConfig.Burst = 100 // Increased from default 10

	mgr, err := ctrl.NewManager(restConfig, ctrl.Options{
		Scheme:                 scheme,
		Metrics:                metricsServerOptions,
		WebhookServer:          webhookServer,
		HealthProbeBindAddress: probeAddr,
		LeaderElection:         enableLeaderElection,
		LeaderElectionID:       "flux-shard-operator.cozystack.io",
		Cache: cache.Options{
			ByObject: map[client.Object]cache.ByObject{
				// Metadata-only stubs: the operator never decodes HelmRelease
				// specs/statuses, and the cache only holds tenant HRs (the
				// shard key label exists on every one of them).
				fso.HelmReleaseMeta(): {Label: labels.NewSelector().Add(*shardKeyExists)},
				fso.NamespaceMeta():   {},
				// Full Deployments, but only from the flux namespace.
				&appsv1.Deployment{}: {Namespaces: map[string]cache.Config{fluxNamespace: {}}},
			},
		},
	})
	if err != nil {
		setupLog.Error(err, "unable to start manager")
		os.Exit(1)
	}

	placement := &fso.PlacementReconciler{Client: mgr.GetClient(), Config: cfg}
	if err := placement.SetupWithManager(mgr); err != nil {
		setupLog.Error(err, "unable to setup controller", "controller", "Placement")
		os.Exit(1)
	}
	shardSet := &fso.ShardSetReconciler{Client: mgr.GetClient(), Config: cfg}
	if err := shardSet.SetupWithManager(mgr); err != nil {
		setupLog.Error(err, "unable to setup controller", "controller", "ShardSet")
		os.Exit(1)
	}
	shardWebhook := &fso.ShardWebhook{Reader: mgr.GetClient()}
	if err := shardWebhook.SetupWithManager(mgr); err != nil {
		setupLog.Error(err, "unable to setup webhook", "webhook", "ShardWebhook")
		os.Exit(1)
	}

	if err := mgr.AddHealthzCheck("healthz", healthz.Ping); err != nil {
		setupLog.Error(err, "unable to set up health check")
		os.Exit(1)
	}
	if err := mgr.AddReadyzCheck("readyz", healthz.Ping); err != nil {
		setupLog.Error(err, "unable to set up ready check")
		os.Exit(1)
	}

	setupLog.Info("starting manager",
		"fluxNamespace", fluxNamespace, "shardCount", shardCount, "shardConcurrent", shardConcurrent)
	if err := mgr.Start(ctrl.SetupSignalHandler()); err != nil {
		setupLog.Error(err, "problem running manager")
		os.Exit(1)
	}
}

// parseShardResources builds the shard helm-controller resource requirements
// from flags; empty flags inherit the corresponding flux-aio values.
func parseShardResources(cpuReq, cpuLim, memReq, memLim string) (corev1.ResourceRequirements, error) {
	res := corev1.ResourceRequirements{}
	set := func(list *corev1.ResourceList, name corev1.ResourceName, value string) error {
		if value == "" {
			return nil
		}
		q, err := resource.ParseQuantity(value)
		if err != nil {
			return err
		}
		if *list == nil {
			*list = corev1.ResourceList{}
		}
		(*list)[name] = q
		return nil
	}
	if err := set(&res.Requests, corev1.ResourceCPU, cpuReq); err != nil {
		return res, err
	}
	if err := set(&res.Limits, corev1.ResourceCPU, cpuLim); err != nil {
		return res, err
	}
	if err := set(&res.Requests, corev1.ResourceMemory, memReq); err != nil {
		return res, err
	}
	if err := set(&res.Limits, corev1.ResourceMemory, memLim); err != nil {
		return res, err
	}
	return res, nil
}
