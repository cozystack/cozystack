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
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"time"

	// Import all Kubernetes client auth plugins (e.g. Azure, GCP, OIDC, etc.)
	// to ensure that exec-entrypoint and run can make use of them.
	_ "k8s.io/client-go/plugin/pkg/client/auth"

	cozyv1alpha1 "github.com/cozystack/cozystack/api/v1alpha1"
	helmv2 "github.com/fluxcd/helm-controller/api/v2"
	sourcev1 "github.com/fluxcd/source-controller/api/v1"
	sourcewatcherv1beta1 "github.com/fluxcd/source-watcher/api/v2/v1beta1"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/dynamic"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/healthz"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"
	"sigs.k8s.io/controller-runtime/pkg/webhook"

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

	flag.StringVar(&metricsAddr, "metrics-bind-address", ":8080", "The address the metric endpoint binds to.")
	flag.StringVar(&probeAddr, "health-probe-bind-address", ":8081", "The address the probe endpoint binds to.")
	flag.BoolVar(&enableLeaderElection, "leader-elect", false,
		"Enable leader election for controller manager. "+
			"Enabling this will ensure there is only one active controller manager.")
	flag.BoolVar(&secureMetrics, "metrics-secure", false,
		"If set the metrics endpoint is served securely")
	flag.BoolVar(&enableHTTP2, "enable-http2", false,
		"If set, HTTP/2 will be enabled for the metrics and webhook servers")

	opts := zap.Options{
		Development: true,
	}
	opts.BindFlags(flag.CommandLine)
	flag.Parse()

	ctrl.SetLogger(zap.New(zap.UseFlagOptions(&opts)))

	config := ctrl.GetConfigOrDie()

	// Phase 1: Install fluxcd-operator and fluxcd, wait for CRDs
	// This allows controller manager to start (it needs CRDs to be registered)
	// Use a direct (non-cached) client for bootstrap since manager cache is not started yet
	bootstrapClient, err := client.New(config, client.Options{Scheme: scheme})
	if err != nil {
		setupLog.Error(err, "failed to create bootstrap client")
		os.Exit(1)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
	defer cancel()

	setupLog.Info("Starting bootstrap phase 1: fluxcd installation")
	if err := runBootstrapPhase1(ctx, bootstrapClient); err != nil {
		setupLog.Error(err, "bootstrap phase 1 failed")
		os.Exit(1)
	}

	// Wait for GitRepository CRD (needed for PlatformConfiguration controller)
	// HelmRelease CRD is already waited for in phase 1
	setupLog.Info("Waiting for GitRepository CRD (needed for PlatformConfiguration controller)")
	if err := waitForCRDs(ctx, bootstrapClient, "gitrepositories.source.toolkit.fluxcd.io"); err != nil {
		setupLog.Error(err, "failed to wait for GitRepository CRD, PlatformConfiguration controller will not be registered")
	}

	// Now that CRDs are available, we can start the controller manager
	// The controller manager needs CRDs to be registered in the scheme
	setupLog.Info("Starting controller manager (CRDs are now available)")
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

	// Setup PlatformReconciler
	firstReconcileDone := make(chan struct{})
	platformReconciler := &operator.PlatformReconciler{
		Client:             mgr.GetClient(),
		Scheme:             mgr.GetScheme(),
		FirstReconcileDone: firstReconcileDone,
	}
	if err = platformReconciler.SetupWithManager(mgr); err != nil {
		setupLog.Error(err, "unable to create controller", "controller", "Platform")
		os.Exit(1)
	}

	bundleReconciler := &operator.CozystackBundleReconciler{
		Client: mgr.GetClient(),
		Scheme: mgr.GetScheme(),
	}
	if err = bundleReconciler.SetupWithManager(mgr); err != nil {
		setupLog.Error(err, "unable to create controller", "controller", "CozystackBundle")
		os.Exit(1)
	}


	// PlatformConfigurationReconciler needs GitRepository and HelmRelease CRDs
	// These CRDs are created in phase 1, so we register the controller after phase 1 completes
	// Check if CRDs are available (we already waited for them above)
	crdCtx, crdCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer crdCancel()
	if err := waitForCRDs(crdCtx, bootstrapClient, "gitrepositories.source.toolkit.fluxcd.io", "helmreleases.helm.toolkit.fluxcd.io"); err == nil {
		setupLog.Info("GitRepository and HelmRelease CRDs are available, registering PlatformConfiguration controller")
		platformConfigurationReconciler := &operator.CozystackPlatformConfigurationReconciler{
			Client: mgr.GetClient(),
			Scheme: mgr.GetScheme(),
		}
		if err := platformConfigurationReconciler.SetupWithManager(mgr); err != nil {
			setupLog.Error(err, "unable to create controller", "controller", "CozystackPlatformConfiguration")
		} else {
			setupLog.Info("PlatformConfiguration controller registered successfully")
		}
	} else {
		setupLog.Info("CRDs not yet available, PlatformConfiguration controller will not be registered", "error", err)
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

	// Phase 2: Install basic charts and other components after controller manager is ready
	// Start manager in a goroutine so we can proceed with phase 2
	mgrCtx := ctrl.SetupSignalHandler()
	mgrStarted := make(chan struct{})
	go func() {
		// Wait a bit for manager to initialize
		time.Sleep(2 * time.Second)
		close(mgrStarted)
		setupLog.Info("starting manager")
		if err := mgr.Start(mgrCtx); err != nil {
			setupLog.Error(err, "problem running manager")
			os.Exit(1)
		}
	}()

	// Wait for manager to initialize
	<-mgrStarted

	// Trigger a reconcile by creating/updating the ConfigMap to ensure first reconcile happens
	// This ensures the controller processes the ConfigMap and completes its first reconcile cycle
	setupLog.Info("Triggering first reconcile cycle")
	reconcileCtx, reconcileCancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer reconcileCancel()

	// Get the ConfigMap to trigger reconcile
	cm := &corev1.ConfigMap{}
	cmKey := types.NamespacedName{Namespace: "cozy-system", Name: "cozystack"}
	if err := bootstrapClient.Get(reconcileCtx, cmKey, cm); err == nil {
		// ConfigMap exists, update it to trigger reconcile
		cm.Labels = map[string]string{}
		cm.Labels["cozystack.io/reconcile-trigger"] = strconv.FormatInt(time.Now().Unix(), 10)
		if err := bootstrapClient.Update(reconcileCtx, cm); err != nil {
			setupLog.Info("Failed to trigger reconcile via ConfigMap update, continuing anyway", "error", err)
		}
	} else if apierrors.IsNotFound(err) {
		// ConfigMap doesn't exist yet, create it to trigger reconcile
		cm.ObjectMeta = metav1.ObjectMeta{
			Name:      "cozystack",
			Namespace: "cozy-system",
			Labels: map[string]string{
				"cozystack.io/reconcile-trigger": strconv.FormatInt(time.Now().Unix(), 10),
			},
		}
		cm.Data = map[string]string{
			"bundleName": "distro-full",
		}
		if err := bootstrapClient.Create(reconcileCtx, cm); err != nil {
			setupLog.Info("Failed to trigger reconcile via ConfigMap creation, continuing anyway", "error", err)
		}
	}

	// Wait for first reconcile cycle to complete
	setupLog.Info("Waiting for first reconcile cycle to complete")
	select {
	case <-firstReconcileDone:
		setupLog.Info("First reconcile cycle completed")
	case <-reconcileCtx.Done():
		setupLog.Info("Timeout waiting for first reconcile, proceeding with phase 2 anyway")
	}

	setupLog.Info("Starting bootstrap phase 2: basic charts installation")
	phase2Ctx, phase2Cancel := context.WithTimeout(context.Background(), 30*time.Minute)
	defer phase2Cancel()

	if err := runBootstrapPhase2(phase2Ctx, bootstrapClient); err != nil {
		setupLog.Error(err, "bootstrap phase 2 failed")
		os.Exit(1)
	}

	setupLog.Info("Bootstrap completed, controller manager is running")
	// Wait for manager (this blocks until context is cancelled)
	<-mgrCtx.Done()
}

// runBootstrapPhase1 installs cozystack-resource-definition-crd, fluxcd-operator and fluxcd, waits for CRDs
// This must complete before controller manager can start (manager needs CRDs registered)
// Basic charts (cilium, kubeovn) are NOT installed here - they are installed in phase 2 after manager starts
func runBootstrapPhase1(ctx context.Context, c client.Client) error {
	// Create cozy-system and cozy-public namespaces first (needed for ConfigMap and HelmRepositories)
	if err := ensureBootstrapNamespace(ctx, c, "cozy-system", true); err != nil {
		return fmt.Errorf("failed to create cozy-system namespace: %w", err)
	}
	if err := ensureBootstrapNamespace(ctx, c, "cozy-public", false); err != nil {
		return fmt.Errorf("failed to create cozy-public namespace: %w", err)
	}

	// Get bundle name
	bundle, err := getBundle(ctx, c)
	if err != nil {
		return err
	}
	setupLog.Info("Bundle detected", "bundle", bundle)

	// Calculate and run migrations
	version, err := calculateVersion()
	if err != nil {
		return err
	}
	setupLog.Info("Target version", "version", version)

	if err := runMigrations(ctx, c, version); err != nil {
		return err
	}

	// Create cozy-fluxcd namespace (needed for fluxcd-operator and fluxcd)
	if err := ensureBootstrapNamespace(ctx, c, "cozy-fluxcd", true); err != nil {
		return fmt.Errorf("failed to create cozy-fluxcd namespace: %w", err)
	}

	// Ensure cozystack-resource-definition-crd is installed
	// This CRD is needed for the controller manager to start
	if err := ensureCozystackResourceDefinitionCRD(ctx, c); err != nil {
		return err
	}

	// Ensure fluxcd-operator and fluxcd are installed
	// This installs/resumes helmreleases and waits for CRDs to be registered
	// After CRDs are available, controller manager can start
	if err := ensureFluxCD(ctx, c); err != nil {
		return err
	}

	setupLog.Info("Bootstrap phase 1 completed: fluxcd installed, CRDs available")
	return nil
}

// runBootstrapPhase2 installs basic charts and performs post-fluxcd operations
// This runs after controller manager has started (CRDs are available for manager)
func runBootstrapPhase2(ctx context.Context, c client.Client) error {
	setupLog.Info("Starting bootstrap phase 2: basic charts installation")

	// Get bundle name
	bundle, err := getBundle(ctx, c)
	if err != nil {
		return err
	}
	setupLog.Info("Bundle detected", "bundle", bundle)

	// Install basic charts (cilium, kubeovn) only if fluxcd is not ready
	// Basic charts are only needed during bootstrap when fluxcd is not ready yet
	fluxOK, err := fluxIsOK(ctx, c)
	if err != nil {
		return err
	}
	if !fluxOK {
		setupLog.Info("fluxcd is not ready, installing basic charts (controller manager is running)")
		if err := installBasicCharts(ctx, c, bundle); err != nil {
			return err
		}
	} else {
		setupLog.Info("fluxcd is ready, skipping basic charts installation")
	}

	// Unsuspend and update charts
	if err := unsuspendCozystackCharts(ctx, c); err != nil {
		return err
	}
	if err := updateCozystackCharts(ctx, c); err != nil {
		return err
	}

	setupLog.Info("Bootstrap phase 2 completed")
	return nil
}

// ensureBootstrapNamespace creates or updates a namespace for bootstrap operations
func ensureBootstrapNamespace(ctx context.Context, c client.Client, name string, privileged bool) error {
	namespace := &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{
			Name: name,
			Labels: map[string]string{
				"cozystack.io/system": "true",
			},
			Annotations: map[string]string{
				"helm.sh/resource-policy": "keep",
			},
		},
	}

	if privileged {
		namespace.Labels["pod-security.kubernetes.io/enforce"] = "privileged"
	}

	existingNs := &corev1.Namespace{}
	err := c.Get(ctx, types.NamespacedName{Name: name}, existingNs)
	if apierrors.IsNotFound(err) {
		setupLog.Info("Creating namespace for bootstrap", "name", name, "privileged", privileged)
		if err := c.Create(ctx, namespace); err != nil {
			return fmt.Errorf("failed to create namespace %s: %w", name, err)
		}
	} else if err != nil {
		return fmt.Errorf("failed to get namespace %s: %w", name, err)
	} else {
		// Update labels and annotations if needed
		needsUpdate := false
		if existingNs.Labels == nil {
			existingNs.Labels = make(map[string]string)
			needsUpdate = true
		}
		for k, v := range namespace.Labels {
			if existingNs.Labels[k] != v {
				existingNs.Labels[k] = v
				needsUpdate = true
			}
		}
		if existingNs.Annotations == nil {
			existingNs.Annotations = make(map[string]string)
			needsUpdate = true
		}
		for k, v := range namespace.Annotations {
			if existingNs.Annotations[k] != v {
				existingNs.Annotations[k] = v
				needsUpdate = true
			}
		}
		if needsUpdate {
			setupLog.Info("Updating namespace for bootstrap", "name", name)
			if err := c.Update(ctx, existingNs); err != nil {
				return fmt.Errorf("failed to update namespace %s: %w", name, err)
			}
		}
	}

	return nil
}

