/*
Copyright 2025.

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

package main

import (
	"crypto/tls"
	"flag"
	"os"
	"time"

	// Import all Kubernetes client auth plugins (e.g. Azure, GCP, OIDC, etc.)
	// to ensure that exec-entrypoint and run can make use of them.
	_ "k8s.io/client-go/plugin/pkg/client/auth"

	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/healthz"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
	"sigs.k8s.io/controller-runtime/pkg/metrics/filters"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"
	"sigs.k8s.io/controller-runtime/pkg/webhook"

	cozystackiov1alpha1 "github.com/cozystack/cozystack/api/v1alpha1"
	"github.com/cozystack/cozystack/internal/controller"
	"github.com/cozystack/cozystack/internal/controller/dashboard"
	"github.com/cozystack/cozystack/internal/telemetry"

	helmv2 "github.com/fluxcd/helm-controller/api/v2"
	// +kubebuilder:scaffold:imports
)

var (
	scheme   = runtime.NewScheme()
	setupLog = ctrl.Log.WithName("setup")
)

func init() {
	utilruntime.Must(clientgoscheme.AddToScheme(scheme))

	utilruntime.Must(cozystackiov1alpha1.AddToScheme(scheme))
	utilruntime.Must(dashboard.AddToScheme(scheme))
	utilruntime.Must(helmv2.AddToScheme(scheme))
	// +kubebuilder:scaffold:scheme
}

func main() {
	var metricsAddr string
	var enableLeaderElection bool
	var probeAddr string
	var secureMetrics bool
	var enableHTTP2 bool
	var disableTelemetry bool
	var telemetryEndpoint string
	var telemetryInterval string
	var cozystackVersion string
	var reconcileDeployment bool
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
	flag.BoolVar(&disableTelemetry, "disable-telemetry", false,
		"Disable telemetry collection")
	flag.StringVar(&telemetryEndpoint, "telemetry-endpoint", "https://telemetry.cozystack.io",
		"Endpoint for sending telemetry data")
	flag.StringVar(&telemetryInterval, "telemetry-interval", "15m",
		"Interval between telemetry data collection (e.g. 15m, 1h)")
	flag.StringVar(&cozystackVersion, "cozystack-version", "unknown",
		"Version of Cozystack")
	flag.BoolVar(&reconcileDeployment, "reconcile-deployment", false,
		"If set, the Cozystack API server is assumed to run as a Deployment, else as a DaemonSet.")
	opts := zap.Options{
		Development: false,
	}
	opts.BindFlags(flag.CommandLine)
	flag.Parse()

	// Parse telemetry interval
	interval, err := time.ParseDuration(telemetryInterval)
	if err != nil {
		setupLog.Error(err, "invalid telemetry interval")
		os.Exit(1)
	}

	// Configure telemetry
	telemetryConfig := telemetry.Config{
		Disabled:         disableTelemetry,
		Endpoint:         telemetryEndpoint,
		Interval:         interval,
		CozystackVersion: cozystackVersion,
	}

	ctrl.SetLogger(zap.New(zap.UseFlagOptions(&opts)))

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

	// Metrics endpoint is enabled in 'config/default/kustomization.yaml'. The Metrics options configure the server.
	// More info:
	// - https://pkg.go.dev/sigs.k8s.io/controller-runtime@v0.19.1/pkg/metrics/server
	// - https://book.kubebuilder.io/reference/metrics.html
	metricsServerOptions := metricsserver.Options{
		BindAddress:   metricsAddr,
		SecureServing: secureMetrics,
		TLSOpts:       tlsOpts,
	}

	if secureMetrics {
		// FilterProvider is used to protect the metrics endpoint with authn/authz.
		// These configurations ensure that only authorized users and service accounts
		// can access the metrics endpoint. The RBAC are configured in 'config/rbac/kustomization.yaml'. More info:
		// https://pkg.go.dev/sigs.k8s.io/controller-runtime@v0.19.1/pkg/metrics/filters#WithAuthenticationAndAuthorization
		metricsServerOptions.FilterProvider = filters.WithAuthenticationAndAuthorization

		// TODO(user): If CertDir, CertName, and KeyName are not specified, controller-runtime will automatically
		// generate self-signed certificates for the metrics server. While convenient for development and testing,
		// this setup is not recommended for production.
	}

	// Configure rate limiting for the Kubernetes client
	config := ctrl.GetConfigOrDie()
	config.QPS = 50.0  // Increased from default 5.0
	config.Burst = 100 // Increased from default 10

	mgr, err := ctrl.NewManager(config, ctrl.Options{
		Scheme:                 scheme,
		Metrics:                metricsServerOptions,
		WebhookServer:          webhookServer,
		HealthProbeBindAddress: probeAddr,
		LeaderElection:         enableLeaderElection,
		LeaderElectionID:       "19a0338c.cozystack.io",
		// LeaderElectionReleaseOnCancel defines if the leader should step down voluntarily
		// when the Manager ends. This requires the binary to immediately end when the
		// Manager is stopped, otherwise, this setting is unsafe. Setting this significantly
		// speeds up voluntary leader transitions as the new leader don't have to wait
		// LeaseDuration time first.
		//
		// In the default scaffold provided, the program ends immediately after
		// the manager stops, so would be fine to enable this option. However,
		// if you are doing or is intended to do any operation such as perform cleanups
		// after the manager stops then its usage might be unsafe.
		// LeaderElectionReleaseOnCancel: true,
	})
	if err != nil {
		setupLog.Error(err, "unable to start manager")
		os.Exit(1)
	}

	if err = (&controller.WorkloadMonitorReconciler{
		Client: mgr.GetClient(),
		Scheme: mgr.GetScheme(),
	}).SetupWithManager(mgr); err != nil {
		setupLog.Error(err, "unable to create controller", "controller", "WorkloadMonitor")
		os.Exit(1)
	}

	if err = (&controller.WorkloadReconciler{
		Client: mgr.GetClient(),
		Scheme: mgr.GetScheme(),
	}).SetupWithManager(mgr); err != nil {
		setupLog.Error(err, "unable to create controller", "controller", "WorkloadReconciler")
		os.Exit(1)
	}

	cozyAPIKind := "DaemonSet"
	if reconcileDeployment {
		cozyAPIKind = "Deployment"
	}
	if err = (&controller.CozystackResourceDefinitionReconciler{
		Client:           mgr.GetClient(),
		Scheme:           mgr.GetScheme(),
		CozystackAPIKind: cozyAPIKind,
	}).SetupWithManager(mgr); err != nil {
		setupLog.Error(err, "unable to create controller", "controller", "CozystackResourceDefinitionReconciler")
		os.Exit(1)
	}

	if err = (&controller.CozystackResourceDefinitionHelmReconciler{
		Client: mgr.GetClient(),
		Scheme: mgr.GetScheme(),
	}).SetupWithManager(mgr); err != nil {
		setupLog.Error(err, "unable to create controller", "controller", "CozystackResourceDefinitionHelmReconciler")
		os.Exit(1)
	}

	dashboardManager := &dashboard.Manager{
		Client: mgr.GetClient(),
		Scheme: mgr.GetScheme(),
	}
	if err = dashboardManager.SetupWithManager(mgr); err != nil {
		setupLog.Error(err, "unable to create controller", "controller", "DashboardReconciler")
		os.Exit(1)
	}

	// +kubebuilder:scaffold:builder

	if err := mgr.AddHealthzCheck("healthz", healthz.Ping); err != nil {
		setupLog.Error(err, "unable to set up health check")
		os.Exit(1)
	}
	if err := mgr.AddReadyzCheck("readyz", healthz.Ping); err != nil {
		setupLog.Error(err, "unable to set up ready check")
		os.Exit(1)
	}

	// Initialize telemetry collector
	collector, err := telemetry.NewCollector(mgr.GetClient(), &telemetryConfig, mgr.GetConfig())
	if err != nil {
		setupLog.V(1).Error(err, "unable to create telemetry collector, telemetry will be disabled")
	}

	if collector != nil {
		if err := mgr.Add(collector); err != nil {
			setupLog.Error(err, "unable to set up telemetry collector")
			setupLog.V(1).Error(err, "unable to set up telemetry collector, continuing without telemetry")
		}
	}

	setupLog.Info("starting manager")
	ctx := ctrl.SetupSignalHandler()
	dashboardManager.InitializeStaticResources(ctx)
	if err := mgr.Start(ctx); err != nil {
		setupLog.Error(err, "problem running manager")
		os.Exit(1)
	}
}
