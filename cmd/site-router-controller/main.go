// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Cozystack Authors.

// Command site-router-controller runs the SiteRouter reconciler: it mediates the
// kube-ovn routing/port-security and pushes the VyOS configuration for each
// site-router app instance. See internal/controller/siterouter.
package main

import (
	"crypto/tls"
	"flag"
	"os"

	// Import all Kubernetes client auth plugins (e.g. Azure, GCP, OIDC, etc.)
	// to ensure that exec-entrypoint and run can make use of them.
	_ "k8s.io/client-go/plugin/pkg/client/auth"

	helmv2 "github.com/fluxcd/helm-controller/api/v2"

	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/healthz"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
	"sigs.k8s.io/controller-runtime/pkg/metrics/filters"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"

	"github.com/cozystack/cozystack/internal/controller/siterouter"
)

var (
	scheme   = runtime.NewScheme()
	setupLog = ctrl.Log.WithName("setup")
)

func init() {
	utilruntime.Must(clientgoscheme.AddToScheme(scheme))
	// HelmRelease is the SiteRouter app-instance projection the controller
	// reconciles.
	utilruntime.Must(helmv2.AddToScheme(scheme))
}

func main() {
	var (
		metricsAddr          string
		probeAddr            string
		enableLeaderElection bool
		secureMetrics        bool
		enableHTTP2          bool
		managementCIDR       string
		allowOpenManagement  bool
	)

	flag.StringVar(&metricsAddr, "metrics-bind-address", "0", "The address the metrics endpoint binds to. "+
		"Use :8443 for HTTPS or :8080 for HTTP, or leave as 0 to disable the metrics service.")
	flag.StringVar(&probeAddr, "health-probe-bind-address", ":8081", "The address the probe endpoint binds to.")
	flag.BoolVar(&enableLeaderElection, "leader-elect", false,
		"Enable leader election for controller manager. "+
			"Enabling this will ensure there is only one active controller manager.")
	flag.BoolVar(&secureMetrics, "metrics-secure", true,
		"If set, the metrics endpoint is served securely via HTTPS. Use --metrics-secure=false to use HTTP instead.")
	flag.BoolVar(&enableHTTP2, "enable-http2", false,
		"If set, HTTP/2 will be enabled for the metrics server")
	flag.StringVar(&managementCIDR, "management-cidr", "",
		"IPv4 CIDR allowed to reach the VyOS management API (HTTPS 443) and SSH (22). REQUIRED in production. "+
			"MUST match the site-router chart's managementCIDR value (both default to the cluster pod CIDR "+
			"10.244.0.0/16): the chart seeds the first-boot firewall from it and the controller re-stamps the same "+
			"rule over the VyOS API. Pass --allow-open-management to explicitly opt out (test environments only).")
	flag.BoolVar(&allowOpenManagement, "allow-open-management", false,
		"Skip the fail-closed check on --management-cidr. ONLY for test environments where the cluster has no "+
			"real-world reachability to the gateway VMs; production deployments must set --management-cidr.")

	opts := zap.Options{Development: false}
	opts.BindFlags(flag.CommandLine)
	flag.Parse()

	ctrl.SetLogger(zap.New(zap.UseFlagOptions(&opts)))

	// Fail closed: an empty --management-cidr with no explicit opt-out is a
	// configuration error, not a warning. Exit before the manager starts so the
	// pod crashloops with a clear message rather than silently exposing the
	// gateway management API.
	if err := siterouter.ValidateManagementCIDR(managementCIDR, allowOpenManagement); err != nil {
		setupLog.Error(err, "invalid management-cidr configuration")
		os.Exit(1)
	}
	if managementCIDR == "" {
		setupLog.Info("WARNING: --allow-open-management is set; the VyOS management API will accept SSH/HTTPS " +
			"from any source. DO NOT use this configuration in production.")
	}

	// Disable HTTP/2 by default to avoid the Stream Cancellation and Rapid Reset
	// CVEs (GHSA-qppj-fm5r-hxr3, GHSA-4374-p667-p6c8).
	var tlsOpts []func(*tls.Config)
	if !enableHTTP2 {
		tlsOpts = append(tlsOpts, func(c *tls.Config) {
			setupLog.Info("disabling http/2")
			c.NextProtos = []string{"http/1.1"}
		})
	}

	metricsServerOptions := metricsserver.Options{
		BindAddress:   metricsAddr,
		SecureServing: secureMetrics,
		TLSOpts:       tlsOpts,
	}
	if secureMetrics {
		metricsServerOptions.FilterProvider = filters.WithAuthenticationAndAuthorization
	}

	// Configure rate limiting for the Kubernetes client.
	config := ctrl.GetConfigOrDie()
	config.QPS = 50.0
	config.Burst = 100

	mgr, err := ctrl.NewManager(config, ctrl.Options{
		Scheme:                 scheme,
		Metrics:                metricsServerOptions,
		HealthProbeBindAddress: probeAddr,
		LeaderElection:         enableLeaderElection,
		LeaderElectionID:       "site-router-controller.apps.cozystack.io",
		// Bound the informer caches to the SiteRouter instances and gateway pods
		// this controller acts on.
		Cache: cache.Options{ByObject: siterouter.CacheByObject()},
	})
	if err != nil {
		setupLog.Error(err, "unable to start manager")
		os.Exit(1)
	}

	if err := (&siterouter.SiteRouterReconciler{
		Client: mgr.GetClient(),
		// APIReader is the uncached reader for the cluster cozystack ConfigMap and
		// the tenant Namespace, which are intentionally not cached (CacheByObject
		// scopes the manager's informers to SiteRouter HelmReleases and gateway
		// pods only).
		APIReader:           mgr.GetAPIReader(),
		Scheme:              mgr.GetScheme(),
		Recorder:            mgr.GetEventRecorderFor("site-router-controller"),
		ManagementCIDR:      managementCIDR,
		AllowOpenManagement: allowOpenManagement,
	}).SetupWithManager(mgr); err != nil {
		setupLog.Error(err, "unable to setup controller", "controller", "SiteRouter")
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

	setupLog.Info("starting manager")
	if err := mgr.Start(ctrl.SetupSignalHandler()); err != nil {
		setupLog.Error(err, "problem running manager")
		os.Exit(1)
	}
}