// Bootstrap helper functions (moved from installer logic)

func getBundle(ctx context.Context, c client.Client) (string, error) {
	cm := &corev1.ConfigMap{}
	key := types.NamespacedName{Namespace: "cozy-system", Name: "cozystack"}
	if err := c.Get(ctx, key, cm); err != nil {
		return "", err
	}
	bundle, ok := cm.Data["bundle-name"]
	if !ok {
		return "", fmt.Errorf("bundle-name not found in cozystack configmap")
	}
	return bundle, nil
}

func calculateVersion() (int, error) {
	migrationsDir := "scripts/migrations"
	entries, err := os.ReadDir(migrationsDir)
	if err != nil {
		return 0, fmt.Errorf("failed to read migrations directory: %w", err)
	}

	var versions []int
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		version, err := strconv.Atoi(entry.Name())
		if err != nil {
			continue
		}
		versions = append(versions, version)
	}

	if len(versions) == 0 {
		return 1, nil
	}

	sort.Ints(versions)
	return versions[len(versions)-1] + 1, nil
}

func runMigrations(ctx context.Context, c client.Client, targetVersion int) error {
	cm := &corev1.ConfigMap{}
	key := types.NamespacedName{Namespace: "cozy-system", Name: "cozystack-version"}
	var currentVersion int

	err := c.Get(ctx, key, cm)
	if err != nil {
		if apierrors.IsNotFound(err) {
			// First run: create ConfigMap with current version, skip migrations
			setupLog.Info("cozystack-version configmap does not exist, creating with current version", "version", targetVersion)
			newCM := &corev1.ConfigMap{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "cozystack-version",
					Namespace: "cozy-system",
				},
				Data: map[string]string{
					"version": strconv.Itoa(targetVersion),
				},
			}
			if err := c.Create(ctx, newCM); err != nil {
				return fmt.Errorf("failed to create version configmap: %w", err)
			}
			setupLog.Info("Created cozystack-version configmap with current version, skipping migrations")
			return nil
		} else {
			return err
		}
	} else {
		versionStr, ok := cm.Data["version"]
		if !ok {
			currentVersion = 0
		} else {
			currentVersion, err = strconv.Atoi(versionStr)
			if err != nil {
				setupLog.Info("Invalid version in configmap, starting from 0", "version", versionStr)
				currentVersion = 0
			}
		}
	}

	for currentVersion < targetVersion {
		nextVersion := currentVersion + 1
		setupLog.Info("Running migration", "from", currentVersion, "to", targetVersion)

		migrationPath := filepath.Join("scripts", "migrations", strconv.Itoa(currentVersion))
		if _, err := os.Stat(migrationPath); os.IsNotExist(err) {
			setupLog.Info("Migration script does not exist, skipping", "path", migrationPath)
			currentVersion = nextVersion
			continue
		}

		// Make script executable
		if err := os.Chmod(migrationPath, 0755); err != nil {
			return fmt.Errorf("failed to make migration script executable: %w", err)
		}

		// Run migration script
		cmd := exec.CommandContext(ctx, migrationPath)
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		if err := cmd.Run(); err != nil {
			return fmt.Errorf("migration %d failed: %w", currentVersion, err)
		}

		// Update version in configmap
		newCM := &corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "cozystack-version",
				Namespace: "cozy-system",
			},
			Data: map[string]string{
				"version": strconv.Itoa(nextVersion),
			},
		}

		// Try to update first
		existingCM := &corev1.ConfigMap{}
		err = c.Get(ctx, key, existingCM)
		if err != nil {
			if apierrors.IsNotFound(err) {
				// Create if doesn't exist
				if err := c.Create(ctx, newCM); err != nil {
					return fmt.Errorf("failed to create version configmap: %w", err)
				}
			} else {
				return fmt.Errorf("failed to get version configmap: %w", err)
			}
		} else {
			// Update existing
			newCM.ResourceVersion = existingCM.ResourceVersion
			if err := c.Update(ctx, newCM); err != nil {
				return fmt.Errorf("failed to update version configmap: %w", err)
			}
		}

		currentVersion = nextVersion
	}

	return nil
}

