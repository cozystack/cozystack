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
	"flag"
	"fmt"
	"net/url"
	"os"
	"strings"
	"time"

	// Import all Kubernetes client auth plugins (e.g. Azure, GCP, OIDC, etc.)
	// to ensure that exec-entrypoint and run can make use of them.
	_ "k8s.io/client-go/plugin/pkg/client/auth"

	cozyv1alpha1 "github.com/cozystack/cozystack/api/v1alpha1"
	helmv2 "github.com/fluxcd/helm-controller/api/v2"
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
	// +kubebuilder:scaffold:imports
)

var (
	scheme   = runtime.NewScheme()
	setupLog = ctrl.Log.WithName("setup")
)

func init() {
	utilruntime.Must(clientgoscheme.AddToScheme(scheme))
	utilruntime.Must(apiextensionsv1.AddToScheme(scheme))
	utilruntime.Must(cozyv1alpha1.AddToScheme(scheme))
	utilruntime.Must(helmv2.AddToScheme(scheme))
	// +kubebuilder:scaffold:scheme
}

func main() {
	var metricsAddr string
	var enableLeaderElection bool
	var probeAddr string
	var secureMetrics bool
	var enableHTTP2 bool
	var installFlux bool
	var cozystackVersion string
	var platformSource string
	var platformSourceName string

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
	flag.StringVar(&cozystackVersion, "cozystack-version", "unknown",
		"Version of Cozystack")
	flag.StringVar(&platformSource, "platform-source", "", "Platform source URL (oci:// or git://). If specified, generates OCIRepository or GitRepository resource.")
	flag.StringVar(&platformSourceName, "platform-source-name", "cozystack-packages", "Name for the generated platform source resource (default: cozystack-packages)")

	opts := zap.Options{
		Development: true,
	}
	opts.BindFlags(flag.CommandLine)
	flag.Parse()

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
		LeaderElectionID:       "cozystack-operator.cozystack.io",
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
			setupLog.Error(err, "failed to install Flux")
			os.Exit(1)
		}
		setupLog.Info("Flux installation completed successfully")
	}

	// Generate and install platform source resource if specified
	if platformSource != "" {
		setupLog.Info("Generating platform source resource", "source", platformSource, "name", platformSourceName)
		installCtx, installCancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer installCancel()

		if err := installPlatformSourceResource(installCtx, mgr.GetClient(), platformSource, platformSourceName); err != nil {
			setupLog.Error(err, "failed to install platform source resource")
			os.Exit(1)
		} else {
			setupLog.Info("Platform source resource installation completed successfully")
		}
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

	setupLog.Info("Starting controller manager")
	mgrCtx := ctrl.SetupSignalHandler()
	if err := mgr.Start(mgrCtx); err != nil {
		setupLog.Error(err, "problem running manager")
		os.Exit(1)
	}
}

// installPlatformSourceResource generates and installs a Flux source resource (OCIRepository or GitRepository)
// based on the platform source URL
func installPlatformSourceResource(ctx context.Context, k8sClient client.Client, sourceURL, resourceName string) error {
	logger := log.FromContext(ctx)

	// Parse the source URL to determine type
	sourceType, repoURL, ref, err := parsePlatformSource(sourceURL)
	if err != nil {
		return fmt.Errorf("failed to parse platform source URL: %w", err)
	}

	var obj *unstructured.Unstructured
	switch sourceType {
	case "oci":
		obj, err = generateOCIRepository(resourceName, repoURL, ref)
		if err != nil {
			return fmt.Errorf("failed to generate OCIRepository: %w", err)
		}
	case "git":
		obj, err = generateGitRepository(resourceName, repoURL, ref)
		if err != nil {
			return fmt.Errorf("failed to generate GitRepository: %w", err)
		}
	default:
		return fmt.Errorf("unsupported source type: %s (expected oci:// or git://)", sourceType)
	}

	// Apply the resource (create or update)
	logger.Info("Applying platform source resource",
		"apiVersion", obj.GetAPIVersion(),
		"kind", obj.GetKind(),
		"name", obj.GetName(),
		"namespace", obj.GetNamespace(),
	)

	existing := &unstructured.Unstructured{}
	existing.SetGroupVersionKind(obj.GroupVersionKind())
	key := client.ObjectKey{
		Name:      obj.GetName(),
		Namespace: obj.GetNamespace(),
	}

	err = k8sClient.Get(ctx, key, existing)
	if err != nil {
		if client.IgnoreNotFound(err) == nil {
			// Resource doesn't exist, create it
			if err := k8sClient.Create(ctx, obj); err != nil {
				return fmt.Errorf("failed to create resource %s/%s: %w", obj.GetKind(), obj.GetName(), err)
			}
			logger.Info("Created platform source resource", "kind", obj.GetKind(), "name", obj.GetName())
		} else {
			return fmt.Errorf("failed to check if resource exists: %w", err)
		}
	} else {
		// Resource exists, update it
		obj.SetResourceVersion(existing.GetResourceVersion())
		if err := k8sClient.Update(ctx, obj); err != nil {
			return fmt.Errorf("failed to update resource %s/%s: %w", obj.GetKind(), obj.GetName(), err)
		}
		logger.Info("Updated platform source resource", "kind", obj.GetKind(), "name", obj.GetName())
	}

	return nil
}

