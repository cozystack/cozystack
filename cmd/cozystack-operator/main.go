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
	corev1 "k8s.io/api/core/v1"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/fields"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/healthz"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"
	"sigs.k8s.io/controller-runtime/pkg/webhook"

	"github.com/cozystack/cozystack/internal/cozyvaluesreplicator"
	"github.com/cozystack/cozystack/internal/fluxinstall"
	"github.com/cozystack/cozystack/internal/operator"
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
	var cozystackVersion string
	var cozyValuesSecretName string
	var cozyValuesSecretNamespace string
	var cozyValuesNamespaceSelector string
	var platformSourceURL string
	var platformSourceName string
	var platformSourceRef string

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
	flag.StringVar(&platformSourceURL, "platform-source-url", "", "Platform source URL (oci:// or https://). If specified, generates OCIRepository or GitRepository resource.")
	flag.StringVar(&platformSourceName, "platform-source-name", "cozystack-packages", "Name for the generated platform source resource (default: cozystack-packages)")
	flag.StringVar(&platformSourceRef, "platform-source-ref", "", "Reference specification as key=value pairs (e.g., 'branch=main' or 'digest=sha256:...,tag=v1.0'). For OCI: digest, semver, semverFilter, tag. For Git: branch, tag, semver, name, commit.")
	flag.StringVar(&cozyValuesSecretName, "cozy-values-secret-name", "cozystack-values", "The name of the secret containing cluster-wide configuration values.")
	flag.StringVar(&cozyValuesSecretNamespace, "cozy-values-secret-namespace", "cozy-system", "The namespace of the secret containing cluster-wide configuration values.")
	flag.StringVar(&cozyValuesNamespaceSelector, "cozy-values-namespace-selector", "cozystack.io/system=true", "The label selector for namespaces where the cluster-wide configuration values must be replicated.")

	opts := zap.Options{
		Development: true,
	}
	opts.BindFlags(flag.CommandLine)
	flag.Parse()

	ctrl.SetLogger(zap.New(zap.UseFlagOptions(&opts)))

	config := ctrl.GetConfigOrDie()

	// Create a direct client (without cache) for pre-start operations
	directClient, err := client.New(config, client.Options{Scheme: scheme})
	if err != nil {
		setupLog.Error(err, "unable to create direct client")
		os.Exit(1)
	}

	targetNSSelector, err := labels.Parse(cozyValuesNamespaceSelector)
	if err != nil {
		setupLog.Error(err, "could not parse namespace label selector")
		os.Exit(1)
	}

	// Start the controller manager
	setupLog.Info("Starting controller manager")
	mgr, err := ctrl.NewManager(config, ctrl.Options{
		Scheme: scheme,
		Cache: cache.Options{
			ByObject: map[client.Object]cache.ByObject{
				// Cache only Secrets named <secretName> (in any namespace)
				&corev1.Secret{}: {
					Field: fields.OneTermEqualSelector("metadata.name", cozyValuesSecretName),
				},

				// Cache only Namespaces that match a label selector
				&corev1.Namespace{}: {
					Label: targetNSSelector,
				},
			},
		},
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

		// Use direct client for pre-start operations (cache is not ready yet)
		if err := fluxinstall.Install(installCtx, directClient, fluxinstall.WriteEmbeddedManifests); err != nil {
			setupLog.Error(err, "failed to install Flux")
			os.Exit(1)
		}
		setupLog.Info("Flux installation completed successfully")
	}

	// Generate and install platform source resource if specified
	if platformSourceURL != "" {
		setupLog.Info("Generating platform source resource", "url", platformSourceURL, "name", platformSourceName, "ref", platformSourceRef)
		installCtx, installCancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer installCancel()

		// Use direct client for pre-start operations (cache is not ready yet)
		if err := installPlatformSourceResource(installCtx, directClient, platformSourceURL, platformSourceName, platformSourceRef); err != nil {
			setupLog.Error(err, "failed to install platform source resource")
			os.Exit(1)
		} else {
			setupLog.Info("Platform source resource installation completed successfully")
		}
	}

	// Setup PackageSource reconciler
	if err := (&operator.PackageSourceReconciler{
		Client: mgr.GetClient(),
		Scheme: mgr.GetScheme(),
	}).SetupWithManager(mgr); err != nil {
		setupLog.Error(err, "unable to create controller", "controller", "PackageSource")
		os.Exit(1)
	}

	// Setup Package reconciler
	if err := (&operator.PackageReconciler{
		Client: mgr.GetClient(),
		Scheme: mgr.GetScheme(),
	}).SetupWithManager(mgr); err != nil {
		setupLog.Error(err, "unable to create controller", "controller", "Package")
		os.Exit(1)
	}

	// Setup CozyValuesReplicator reconciler
	if err := (&cozyvaluesreplicator.SecretReplicatorReconciler{
		Client:                  mgr.GetClient(),
		Scheme:                  mgr.GetScheme(),
		SourceNamespace:         cozyValuesSecretNamespace,
		SecretName:              cozyValuesSecretName,
		TargetNamespaceSelector: targetNSSelector,
	}).SetupWithManager(mgr); err != nil {
		setupLog.Error(err, "unable to create controller", "controller", "CozyValuesReplicator")
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

	setupLog.Info("Starting controller manager")
	mgrCtx := ctrl.SetupSignalHandler()
	if err := mgr.Start(mgrCtx); err != nil {
		setupLog.Error(err, "problem running manager")
		os.Exit(1)
	}
}

// installPlatformSourceResource generates and installs a Flux source resource (OCIRepository or GitRepository)
// based on the platform source URL
func installPlatformSourceResource(ctx context.Context, k8sClient client.Client, sourceURL, resourceName, refSpec string) error {
	logger := log.FromContext(ctx)

	// Parse the source URL to determine type
	sourceType, repoURL, err := parsePlatformSourceURL(sourceURL)
	if err != nil {
		return fmt.Errorf("failed to parse platform source URL: %w", err)
	}

	// Parse reference specification
	refMap, err := parseRefSpec(refSpec)
	if err != nil {
		return fmt.Errorf("failed to parse reference specification: %w", err)
	}

	var obj client.Object
	switch sourceType {
	case "oci":
		obj, err = generateOCIRepository(resourceName, repoURL, refMap)
		if err != nil {
			return fmt.Errorf("failed to generate OCIRepository: %w", err)
		}
	case "git":
		obj, err = generateGitRepository(resourceName, repoURL, refMap)
		if err != nil {
			return fmt.Errorf("failed to generate GitRepository: %w", err)
		}
	default:
		return fmt.Errorf("unsupported source type: %s (expected oci:// or https://)", sourceType)
	}

	// Apply the resource (create or update)
	logger.Info("Applying platform source resource",
		"apiVersion", obj.GetObjectKind().GroupVersionKind().GroupVersion().String(),
		"kind", obj.GetObjectKind().GroupVersionKind().Kind,
		"name", obj.GetName(),
		"namespace", obj.GetNamespace(),
	)

	existing := obj.DeepCopyObject().(client.Object)
	key := client.ObjectKeyFromObject(obj)

	err = k8sClient.Get(ctx, key, existing)
	if err != nil {
		if client.IgnoreNotFound(err) == nil {
			// Resource doesn't exist, create it
			if err := k8sClient.Create(ctx, obj); err != nil {
				return fmt.Errorf("failed to create resource %s/%s: %w", obj.GetObjectKind().GroupVersionKind().Kind, obj.GetName(), err)
			}
			logger.Info("Created platform source resource", "kind", obj.GetObjectKind().GroupVersionKind().Kind, "name", obj.GetName())
		} else {
			return fmt.Errorf("failed to check if resource exists: %w", err)
		}
	} else {
		// Resource exists, update it
		obj.SetResourceVersion(existing.GetResourceVersion())
		if err := k8sClient.Update(ctx, obj); err != nil {
			return fmt.Errorf("failed to update resource %s/%s: %w", obj.GetObjectKind().GroupVersionKind().Kind, obj.GetName(), err)
		}
		logger.Info("Updated platform source resource", "kind", obj.GetObjectKind().GroupVersionKind().Kind, "name", obj.GetName())
	}

	return nil
}

// parsePlatformSourceURL parses the source URL and returns the source type and repository URL.
// Supports formats:
//   - oci://registry.example.com/repo
//   - https://github.com/user/repo
//   - http://github.com/user/repo
//   - ssh://git@github.com/user/repo
func parsePlatformSourceURL(sourceURL string) (sourceType, repoURL string, err error) {
	sourceURL = strings.TrimSpace(sourceURL)

	if strings.HasPrefix(sourceURL, "oci://") {
		return "oci", sourceURL, nil
	}

	if strings.HasPrefix(sourceURL, "https://") || strings.HasPrefix(sourceURL, "http://") || strings.HasPrefix(sourceURL, "ssh://") {
		return "git", sourceURL, nil
	}

	return "", "", fmt.Errorf("unsupported source URL scheme (expected oci://, https://, http://, or ssh://): %s", sourceURL)
}

// parseRefSpec parses a reference specification string in the format "key1=value1,key2=value2".
// Returns a map of key-value pairs.
func parseRefSpec(refSpec string) (map[string]string, error) {
	result := make(map[string]string)

	refSpec = strings.TrimSpace(refSpec)
	if refSpec == "" {
		return result, nil
	}

	pairs := strings.Split(refSpec, ",")
	for _, pair := range pairs {
		pair = strings.TrimSpace(pair)
		if pair == "" {
			continue
		}

		// Split on first '=' only to allow '=' in values (e.g., digest=sha256:...)
		idx := strings.Index(pair, "=")
		if idx == -1 {
			return nil, fmt.Errorf("invalid reference specification format: %q (expected key=value)", pair)
		}

		key := strings.TrimSpace(pair[:idx])
		value := strings.TrimSpace(pair[idx+1:])

		if key == "" {
			return nil, fmt.Errorf("empty key in reference specification: %q", pair)
		}
		if value == "" {
			return nil, fmt.Errorf("empty value for key %q in reference specification", key)
		}

		result[key] = value
	}

	return result, nil
}

// Valid reference keys for OCI repositories
var validOCIRefKeys = map[string]bool{
	"digest":       true,
	"semver":       true,
	"semverFilter": true,
	"tag":          true,
}

// Valid reference keys for Git repositories
var validGitRefKeys = map[string]bool{
	"branch": true,
	"tag":    true,
	"semver": true,
	"name":   true,
	"commit": true,
}

// validateOCIRef validates reference keys for OCI repositories
func validateOCIRef(refMap map[string]string) error {
	for key := range refMap {
		if !validOCIRefKeys[key] {
			return fmt.Errorf("invalid OCI reference key %q (valid keys: digest, semver, semverFilter, tag)", key)
		}
	}

	// Validate digest format if provided
	if digest, ok := refMap["digest"]; ok {
		if !strings.HasPrefix(digest, "sha256:") {
			return fmt.Errorf("digest must be in format 'sha256:<hash>', got: %s", digest)
		}
	}

	return nil
}

// validateGitRef validates reference keys for Git repositories
func validateGitRef(refMap map[string]string) error {
	for key := range refMap {
		if !validGitRefKeys[key] {
			return fmt.Errorf("invalid Git reference key %q (valid keys: branch, tag, semver, name, commit)", key)
		}
	}

	// Validate commit format if provided (should be a hex string)
	if commit, ok := refMap["commit"]; ok {
		if len(commit) < 7 {
			return fmt.Errorf("commit SHA should be at least 7 characters, got: %s", commit)
		}
		for _, c := range commit {
			if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F')) {
				return fmt.Errorf("commit SHA should be a hexadecimal string, got: %s", commit)
			}
		}
	}

	return nil
}