// ensureCozystackResourceDefinitionCRD installs cozystack-resource-definition-crd and waits for CRD
// This CRD is needed for the controller manager to start
func ensureCozystackResourceDefinitionCRD(ctx context.Context, c client.Client) error {
	// Check if CRD already exists
	crd := &apiextensionsv1.CustomResourceDefinition{}
	key := types.NamespacedName{Name: "cozystackresourcedefinitions.cozystack.io"}
	if err := c.Get(ctx, key, crd); err == nil {
		setupLog.Info("cozystack-resource-definition-crd CRD already exists, skipping installation")
		return nil
	} else if !apierrors.IsNotFound(err) {
		return fmt.Errorf("failed to check cozystack-resource-definition-crd CRD: %w", err)
	}

	// CRD doesn't exist, install it
	setupLog.Info("Installing cozystack-resource-definition-crd")
	cmd := exec.CommandContext(ctx, "make", "-C", "packages/system/cozystack-resource-definition-crd", "apply-locally")
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("failed to install cozystack-resource-definition-crd: %w", err)
	}

	// Wait for CRD
	if err := waitForCRDs(ctx, c, "cozystackresourcedefinitions.cozystack.io"); err != nil {
		return err
	}

	return nil
}

func ensureFluxCD(ctx context.Context, c client.Client) error {
	fluxOK, err := fluxIsOK(ctx, c)
	if err != nil {
		return err
	}
	if fluxOK {
		setupLog.Info("fluxcd is already ready, skipping installation")
		// Still need to ensure CRDs are available for controller manager
		// Check if CRDs exist
		if err := waitForCRDs(ctx, c, "helmreleases.helm.toolkit.fluxcd.io", "helmrepositories.source.toolkit.fluxcd.io"); err != nil {
			return err
		}
		return nil
	}
	setupLog.Info("fluxcd is not ready, proceeding with installation/resume")

	// Install fluxcd-operator
	hr := &helmv2.HelmRelease{}
	key := types.NamespacedName{Namespace: "cozy-fluxcd", Name: "fluxcd-operator"}
	err = c.Get(ctx, key, hr)
	if err == nil {
		// HelmRelease exists, apply and resume it
		setupLog.Info("Applying and resuming fluxcd-operator helmrelease")
		cmd := exec.CommandContext(ctx, "make", "-C", "packages/system/fluxcd-operator", "apply", "resume")
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		if err := cmd.Run(); err != nil {
			return fmt.Errorf("failed to apply and resume fluxcd-operator: %w", err)
		}
	} else if apierrors.IsNotFound(err) {
		// HelmRelease doesn't exist, need to create it
		setupLog.Info("Creating fluxcd-operator using make (TODO: use helm-controller API)")
		cmd := exec.CommandContext(ctx, "make", "-C", "packages/system/fluxcd-operator", "apply-locally")
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		if err := cmd.Run(); err != nil {
			return fmt.Errorf("failed to install fluxcd-operator: %w", err)
		}
	} else if meta.IsNoMatchError(err) {
		// CRD for HelmRelease doesn't exist yet, need to install fluxcd-operator first
		setupLog.Info("HelmRelease CRD not found, installing fluxcd-operator to create CRDs")
		cmd := exec.CommandContext(ctx, "make", "-C", "packages/system/fluxcd-operator", "apply-locally")
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		if err := cmd.Run(); err != nil {
			return fmt.Errorf("failed to install fluxcd-operator: %w", err)
		}
	} else {
		return fmt.Errorf("failed to check fluxcd-operator: %w", err)
	}

	// Wait for FluxInstance CRD (created by fluxcd-operator)
	if err := waitForCRDs(ctx, c, "fluxinstances.fluxcd.controlplane.io"); err != nil {
		return err
	}

	// Install fluxcd (flux-instance) via FluxInstance resource
	// flux-instance is installed via FluxInstance, not HelmRelease
	// We need to use unstructured client to check FluxInstance since it's not in our scheme yet
	config := ctrl.GetConfigOrDie()
	dyn, err := dynamic.NewForConfig(config)
	if err != nil {
		return fmt.Errorf("failed to create dynamic client: %w", err)
	}

	fluxInstanceGVR := schema.GroupVersionResource{
		Group:    "fluxcd.controlplane.io",
		Version:  "v1",
		Resource: "fluxinstances",
	}

	_, err = dyn.Resource(fluxInstanceGVR).Namespace("cozy-fluxcd").Get(ctx, "flux", metav1.GetOptions{})
	if err == nil {
		// FluxInstance exists, check if HelmRelease exists before applying
		// HelmRelease CRD might not exist yet, so we need to check carefully
		helmReleaseGVR := schema.GroupVersionResource{
			Group:    "helm.toolkit.fluxcd.io",
			Version:  "v2",
			Resource: "helmreleases",
		}

		// Try to get HelmRelease - if CRD doesn't exist, this will return IsNoMatchError
		_, hrErr := dyn.Resource(helmReleaseGVR).Namespace("cozy-fluxcd").Get(ctx, "fluxcd", metav1.GetOptions{})
		if hrErr == nil {
			// HelmRelease exists, apply and resume it via make
			setupLog.Info("FluxInstance and HelmRelease exist, applying and resuming fluxcd")
			cmd := exec.CommandContext(ctx, "make", "-C", "packages/system/fluxcd", "apply", "resume")
			cmd.Stdout = os.Stdout
			cmd.Stderr = os.Stderr
			if err := cmd.Run(); err != nil {
				return fmt.Errorf("failed to apply and resume fluxcd: %w", err)
			}
		} else if apierrors.IsNotFound(hrErr) || meta.IsNoMatchError(hrErr) {
			// HelmRelease doesn't exist or CRD not available, use apply-locally
			setupLog.Info("FluxInstance exists but HelmRelease not found, creating fluxcd using apply-locally")
			cmd := exec.CommandContext(ctx, "make", "-C", "packages/system/fluxcd", "apply-locally")
			cmd.Stdout = os.Stdout
			cmd.Stderr = os.Stderr
			if err := cmd.Run(); err != nil {
				return fmt.Errorf("failed to install fluxcd: %w", err)
			}
		} else {
			return fmt.Errorf("failed to check fluxcd HelmRelease: %w", hrErr)
		}
	} else if apierrors.IsNotFound(err) {
		// FluxInstance doesn't exist, need to create it
		setupLog.Info("Creating fluxcd (flux-instance) using make")
		cmd := exec.CommandContext(ctx, "make", "-C", "packages/system/fluxcd", "apply-locally")
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		if err := cmd.Run(); err != nil {
			return fmt.Errorf("failed to install fluxcd: %w", err)
		}
	} else if meta.IsNoMatchError(err) {
		// CRD for FluxInstance doesn't exist yet, but we already waited for it above
		// This shouldn't happen, but if it does, try to install anyway
		setupLog.Info("FluxInstance CRD not found (unexpected), trying to install fluxcd anyway")
		cmd := exec.CommandContext(ctx, "make", "-C", "packages/system/fluxcd", "apply-locally")
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		if err := cmd.Run(); err != nil {
			return fmt.Errorf("failed to install fluxcd: %w", err)
		}
	} else {
		return fmt.Errorf("failed to check fluxcd FluxInstance: %w", err)
	}

	// Wait for HelmRelease CRD to be created by flux-instance
	// flux-instance installs Flux which creates HelmRelease CRD
	setupLog.Info("Waiting for HelmRelease CRD to be created by flux-instance")
	if err := waitForCRDs(ctx, c, "helmreleases.helm.toolkit.fluxcd.io"); err != nil {
		return fmt.Errorf("failed to wait for HelmRelease CRD: %w", err)
	}

	// Wait for CRDs
	// CRDs must be available before controller manager can start
	// Controller manager needs CRDs to be registered in the scheme
	if err := waitForCRDs(ctx, c, "helmreleases.helm.toolkit.fluxcd.io", "helmrepositories.source.toolkit.fluxcd.io"); err != nil {
		return err
	}

	// Note: We don't wait for fluxcd to be fully ready (source-controller, helm-controller deployments)
	// We only wait for CRDs to be registered, then controller manager can start
	// Basic charts will be installed in phase 2 after controller manager has started

	return nil
}