// parsePlatformSource parses the source URL and returns the type, repository URL, and reference
// Supports formats:
//   - oci://registry.example.com/repo@sha256:digest
//   - oci://registry.example.com/repo (ref will be empty)
//   - git://github.com/user/repo@branch
//   - git://github.com/user/repo (ref will default to "main")
//   - https://github.com/user/repo@branch (treated as git)
func parsePlatformSource(sourceURL string) (sourceType, repoURL, ref string, err error) {
	// Normalize the URL by trimming whitespace
	sourceURL = strings.TrimSpace(sourceURL)

	// Check for oci:// prefix
	if strings.HasPrefix(sourceURL, "oci://") {
		// Remove oci:// prefix
		rest := strings.TrimPrefix(sourceURL, "oci://")

		// Check for @sha256: digest (look for @ followed by sha256:)
		// We need to find the last @ before sha256: to handle paths with @ symbols
		sha256Idx := strings.LastIndex(rest, "@sha256:")
		if sha256Idx != -1 {
			repoURL = "oci://" + rest[:sha256Idx]
			ref = rest[sha256Idx+1:] // sha256:digest
		} else {
			// Check for @ without sha256: (might be a tag)
			if atIdx := strings.LastIndex(rest, "@"); atIdx != -1 {
				// Could be a tag, but for OCI we expect sha256: digest
				// For now, treat everything after @ as the ref
				repoURL = "oci://" + rest[:atIdx]
				ref = rest[atIdx+1:]
			} else {
				repoURL = "oci://" + rest
				ref = "" // No digest specified
			}
		}
		return "oci", repoURL, ref, nil
	}

	// Check for git:// prefix or treat as git for http/https
	if strings.HasPrefix(sourceURL, "git://") || strings.HasPrefix(sourceURL, "http://") || strings.HasPrefix(sourceURL, "https://") || strings.HasPrefix(sourceURL, "ssh://") {
		// Parse URL to extract ref if present
		parsedURL, err := url.Parse(sourceURL)
		if err != nil {
			return "", "", "", fmt.Errorf("invalid URL: %w", err)
		}

		// Check for @ref in the path (e.g., git://host/path@branch)
		path := parsedURL.Path
		if idx := strings.LastIndex(path, "@"); idx != -1 {
			repoURL = fmt.Sprintf("%s://%s%s", parsedURL.Scheme, parsedURL.Host, path[:idx])
			if parsedURL.RawQuery != "" {
				repoURL += "?" + parsedURL.RawQuery
			}
			ref = path[idx+1:]
		} else {
			// Default to main branch if no ref specified
			repoURL = sourceURL
			ref = "main"
		}

		// Normalize git:// to https:// for GitRepository
		if strings.HasPrefix(repoURL, "git://") {
			repoURL = strings.Replace(repoURL, "git://", "https://", 1)
		}

		return "git", repoURL, ref, nil
	}

	return "", "", "", fmt.Errorf("unsupported source URL scheme (expected oci:// or git://): %s", sourceURL)
}

// generateOCIRepository creates an OCIRepository resource
func generateOCIRepository(name, repoURL, digest string) (*unstructured.Unstructured, error) {
	obj := &unstructured.Unstructured{}
	obj.SetAPIVersion("source.toolkit.fluxcd.io/v1")
	obj.SetKind("OCIRepository")
	obj.SetName(name)
	obj.SetNamespace("cozy-system")

	spec := map[string]interface{}{
		"interval": "5m0s",
		"url":      repoURL,
	}

	if digest != "" {
		// Ensure digest starts with sha256:
		if !strings.HasPrefix(digest, "sha256:") {
			digest = "sha256:" + digest
		}
		spec["ref"] = map[string]interface{}{
			"digest": digest,
		}
	}

	if err := unstructured.SetNestedField(obj.Object, spec, "spec"); err != nil {
		return nil, fmt.Errorf("failed to set spec: %w", err)
	}

	return obj, nil
}

// generateGitRepository creates a GitRepository resource
func generateGitRepository(name, repoURL, ref string) (*unstructured.Unstructured, error) {
	obj := &unstructured.Unstructured{}
	obj.SetAPIVersion("source.toolkit.fluxcd.io/v1")
	obj.SetKind("GitRepository")
	obj.SetName(name)
	obj.SetNamespace("cozy-system")

	spec := map[string]interface{}{
		"interval": "5m0s",
		"url":      repoURL,
		"ref": map[string]interface{}{
			"branch": ref,
		},
	}

	if err := unstructured.SetNestedField(obj.Object, spec, "spec"); err != nil {
		return nil, fmt.Errorf("failed to set spec: %w", err)
	}

	return obj, nil
}
