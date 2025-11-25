/*
Copyright 2025 The Cozystack Authors.

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
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	// Import all Kubernetes client auth plugins (e.g. Azure, GCP, OIDC, etc.)
	// to ensure that exec-entrypoint and run can make use of them.
	_ "k8s.io/client-go/plugin/pkg/client/auth"

	cozyv1alpha1 "github.com/cozystack/cozystack/api/v1alpha1"
	helmv2 "github.com/fluxcd/helm-controller/api/v2"
	sourcev1 "github.com/fluxcd/source-controller/api/v1"
	sourcewatcherv1beta1 "github.com/fluxcd/source-watcher/api/v2/v1beta1"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/healthz"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"
	"sigs.k8s.io/controller-runtime/pkg/webhook"

	"github.com/cozystack/cozystack/internal/fluxinstall"
	"github.com/cozystack/cozystack/internal/operator"
	"github.com/cozystack/cozystack/internal/telemetry"
	// +kubebuilder:scaffold:imports
)

var (
	scheme   = runtime.NewScheme()
	setupLog = ctrl.Log.WithName("setup")
)

// stringSliceFlag is a custom flag type that allows multiple values
type stringSliceFlag []string

func (f *stringSliceFlag) String() string {
	return strings.Join(*f, ",")
}

func (f *stringSliceFlag) Set(value string) error {
	*f = append(*f, value)
	return nil
}

func init() {
	utilruntime.Must(clientgoscheme.AddToScheme(scheme))
	utilruntime.Must(apiextensionsv1.AddToScheme(scheme))
	utilruntime.Must(cozyv1alpha1.AddToScheme(scheme))
	utilruntime.Must(helmv2.AddToScheme(scheme))
	utilruntime.Must(sourcev1.AddToScheme(scheme))
	utilruntime.Must(sourcewatcherv1beta1.AddToScheme(scheme))
	// +kubebuilder:scaffold:scheme
}

func main() {
	var metricsAddr string
	var enableLeaderElection bool
	var probeAddr string
	var secureMetrics bool
	var enableHTTP2 bool
	var installFlux bool
	var disableTelemetry bool
	var telemetryEndpoint string
	var telemetryInterval string
	var cozystackVersion string
	var installFluxResources stringSliceFlag

	flag.StringVar(&metricsAddr, "metrics-bind-address", ":8080", "The address the metric endpoint binds to.")
	flag.StringVar(&probeAddr, "health-probe-bind-address", ":8081", "The address the probe endpoint binds to.")
	flag.BoolVar(&enableLeaderElection, "leader-elect", false,
		"Enable leader election for controller manager. "+
			"Enabling this will ensure there is only one active controller manager.")
	flag.BoolVar(&secureMetrics, "metrics-secure", false,
		"If set the metrics endpoint is served securely")
	flag.BoolVar(&enableHTTP2, "enable-http2", false,
		"If set, HTTP/2 will be enabled for the metrics and webhook servers")
	flag.BoolVar(&installFlux, "install-flux", false, "Install Flux components before starting reconcile loop")
	flag.Var(&installFluxResources, "install-flux-resource", "Install Flux resource (JSON format). Can be specified multiple times. Applied after Flux installation.")
	flag.BoolVar(&disableTelemetry, "disable-telemetry", false,
		"Disable telemetry collection")
	flag.StringVar(&telemetryEndpoint, "telemetry-endpoint", "https://telemetry.cozystack.io",
		"Endpoint for sending telemetry data")
	flag.StringVar(&telemetryInterval, "telemetry-interval", "15m",
		"Interval between telemetry data collection (e.g. 15m, 1h)")
	flag.StringVar(&cozystackVersion, "cozystack-version", "unknown",
		"Version of Cozystack")

	opts := zap.Options{
		Development: true,
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

	config := ctrl.GetConfigOrDie()

	// Start the controller manager
	setupLog.Info("Starting controller manager")
	mgr, err := ctrl.NewManager(config, ctrl.Options{
		Scheme: scheme,
		Metrics: metricsserver.Options{
			BindAddress:   metricsAddr,
			SecureServing: secureMetrics,
		},
		WebhookServer: webhook.NewServer(webhook.Options{
			Port: 9443,
		}),
		HealthProbeBindAddress: probeAddr,
		LeaderElection:         enableLeaderElection,
		LeaderElectionID:       "platform-operator.cozystack.io",
		// LeaderElectionReleaseOnCancel defines if the leader should step down voluntarily
		// when the Manager ends. This requires the binary to immediately end when the
		// Manager is stopped, otherwise, setting this significantly speeds up voluntary
		// leader transitions as the new leader don't have to wait LeaseDuration time first.
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

	// Install Flux before starting reconcile loop
	if installFlux {
		setupLog.Info("Installing Flux components before starting reconcile loop")
		installCtx, installCancel := context.WithTimeout(context.Background(), 5*time.Minute)
		defer installCancel()

		// The namespace will be automatically extracted from the embedded manifests
		if err := fluxinstall.Install(installCtx, mgr.GetClient(), fluxinstall.WriteEmbeddedManifests); err != nil {
			setupLog.Error(err, "failed to install Flux, continuing anyway")
			// Don't exit - allow operator to start even if Flux install fails
			// This allows the operator to work in environments where Flux is already installed
		} else {
			setupLog.Info("Flux installation completed successfully")
		}
	}

	// Install Flux resources after Flux installation
	if len(installFluxResources) > 0 {
		setupLog.Info("Installing Flux resources", "count", len(installFluxResources))
		installCtx, installCancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer installCancel()

		if err := installFluxResourcesFunc(installCtx, mgr.GetClient(), installFluxResources); err != nil {
			setupLog.Error(err, "failed to install Flux resources, continuing anyway")
			// Don't exit - allow operator to start even if resource installation fails
		} else {
			setupLog.Info("Flux resources installation completed successfully")
		}
	}

	bundleReconciler := &operator.CozystackBundleReconciler{
		Client: mgr.GetClient(),
		Scheme: mgr.GetScheme(),
	}
	if err = bundleReconciler.SetupWithManager(mgr); err != nil {
		setupLog.Error(err, "unable to create controller", "controller", "CozystackBundle")
		os.Exit(1)
	}

	platformReconciler := &operator.CozystackPlatformReconciler{
		Client: mgr.GetClient(),
		Scheme: mgr.GetScheme(),
	}
	if err = platformReconciler.SetupWithManager(mgr); err != nil {
		setupLog.Error(err, "unable to create controller", "controller", "CozystackPlatform")
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

	setupLog.Info("Starting controller manager")
	mgrCtx := ctrl.SetupSignalHandler()
	if err := mgr.Start(mgrCtx); err != nil {
		setupLog.Error(err, "problem running manager")
		os.Exit(1)
	}
}

// installFluxResourcesFunc installs Flux resources from JSON strings
func installFluxResourcesFunc(ctx context.Context, k8sClient client.Client, resources []string) error {
	logger := log.FromContext(ctx)

	for i, resourceJSON := range resources {
		logger.Info("Installing Flux resource", "index", i+1, "total", len(resources))

		// Parse JSON into unstructured object
		var obj unstructured.Unstructured
		if err := json.Unmarshal([]byte(resourceJSON), &obj.Object); err != nil {
			return fmt.Errorf("failed to parse resource JSON at index %d: %w", i, err)
		}

		// Validate that it has required fields
		if obj.GetAPIVersion() == "" {
			return fmt.Errorf("resource at index %d missing apiVersion", i)
		}
		if obj.GetKind() == "" {
			return fmt.Errorf("resource at index %d missing kind", i)
		}
		if obj.GetName() == "" {
			return fmt.Errorf("resource at index %d missing metadata.name", i)
		}

		// Apply the resource (create or update)
		logger.Info("Applying Flux resource",
			"apiVersion", obj.GetAPIVersion(),
			"kind", obj.GetKind(),
			"name", obj.GetName(),
			"namespace", obj.GetNamespace(),
		)

		// Use server-side apply or create/update
		existing := &unstructured.Unstructured{}
		existing.SetGroupVersionKind(obj.GroupVersionKind())
		key := client.ObjectKey{
			Name:      obj.GetName(),
			Namespace: obj.GetNamespace(),
		}

		err := k8sClient.Get(ctx, key, existing)
		if err != nil {
			if client.IgnoreNotFound(err) == nil {
				// Resource doesn't exist, create it
				if err := k8sClient.Create(ctx, &obj); err != nil {
					return fmt.Errorf("failed to create resource %s/%s: %w", obj.GetKind(), obj.GetName(), err)
				}
				logger.Info("Created Flux resource", "kind", obj.GetKind(), "name", obj.GetName())
			} else {
				return fmt.Errorf("failed to check if resource exists: %w", err)
			}
		} else {
			// Resource exists, update it
			obj.SetResourceVersion(existing.GetResourceVersion())
			if err := k8sClient.Update(ctx, &obj); err != nil {
				return fmt.Errorf("failed to update resource %s/%s: %w", obj.GetKind(), obj.GetName(), err)
			}
			logger.Info("Updated Flux resource", "kind", obj.GetKind(), "name", obj.GetName())
		}
	}

	return nil
}
