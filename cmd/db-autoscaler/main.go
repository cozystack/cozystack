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

package main

import (
	"crypto/tls"
	"flag"
	"os"

	// Import all Kubernetes client auth plugins (e.g. Azure, GCP, OIDC, etc.)
	// to ensure that exec-entrypoint and run can make use of them.
	_ "k8s.io/client-go/plugin/pkg/client/auth"

	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/client-go/dynamic"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/healthz"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
	"sigs.k8s.io/controller-runtime/pkg/metrics/filters"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"
	"sigs.k8s.io/controller-runtime/pkg/webhook"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"

	autoscalingv1alpha1 "github.com/cozystack/cozystack/api/autoscaling/v1alpha1"
	cozyv1alpha1 "github.com/cozystack/cozystack/api/v1alpha1"
	"github.com/cozystack/cozystack/internal/dbautoscaler"
	// +kubebuilder:scaffold:imports
)

// validateReplicasPath is the webhook path the ValidatingWebhookConfiguration
// must target. It guards the backing Flux HelmRelease (not the aggregated apps
// API, which kube-apiserver does not run admission webhooks for).
const validateReplicasPath = "/validate-helmrelease-replicas"

var (
	scheme   = runtime.NewScheme()
	setupLog = ctrl.Log.WithName("setup")
)

func init() {
	utilruntime.Must(clientgoscheme.AddToScheme(scheme))
	utilruntime.Must(cozyv1alpha1.AddToScheme(scheme))
	utilruntime.Must(autoscalingv1alpha1.AddToScheme(scheme))
	// +kubebuilder:scaffold:scheme
}

func main() {
	var metricsAddr string
	var enableLeaderElection bool
	var probeAddr string
	var secureMetrics bool
	var enableHTTP2 bool
	var operatorUsername string
	var appsAPIUsername string
	var enableWebhook bool
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
	flag.StringVar(&operatorUsername, "operator-username",
		"system:serviceaccount:cozy-db-autoscaler:db-autoscaler",
		"The ServiceAccount username the autoscaler runs as; the ownership webhook allows this user to change a managed replicas value.")
	flag.StringVar(&appsAPIUsername, "apps-api-username",
		"system:serviceaccount:cozy-system:cozystack-api",
		"The apps.cozystack.io extension-server ServiceAccount; legitimate replicas writes through the apps API reach the HelmRelease as this user, so it is also allowed.")
	flag.BoolVar(&enableWebhook, "enable-webhook", true,
		"Register the replicas-ownership validating webhook.")
	opts := zap.Options{Development: false}
	opts.BindFlags(flag.CommandLine)
	flag.Parse()

	ctrl.SetLogger(zap.New(zap.UseFlagOptions(&opts)))

	// Disable HTTP/2 by default (Rapid Reset / Stream Cancellation CVEs).
	disableHTTP2 := func(c *tls.Config) {
		setupLog.Info("disabling http/2")
		c.NextProtos = []string{"http/1.1"}
	}
	if !enableHTTP2 {
		tlsOpts = append(tlsOpts, disableHTTP2)
	}

	webhookServer := webhook.NewServer(webhook.Options{TLSOpts: tlsOpts})

	metricsServerOptions := metricsserver.Options{
		BindAddress:   metricsAddr,
		SecureServing: secureMetrics,
		TLSOpts:       tlsOpts,
	}
	if secureMetrics {
		metricsServerOptions.FilterProvider = filters.WithAuthenticationAndAuthorization
		// Serve metrics with the cert-manager serving cert (the same one mounted
		// for the webhook) so scrapers can verify the endpoint against its CA,
		// instead of controller-runtime's in-memory self-signed cert that forces
		// insecureSkipVerify on the VMServiceScrape.
		metricsServerOptions.CertDir = "/tmp/k8s-webhook-server/serving-certs"
		metricsServerOptions.CertName = "tls.crt"
		metricsServerOptions.KeyName = "tls.key"
	}

	config := ctrl.GetConfigOrDie()
	config.QPS = 50.0
	config.Burst = 100

	mgr, err := ctrl.NewManager(config, ctrl.Options{
		Scheme:                 scheme,
		Metrics:                metricsServerOptions,
		WebhookServer:          webhookServer,
		HealthProbeBindAddress: probeAddr,
		LeaderElection:         enableLeaderElection,
		LeaderElectionID:       "db-autoscaler.autoscaling.cozystack.io",
	})
	if err != nil {
		setupLog.Error(err, "unable to start manager")
		os.Exit(1)
	}

	dynClient, err := dynamic.NewForConfig(config)
	if err != nil {
		setupLog.Error(err, "unable to build dynamic client")
		os.Exit(1)
	}

	if err = (&dbautoscaler.Reconciler{
		Client:     mgr.GetClient(),
		Interface:  dynClient,
		RESTMapper: mgr.GetRESTMapper(),
		Scheme:     mgr.GetScheme(),
		Recorder:   mgr.GetEventRecorderFor("db-autoscaler"),
		VM:         dbautoscaler.NewVMClient(),
	}).SetupWithManager(mgr); err != nil {
		setupLog.Error(err, "unable to create controller", "controller", "DatabaseHorizontalAutoscaler")
		os.Exit(1)
	}

	if enableWebhook {
		mgr.GetWebhookServer().Register(validateReplicasPath, &admission.Webhook{
			Handler: &dbautoscaler.ReplicasOwnershipValidator{
				AllowedUsers:     []string{operatorUsername, appsAPIUsername},
				MarkerAnnotation: dbautoscaler.ProjectedMarkerAnnotation,
				ReplicasPath:     []string{"values", "replicas"},
			},
		})
		setupLog.Info("registered replicas-ownership webhook", "path", validateReplicasPath)
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

	setupLog.Info("starting manager")
	if err := mgr.Start(ctrl.SetupSignalHandler()); err != nil {
		setupLog.Error(err, "problem running manager")
		os.Exit(1)
	}
}