func fluxIsOK(ctx context.Context, c client.Client) (bool, error) {
	// Check source-controller deployment
	sourceDeploy := &appsv1.Deployment{}
	key := types.NamespacedName{Namespace: "cozy-fluxcd", Name: "source-controller"}
	if err := c.Get(ctx, key, sourceDeploy); err != nil {
		setupLog.Info("fluxcd check: source-controller deployment not found")
		return false, nil
	}
	if !isDeploymentAvailable(sourceDeploy) {
		setupLog.Info("fluxcd check: source-controller deployment not available")
		return false, nil
	}

	// Check helm-controller deployment
	helmDeploy := &appsv1.Deployment{}
	key = types.NamespacedName{Namespace: "cozy-fluxcd", Name: "helm-controller"}
	if err := c.Get(ctx, key, helmDeploy); err != nil {
		setupLog.Info("fluxcd check: helm-controller deployment not found")
		return false, nil
	}
	if !isDeploymentAvailable(helmDeploy) {
		setupLog.Info("fluxcd check: helm-controller deployment not available")
		return false, nil
	}

	// Check fluxcd helmrelease is ready
	hr := &helmv2.HelmRelease{}
	key = types.NamespacedName{Namespace: "cozy-fluxcd", Name: "fluxcd"}
	if err := c.Get(ctx, key, hr); err != nil {
		setupLog.Info("fluxcd check: fluxcd helmrelease not found")
		return false, nil
	}

	// Check if ready (this implicitly checks suspend, as suspended HelmRelease cannot be Ready)
	if hr.Status.Conditions != nil {
		for _, cond := range hr.Status.Conditions {
			if cond.Type == "Ready" && cond.Status == metav1.ConditionTrue {
				setupLog.Info("fluxcd check: fluxcd is ready")
				return true, nil
			}
		}
	}

	setupLog.Info("fluxcd check: fluxcd helmrelease not ready")
	return false, nil
}