// generateOCIRepository creates an OCIRepository resource
func generateOCIRepository(name, repoURL string, refMap map[string]string) (*sourcev1.OCIRepository, error) {
	if err := validateOCIRef(refMap); err != nil {
		return nil, err
	}

	obj := &sourcev1.OCIRepository{
		TypeMeta: metav1.TypeMeta{
			APIVersion: sourcev1.GroupVersion.String(),
			Kind:       sourcev1.OCIRepositoryKind,
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: "cozy-system",
		},
		Spec: sourcev1.OCIRepositorySpec{
			URL:      repoURL,
			Interval: metav1.Duration{Duration: 5 * time.Minute},
		},
	}

	// Set reference if any ref options are provided
	if len(refMap) > 0 {
		obj.Spec.Reference = &sourcev1.OCIRepositoryRef{
			Digest:       refMap["digest"],
			SemVer:       refMap["semver"],
			SemverFilter: refMap["semverFilter"],
			Tag:          refMap["tag"],
		}
	}

	return obj, nil
}

// generateGitRepository creates a GitRepository resource
func generateGitRepository(name, repoURL string, refMap map[string]string) (*sourcev1.GitRepository, error) {
	if err := validateGitRef(refMap); err != nil {
		return nil, err
	}

	obj := &sourcev1.GitRepository{
		TypeMeta: metav1.TypeMeta{
			APIVersion: sourcev1.GroupVersion.String(),
			Kind:       sourcev1.GitRepositoryKind,
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: "cozy-system",
		},
		Spec: sourcev1.GitRepositorySpec{
			URL:      repoURL,
			Interval: metav1.Duration{Duration: 5 * time.Minute},
		},
	}

	// Set reference if any ref options are provided
	if len(refMap) > 0 {
		obj.Spec.Reference = &sourcev1.GitRepositoryRef{
			Branch: refMap["branch"],
			Tag:    refMap["tag"],
			SemVer: refMap["semver"],
			Name:   refMap["name"],
			Commit: refMap["commit"],
		}
	}

	return obj, nil
}
