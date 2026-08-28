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
	fluxmeta "github.com/fluxcd/pkg/apis/meta"
	sourcev1 "github.com/fluxcd/source-controller/api/v1"
	sourcewatcherv1beta1 "github.com/fluxcd/source-watcher/api/v2/v1beta1"
	corev1 "k8s.io/api/core/v1"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	"k8s.io/apimachinery/pkg/api/resource"
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
	"github.com/cozystack/cozystack/internal/crdinstall"
	"github.com/cozystack/cozystack/internal/fluxinstall"
	"github.com/cozystack/cozystack/internal/operator"
	"github.com/cozystack/cozystack/internal/telemetry"
	"github.com/cozystack/cozystack/pkg/config"
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
	var installCRDs bool
	var installFlux bool
	var disableTelemetry bool
	var telemetryEndpoint string
	var telemetryInterval string
	var helmReleaseInterval string
	var helmReleaseRetryInterval string
	var helmReleaseInstallTimeout string
	var helmReleaseUpgradeTimeout string
	var helmReleaseMaxHistory int
	var cozyValuesSecretName string
	var cozyValuesSecretNamespace string
	var cozyValuesNamespaceSelector string
	var platformSourceURL string
	var platformSourceName string
	var platformSourceRef string
	var platformSourceSecret string
	var systemNamespaceMemoryLimit string
	var systemNamespaceMemoryRequest string

	flag.StringVar(&metricsAddr, "metrics-bind-address", ":8080", "The address the metric endpoint binds to.")
	flag.StringVar(&probeAddr, "health-probe-bind-address", ":8081", "The address the probe endpoint binds to.")
	flag.BoolVar(&enableLeaderElection, "leader-elect", false,
		"Enable leader election for controller manager. "+
			"Enabling this will ensure there is only one active controller manager.")
	flag.BoolVar(&secureMetrics, "metrics-secure", false,
		"If set the metrics endpoint is served securely")
	flag.BoolVar(&enableHTTP2, "enable-http2", false,
		"If set, HTTP/2 will be enabled for the metrics and webhook servers")
	flag.BoolVar(&installCRDs, "install-crds", false, "Install Cozystack CRDs before starting reconcile loop")
	flag.BoolVar(&installFlux, "install-flux", false, "Install Flux components before starting reconcile loop")
	flag.BoolVar(&disableTelemetry, "disable-telemetry", false,
		"Disable telemetry collection")
	flag.StringVar(&telemetryEndpoint, "telemetry-endpoint", "https://telemetry.cozystack.io",
		"Endpoint for sending telemetry data")
	flag.StringVar(&telemetryInterval, "telemetry-interval", "15m",
		"Interval between telemetry data collection (e.g. 15m, 1h)")
	flag.StringVar(&helmReleaseInterval, "helmrelease-interval", "5m",
		"Reconcile interval applied to HelmReleases created by the Package reconciler. "+
			"Lower values speed up dependency-blocked retries (e.g. during E2E install) at the cost of "+
			"controller load. Production default 5m matches existing behaviour.")
	flag.StringVar(&helmReleaseRetryInterval, "helmrelease-retry-interval", "30s",
		"Retry interval applied to Install.Strategy and Upgrade.Strategy of HelmReleases created "+
			"by the Package reconciler. With Strategy.Name=RetryOnFailure, this controls how long the "+
			"controller waits between failed install/upgrade attempts. Decoupled from --helmrelease-interval "+
			"(which is the healthy reconcile cadence) so failures recover fast without polling healthy "+
			"releases at the same fast cadence.")
	flag.StringVar(&helmReleaseInstallTimeout, "helmrelease-install-timeout", "10m",
		"Timeout for the Helm install action of HelmReleases created by the Package reconciler "+
			"(Spec.Install.Timeout). Bounds how long an individual Kubernetes operation (Job/hook/wait) "+
			"may take during install.")
	flag.StringVar(&helmReleaseUpgradeTimeout, "helmrelease-upgrade-timeout", "10m",
		"Timeout for the Helm upgrade action of HelmReleases created by the Package reconciler "+
			"(Spec.Upgrade.Timeout). Bounds how long an individual Kubernetes operation (Job/hook/wait) "+
			"may take during upgrade.")
	flag.IntVar(&helmReleaseMaxHistory, "helmrelease-max-history", 5,
		"Number of release revisions Helm keeps for HelmReleases created by the Package reconciler "+
			"(Spec.MaxHistory). 0 means unlimited; 5 matches Helm's default. Lower values reduce "+
			"per-release Secret accumulation in clusters that bounce HRs frequently (e.g. E2E sandboxes).")
	flag.StringVar(&platformSourceURL, "platform-source-url", "", "Platform source URL (oci:// or https://). If specified, generates OCIRepository or GitRepository resource.")
	flag.StringVar(&platformSourceName, "platform-source-name", "cozystack-platform", "Name for the generated platform source resource and PackageSource")
	flag.StringVar(&platformSourceRef, "platform-source-ref", "", "Reference specification as key=value pairs (e.g., 'branch=main' or 'digest=sha256:...,tag=v1.0'). For OCI: digest, semver, semverFilter, tag. For Git: branch, tag, semver, name, commit.")
	flag.StringVar(&platformSourceSecret, "platform-source-secret", "", "Name of the Secret in cozy-system namespace containing credentials for the platform source. Sets spec.secretRef on the generated OCIRepository or GitRepository. Secret type depends on the source kind: kubernetes.io/dockerconfigjson for OCI (oci://...); Opaque with username+password (or bearerToken) for Git over HTTPS; Opaque with identity (PEM private key) and known_hosts for Git over SSH.")
	flag.StringVar(&cozyValuesSecretName, "cozy-values-secret-name", "cozystack-values", "The name of the secret containing cluster-wide configuration values.")
	flag.StringVar(&cozyValuesSecretNamespace, "cozy-values-secret-namespace", "cozy-system", "The namespace of the secret containing cluster-wide configuration values.")
	flag.StringVar(&cozyValuesNamespaceSelector, "cozy-values-namespace-selector", "cozystack.io/system=true", "The label selector for namespaces where the cluster-wide configuration values must be replicated.")
	flag.StringVar(&systemNamespaceMemoryLimit, "system-namespace-memory-limit", DefaultSystemNamespaceMemoryLimit,
		"Default container memory limit applied through a LimitRange in every system namespace. "+
			"Keeps system components out of the Talos userspace OOM handler's victim set, which only "+
			"considers cgroups with no memory.max. The default is deliberately far above any real "+
			"working set: the handler discards a cgroup for having a memory.max at all, whatever its "+
			"value, so what this buys is immunity rather than a fitted ceiling, and a ceiling nothing "+
			"reaches cannot turn a rare pressure-driven kill into a deterministic one. Lower it to make "+
			"it bind, per component or fleet-wide, once real usage is known. It should stay above the "+
			"largest memory request in any system namespace, since a defaulted limit below a "+
			"container's own request fails admission. "+
			"Where a namespace does hold such a container, that namespace's ceiling is raised to clear "+
			"the request rather than the LimitRange being withheld - a loose memory.max still takes the "+
			"pod out of the victim set where no memory.max does not - and it drops back to this value "+
			"once the request has been absent for a grace period, since a scan finding nothing is also "+
			"what a pod between deletion and recreation looks like. Grep the operator log for \"raising "+
			"the default container memory limit\" to see which namespace, workload and container caused "+
			"it, and for \"lowering this namespace's container memory ceiling\" to see it come back down. "+
			"Scope is every namespace a Package targets that is not a tenant namespace, which "+
			"includes kube-system - cozystack-scheduler is installed there - so namespaces owned "+
			"by the underlying distribution are covered too. A namespace where another LimitRange "+
			"already says anything about memory is left alone, in any field and at either the "+
			"Container or the Pod scope: a second default leaves the effective ceiling to the order "+
			"LimitRanger iterates them, while a max below this default, a min above the paired "+
			"request, or a maxLimitRequestRatio each reject the pod at admission. Empty or 0 "+
			"disables the LimitRange and removes it from every namespace an active Package still "+
			"targets; a namespace whose Package has since been removed keeps its LimitRange, "+
			"labelled app.kubernetes.io/managed-by=cozystack-package-controller.")
	flag.StringVar(&systemNamespaceMemoryRequest, "system-namespace-memory-request", DefaultSystemNamespaceMemoryRequest,
		"Default container memory request paired with --system-namespace-memory-limit. Set small and "+
			"explicitly: Kubernetes defaults an unset request to the limit, which would reserve the full "+
			"limit for every system container at schedule time. 0 is supported and means the opposite "+
			"trade rather than a broken value: the API server accepts a LimitRange with defaultRequest 0, "+
			"and a container with no memory request of its own is then admitted with an explicit request "+
			"of 0 and the default limit, so it still gets the memory.max this feature exists to give it, "+
			"while the scheduler reserves nothing for it - the same scheduling signal system components "+
			"had before this feature, no worse and no better.")

	opts := zap.Options{
		Development: true,
	}
	opts.BindFlags(flag.CommandLine)
	flag.Parse()

	ctrl.SetLogger(zap.New(zap.UseFlagOptions(&opts)))

	parseFlag := func(flagName, raw string) time.Duration {
		d, err := config.ParsePositiveDuration(flagName, raw)
		if err != nil {
			setupLog.Error(err, "invalid duration flag")
			os.Exit(1)
		}
		return d
	}
	hrIntervalDuration := parseFlag("--helmrelease-interval", helmReleaseInterval)
	hrRetryIntervalDuration := parseFlag("--helmrelease-retry-interval", helmReleaseRetryInterval)
	hrInstallTimeoutDuration := parseFlag("--helmrelease-install-timeout", helmReleaseInstallTimeout)
	hrUpgradeTimeoutDuration := parseFlag("--helmrelease-upgrade-timeout", helmReleaseUpgradeTimeout)
	if helmReleaseMaxHistory < 0 {
		setupLog.Error(fmt.Errorf("--helmrelease-max-history must be >= 0"), "invalid value", "value", helmReleaseMaxHistory)
		os.Exit(1)
	}

	systemNSMemoryLimit, systemNSMemoryRequest, err := parseSystemNamespaceMemory(systemNamespaceMemoryLimit, systemNamespaceMemoryRequest)
	if err != nil {
		setupLog.Error(err, "invalid system namespace memory flags",
			"limit", systemNamespaceMemoryLimit, "request", systemNamespaceMemoryRequest)
		os.Exit(1)
	}

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

	// Initialize the controller manager
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

	// Set up signal handler early so install phases respect SIGTERM
	mgrCtx := ctrl.SetupSignalHandler()

	// Install Cozystack CRDs before starting reconcile loop
	if installCRDs {
		setupLog.Info("Installing Cozystack CRDs before starting reconcile loop")
		installCtx, installCancel := context.WithTimeout(mgrCtx, 2*time.Minute)
		defer installCancel()

		if err := crdinstall.Install(installCtx, directClient, crdinstall.WriteEmbeddedManifests); err != nil {
			setupLog.Error(err, "failed to install CRDs")
			os.Exit(1)
		}
		setupLog.Info("CRD installation completed successfully")
	}

	// Install Flux before starting reconcile loop
	if installFlux {
		setupLog.Info("Installing Flux components before starting reconcile loop")
		installCtx, installCancel := context.WithTimeout(mgrCtx, 5*time.Minute)
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
		setupLog.Info("Generating platform source resource", "url", platformSourceURL, "name", platformSourceName, "ref", platformSourceRef, "secret", platformSourceSecret)
		installCtx, installCancel := context.WithTimeout(mgrCtx, 2*time.Minute)
		defer installCancel()

		// Use direct client for pre-start operations (cache is not ready yet)
		if err := installPlatformSourceResource(installCtx, directClient, platformSourceURL, platformSourceName, platformSourceRef, platformSourceSecret); err != nil {
			setupLog.Error(err, "failed to install platform source resource")
			os.Exit(1)
		} else {
			setupLog.Info("Platform source resource installation completed successfully")
		}
	}

	// Create platform PackageSource when CRDs are managed by the operator and
	// a platform source URL is configured. Without a URL there is no Flux source
	// resource to reference, so creating a PackageSource would leave a dangling SourceRef.
	if installCRDs && platformSourceURL != "" {
		sourceRefKind := "OCIRepository"
		sourceType, _, err := parsePlatformSourceURL(platformSourceURL)
		if err != nil {
			setupLog.Error(err, "failed to parse platform source URL for PackageSource")
			os.Exit(1)
		}
		if sourceType == "git" {
			sourceRefKind = "GitRepository"
		}
		setupLog.Info("Creating platform PackageSource", "platformSourceName", platformSourceName)
		psCtx, psCancel := context.WithTimeout(mgrCtx, 2*time.Minute)
		defer psCancel()
		if err := installPlatformPackageSource(psCtx, directClient, platformSourceName, sourceRefKind); err != nil {
			setupLog.Error(err, "failed to create platform PackageSource")
			os.Exit(1)
		}
		setupLog.Info("Platform PackageSource creation completed successfully")
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
		// Non-cached, so the per-namespace Pod scan guarding the system defaults
		// LimitRange does not start a cluster-wide Pod informer in an operator
		// whose whole purpose here is to bound memory.
		APIReader:                 mgr.GetAPIReader(),
		Scheme:                    mgr.GetScheme(),
		HelmReleaseInterval:       hrIntervalDuration,
		HelmReleaseRetryInterval:  hrRetryIntervalDuration,
		HelmReleaseInstallTimeout: hrInstallTimeoutDuration,
		HelmReleaseUpgradeTimeout: hrUpgradeTimeoutDuration,
		HelmReleaseMaxHistory:     helmReleaseMaxHistory,

		SystemNamespaceMemoryLimit:   systemNSMemoryLimit,
		SystemNamespaceMemoryRequest: systemNSMemoryRequest,
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

	// Parse telemetry interval
	interval, err := time.ParseDuration(telemetryInterval)
	if err != nil {
		setupLog.Error(err, "invalid telemetry interval")
		os.Exit(1)
	}

	// Configure telemetry
	telemetryConfig := telemetry.Config{
		Disabled: disableTelemetry,
		Endpoint: telemetryEndpoint,
		Interval: interval,
	}

	// Initialize telemetry collector
	// Use APIReader (non-cached) because the manager's cache is filtered
	// and doesn't include resources needed for telemetry (e.g., kube-system namespace, nodes, etc.)
	collector, err := telemetry.NewOperatorCollector(mgr.GetAPIReader(), &telemetryConfig, config)
	if err != nil {
		setupLog.V(1).Info("unable to create telemetry collector, telemetry will be disabled", "error", err)
	}

	if collector != nil {
		if err := mgr.Add(collector); err != nil {
			setupLog.V(1).Info("unable to set up telemetry collector, continuing without telemetry", "error", err)
		}
	}

	setupLog.Info("Starting controller manager")
	if err := mgr.Start(mgrCtx); err != nil {
		setupLog.Error(err, "problem running manager")
		os.Exit(1)
	}
}