func isDeploymentAvailable(deploy *appsv1.Deployment) bool {
	for _, cond := range deploy.Status.Conditions {
		if cond.Type == appsv1.DeploymentAvailable && cond.Status == corev1.ConditionTrue {
			return true
		}
	}
	return false
}

func waitForCRDs(ctx context.Context, c client.Client, crdNames ...string) error {
	for _, crdName := range crdNames {
		setupLog.Info("Waiting for CRD", "crd", crdName)

		err := wait.PollUntilContextTimeout(ctx, 1*time.Second, 60*time.Second, true, func(ctx context.Context) (bool, error) {
			crd := &apiextensionsv1.CustomResourceDefinition{}
			key := types.NamespacedName{Name: crdName}
			if err := c.Get(ctx, key, crd); err != nil {
				if apierrors.IsNotFound(err) {
					// CRD not found yet, continue waiting
					return false, nil
				}
				// Other error, return it
				return false, err
			}
			// CRD found
			setupLog.Info("CRD found", "crd", crdName)
			return true, nil
		})

		if err != nil {
			return fmt.Errorf("timeout waiting for CRD %s: %w", crdName, err)
		}
	}
	return nil
}

func resumeHelmRelease(ctx context.Context, c client.Client, hr *helmv2.HelmRelease) error {
	if !hr.Spec.Suspend {
		return nil
	}

	patch := client.MergeFrom(hr.DeepCopy())
	hr.Spec.Suspend = false

	if err := c.Patch(ctx, hr, patch); err != nil {
		return fmt.Errorf("failed to patch HelmRelease: %w", err)
	}

	return nil
}

func installBasicCharts(ctx context.Context, c client.Client, bundle string) error {
	// Check if cilium and kubeovn are present in CozystackBundle resources
	hasCilium := false
	hasKubeovn := false

	// List all CozystackBundle resources
	bundleList := &cozyv1alpha1.CozystackBundleList{}
	if err := c.List(ctx, bundleList); err != nil {
		setupLog.Info("Failed to list CozystackBundles, skipping component checks", "error", err)
		// If bundle loading fails, fall back to old behavior for backward compatibility
		if bundle == "paas-full" || bundle == "distro-full" {
			setupLog.Info("Installing cilium using make (fallback mode)")
			cmd := exec.CommandContext(ctx, "make", "-C", "packages/system/cilium", "apply", "resume")
			cmd.Stdout = os.Stdout
			cmd.Stderr = os.Stderr
			if err := cmd.Run(); err != nil {
				return fmt.Errorf("failed to install cilium: %w", err)
			}
		}
		if bundle == "paas-full" {
			setupLog.Info("Installing kubeovn using make (fallback mode)")
			cmd := exec.CommandContext(ctx, "make", "-C", "packages/system/kubeovn", "apply", "resume")
			cmd.Stdout = os.Stdout
			cmd.Stderr = os.Stderr
			if err := cmd.Run(); err != nil {
				return fmt.Errorf("failed to install kubeovn: %w", err)
			}
		}
		return nil
	}

	// Check all bundles for cilium and kubeovn packages
	for _, bundleResource := range bundleList.Items {
		for _, pkg := range bundleResource.Spec.Packages {
			if pkg.Name == "cilium" && !pkg.Disabled {
				hasCilium = true
			}
			if pkg.Name == "kubeovn" && !pkg.Disabled {
				hasKubeovn = true
			}
		}
	}

	// Install cilium only if present in bundle
	if hasCilium {
		// TODO: Create HelmRelease for cilium using helm-controller API
		setupLog.Info("Installing cilium using make (found in bundle)")
		cmd := exec.CommandContext(ctx, "make", "-C", "packages/system/cilium", "apply", "resume")
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		if err := cmd.Run(); err != nil {
			return fmt.Errorf("failed to install cilium: %w", err)
		}
	} else {
		setupLog.Info("Skipping cilium installation (not found in bundle)")
	}

	// Install kubeovn only if present in bundle
	if hasKubeovn {
		// TODO: Create HelmRelease for kubeovn using helm-controller API
		setupLog.Info("Installing kubeovn using make (found in bundle)")
		cmd := exec.CommandContext(ctx, "make", "-C", "packages/system/kubeovn", "apply", "resume")
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		if err := cmd.Run(); err != nil {
			return fmt.Errorf("failed to install kubeovn: %w", err)
		}
	} else {
		setupLog.Info("Skipping kubeovn installation (not found in bundle)")
	}

	return nil
}