// installPlatformSourceResource generates and installs a Flux source resource (OCIRepository or GitRepository)
// based on the platform source URL
func installPlatformSourceResource(ctx context.Context, k8sClient client.Client, sourceURL, resourceName, refSpec, secretName string) error {
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
		obj, err = generateOCIRepository(resourceName, repoURL, refMap, secretName)
		if err != nil {
			return fmt.Errorf("failed to generate OCIRepository: %w", err)
		}
	case "git":
		obj, err = generateGitRepository(resourceName, repoURL, refMap, secretName)
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

// The shipped defaults for the two system-namespace memory knobs. Named rather than written
// into flag.StringVar directly so the flag registration and the test that pins them read the
// same constant: the previous form duplicated the literal, and a change to the flag left the
// test asserting a value the operator no longer shipped.
const (
	DefaultSystemNamespaceMemoryLimit   = "32Gi"
	DefaultSystemNamespaceMemoryRequest = "32Mi"
)

// parseSystemNamespaceMemory turns the --system-namespace-memory-limit and
// --system-namespace-memory-request flag values into the quantities the Package reconciler
// takes, rejecting the combinations that would fail later and further away.
//
// Empty is how both flags are disabled and parses to the zero quantity; for the limit that
// means "no LimitRange, and remove any this operator created", which is the documented
// off switch. A negative quantity parses fine as a Quantity and is meaningless as memory,
// so it is caught here rather than being written into a LimitRange the API server rejects.
//
// The pair check is the one that matters most. A LimitRange whose defaultRequest exceeds
// its default is rejected by the API server, and because the reconciler applies one per
// system namespace on every Package reconcile, a bad pair would wedge namespace
// reconciliation for every system package with an error nothing connects back to a flag.
// A request of 0 against a non-zero limit passes deliberately: it is the supported way to
// take the memory.max without reserving anything at schedule time, not a broken value.
// Both zero is the disabled case and never reaches the comparison.
func parseSystemNamespaceMemory(rawLimit, rawRequest string) (limit, request resource.Quantity, err error) {
	parse := func(flagName, raw string) (resource.Quantity, error) {
		if raw == "" {
			return resource.Quantity{}, nil
		}
		q, err := resource.ParseQuantity(raw)
		if err != nil {
			return resource.Quantity{}, fmt.Errorf("%s: %w", flagName, err)
		}
		if q.Sign() < 0 {
			return resource.Quantity{}, fmt.Errorf("%s must not be negative, got %s", flagName, raw)
		}
		return q, nil
	}

	if limit, err = parse("--system-namespace-memory-limit", rawLimit); err != nil {
		return resource.Quantity{}, resource.Quantity{}, err
	}
	if request, err = parse("--system-namespace-memory-request", rawRequest); err != nil {
		return resource.Quantity{}, resource.Quantity{}, err
	}
	if !limit.IsZero() && request.Cmp(limit) > 0 {
		return resource.Quantity{}, resource.Quantity{}, fmt.Errorf(
			"--system-namespace-memory-request (%s) must not exceed --system-namespace-memory-limit (%s)",
			request.String(), limit.String())
	}
	return limit, request, nil
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
func generateOCIRepository(name, repoURL string, refMap map[string]string, secretName string) (*sourcev1.OCIRepository, error) {
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

	// Set secretRef if secret name is provided
	if secretName != "" {
		obj.Spec.SecretRef = &fluxmeta.LocalObjectReference{
			Name: secretName,
		}
	}

	return obj, nil
}

// generateGitRepository creates a GitRepository resource
func generateGitRepository(name, repoURL string, refMap map[string]string, secretName string) (*sourcev1.GitRepository, error) {
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

	// Set secretRef if secret name is provided
	if secretName != "" {
		obj.Spec.SecretRef = &fluxmeta.LocalObjectReference{
			Name: secretName,
		}
	}

	return obj, nil
}

// installPlatformPackageSource creates the platform PackageSource resource
// that references the Flux source resource (OCIRepository or GitRepository).
//
// The variant list is intentionally hardcoded here. These are platform-defined
// deployment profiles (not user-extensible), matching what was previously in
// the Helm template. Changes require a new operator build and release.
func installPlatformPackageSource(ctx context.Context, k8sClient client.Client, platformSourceName, sourceRefKind string) error {
	logger := log.FromContext(ctx)

	packageSourceName := "cozystack." + platformSourceName

	ps := &cozyv1alpha1.PackageSource{
		TypeMeta: metav1.TypeMeta{
			APIVersion: cozyv1alpha1.GroupVersion.String(),
			Kind:       "PackageSource",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name: packageSourceName,
			Annotations: map[string]string{
				"operator.cozystack.io/skip-cozystack-values": "true",
			},
		},
		Spec: cozyv1alpha1.PackageSourceSpec{
			SourceRef: &cozyv1alpha1.PackageSourceRef{
				Kind:      sourceRefKind,
				Name:      platformSourceName,
				Namespace: "cozy-system",
				Path:      "/",
			},
		},
	}

	variantData := []struct {
		name        string
		valuesFiles []string
	}{
		{"default", []string{"values.yaml"}},
		{"isp-full", []string{"values.yaml", "values-isp-full.yaml"}},
		{"isp-hosted", []string{"values.yaml", "values-isp-hosted.yaml"}},
		{"isp-full-generic", []string{"values.yaml", "values-isp-full-generic.yaml"}},
	}

	variants := make([]cozyv1alpha1.Variant, len(variantData))
	for i, v := range variantData {
		variants[i] = cozyv1alpha1.Variant{
			Name: v.name,
			Components: []cozyv1alpha1.Component{
				{
					Name: "platform",
					Path: "core/platform",
					Install: &cozyv1alpha1.ComponentInstall{
						Namespace:   "cozy-system",
						ReleaseName: "cozystack-platform",
					},
					ValuesFiles: v.valuesFiles,
				},
			},
		}
	}
	ps.Spec.Variants = variants

	logger.Info("Applying platform PackageSource", "name", packageSourceName)

	patchOptions := &client.PatchOptions{
		FieldManager: "cozystack-operator",
		Force:        func() *bool { b := true; return &b }(),
	}

	if err := k8sClient.Patch(ctx, ps, client.Apply, patchOptions); err != nil {
		return fmt.Errorf("failed to apply PackageSource %s: %w", packageSourceName, err)
	}

	logger.Info("Applied platform PackageSource", "name", packageSourceName)
	return nil
}