func unsuspendCozystackCharts(ctx context.Context, c client.Client) error {
	hrList := &helmv2.HelmReleaseList{}
	if err := c.List(ctx, hrList); err != nil {
		return fmt.Errorf("failed to list HelmReleases: %w", err)
	}

	for _, hr := range hrList.Items {
		if !hr.Spec.Suspend {
			continue
		}

		// Check if it's from a Cozystack managed repository
		if hr.Spec.Chart == nil || hr.Spec.Chart.Spec.SourceRef.Name == "" {
			continue
		}

		sourceRef := hr.Spec.Chart.Spec.SourceRef
		repoKey := fmt.Sprintf("%s/%s", sourceRef.Namespace, sourceRef.Name)

		switch repoKey {
		case "cozy-system/cozystack-system", "cozy-public/cozystack-extra", "cozy-public/cozystack-apps":
			setupLog.Info("Unsuspending HelmRelease", "namespace", hr.Namespace, "name", hr.Name)
			if err := resumeHelmRelease(ctx, c, &hr); err != nil {
				setupLog.Error(err, "Failed to unsuspend HelmRelease", "namespace", hr.Namespace, "name", hr.Name)
				continue
			}
		}
	}

	return nil
}

func updateCozystackCharts(ctx context.Context, c client.Client) error {
	hrList := &helmv2.HelmReleaseList{}
	if err := c.List(ctx, hrList, client.MatchingLabels{"cozystack.io/ui": "true"}); err != nil {
		return fmt.Errorf("failed to list HelmReleases: %w", err)
	}

	for _, hr := range hrList.Items {
		if hr.Spec.Chart == nil {
			continue
		}

		// Update version to >= 0.0.0-0
		patch := client.MergeFrom(hr.DeepCopy())
		hr.Spec.Chart.Spec.Version = ">= 0.0.0-0"

		setupLog.Info("Updating HelmRelease to latest version", "namespace", hr.Namespace, "name", hr.Name)
		if err := c.Patch(ctx, &hr, patch); err != nil {
			setupLog.Error(err, "Failed to update HelmRelease", "namespace", hr.Namespace, "name", hr.Name)
			continue
		}
	}

	return nil
}
