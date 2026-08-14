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

package operator

import (
	"context"
	"fmt"
	"strings"
	"time"

	cozyv1alpha1 "github.com/cozystack/cozystack/api/v1alpha1"
	"github.com/cozystack/cozystack/pkg/config"
	helmv2 "github.com/fluxcd/helm-controller/api/v2"
	appsv1 "k8s.io/api/apps/v1"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/apiutil"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

const (
	// AnnotationSkipCozystackValues disables injection of cozystack-values secret into HelmRelease
	// This annotation should be placed on PackageSource
	AnnotationSkipCozystackValues = "operator.cozystack.io/skip-cozystack-values"
	// SecretCozystackValues is the name of the secret containing cluster and namespace configuration
	SecretCozystackValues = "cozystack-values"
	// SystemDefaultsLimitRangeName is the LimitRange the reconciler maintains in every
	// system namespace to default container memory requests and limits.
	SystemDefaultsLimitRangeName = "cozystack-system-defaults"
	// packageControllerFieldOwner is the server-side-apply field manager used for every
	// cluster object the Package reconciler owns outright.
	packageControllerFieldOwner = "cozystack-package-controller"
)

// parseCRDPolicy maps ComponentInstall.UpgradeCRDs to a helmv2.CRDsPolicy.
// Empty / nil preserves the helm-controller default (Skip on upgrade);
// the CRD enum marker restricts the string to Skip/Create/CreateReplace.
func parseCRDPolicy(install *cozyv1alpha1.ComponentInstall) helmv2.CRDsPolicy {
	if install == nil || install.UpgradeCRDs == "" {
		return ""
	}
	return helmv2.CRDsPolicy(install.UpgradeCRDs)
}

// PackageReconciler reconciles Package resources
type PackageReconciler struct {
	client.Client
	// APIReader reads straight from the API server, bypassing the manager's cache.
	// Used only for the per-namespace Pod scan behind the system defaults LimitRange:
	// routing that through the cached client would start a cluster-wide Pod informer
	// and cost the operator far more memory than the LimitRange saves. Optional —
	// when nil the scan falls back to Client, which is what the unit tests use.
	APIReader                 client.Reader
	Scheme                    *runtime.Scheme
	HelmReleaseInterval       time.Duration
	HelmReleaseRetryInterval  time.Duration
	HelmReleaseInstallTimeout time.Duration
	HelmReleaseUpgradeTimeout time.Duration
	HelmReleaseMaxHistory     int
	// SystemNamespaceMemoryLimit is the default container memory limit applied through a
	// LimitRange in every system namespace. A zero quantity disables the LimitRange and
	// removes any the reconciler previously created.
	SystemNamespaceMemoryLimit resource.Quantity
	// SystemNamespaceMemoryRequest is the matching default container memory request. It
	// must be set whenever the limit is, because Kubernetes otherwise defaults each
	// request to the limit and reserves that much at schedule time.
	SystemNamespaceMemoryRequest resource.Quantity
}

// buildHelmReleaseSpec assembles the Spec applied to every generated
// HelmRelease. RetryInterval drives recovery from failed install/upgrade
// attempts; Interval polls healthy releases.
func (r *PackageReconciler) buildHelmReleaseSpec(componentInstall *cozyv1alpha1.ComponentInstall, artifactName string) helmv2.HelmReleaseSpec {
	maxHistory := r.HelmReleaseMaxHistory
	spec := helmv2.HelmReleaseSpec{
		Interval:   metav1.Duration{Duration: r.HelmReleaseInterval},
		MaxHistory: &maxHistory,
		ChartRef: &helmv2.CrossNamespaceSourceReference{
			Kind:      "ExternalArtifact",
			Name:      artifactName,
			Namespace: "cozy-system",
		},
		Install: &helmv2.Install{
			Timeout: &metav1.Duration{Duration: r.HelmReleaseInstallTimeout},
			Strategy: &helmv2.InstallStrategy{
				Name:          string(helmv2.ActionStrategyRetryOnFailure),
				RetryInterval: &metav1.Duration{Duration: r.HelmReleaseRetryInterval},
			},
		},
		Upgrade: &helmv2.Upgrade{
			Timeout: &metav1.Duration{Duration: r.HelmReleaseUpgradeTimeout},
			Strategy: &helmv2.UpgradeStrategy{
				Name:          string(helmv2.ActionStrategyRetryOnFailure),
				RetryInterval: &metav1.Duration{Duration: r.HelmReleaseRetryInterval},
			},
			CRDs: parseCRDPolicy(componentInstall),
		},
	}
	// kstatus readiness (issue #2642): gate the release on the CR(s) it renders.
	// ResolveWaitStrategy couples the default — expressions imply poller when no
	// strategy is set, since healthCheckExprs are only evaluated under poller.
	if componentInstall != nil {
		spec.HealthCheckExprs = componentInstall.HealthCheckExprs
		spec.WaitStrategy = config.ResolveWaitStrategy(componentInstall.WaitStrategy, len(componentInstall.HealthCheckExprs) > 0)
	}
	return spec
}

// +kubebuilder:rbac:groups=cozystack.io,resources=packages,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=cozystack.io,resources=packages/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=cozystack.io,resources=packagesources,verbs=get;list;watch
// +kubebuilder:rbac:groups=helm.toolkit.fluxcd.io,resources=helmreleases,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=core,resources=namespaces,verbs=get;list;watch;create;update;patch
// +kubebuilder:rbac:groups=core,resources=limitranges,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=core,resources=pods,verbs=get;list;watch
// +kubebuilder:rbac:groups=apps,resources=deployments;statefulsets;daemonsets,verbs=get;list;watch
// +kubebuilder:rbac:groups=batch,resources=cronjobs;jobs,verbs=get;list;watch

// Reconcile is part of the main kubernetes reconciliation loop
func (r *PackageReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	pkg := &cozyv1alpha1.Package{}
	if err := r.Get(ctx, req.NamespacedName, pkg); err != nil {
		if apierrors.IsNotFound(err) {
			// Resource not found, return (ownerReference will handle cleanup)
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}

	// Get PackageSource with the same name
	packageSource := &cozyv1alpha1.PackageSource{}
	if err := r.Get(ctx, types.NamespacedName{Name: pkg.Name}, packageSource); err != nil {
		if apierrors.IsNotFound(err) {
			meta.SetStatusCondition(&pkg.Status.Conditions, metav1.Condition{
				Type:    "Ready",
				Status:  metav1.ConditionFalse,
				Reason:  "PackageSourceNotFound",
				Message: fmt.Sprintf("PackageSource %s not found", pkg.Name),
			})
			if err := r.Status().Update(ctx, pkg); err != nil {
				return ctrl.Result{}, err
			}
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}

	// Determine variant (default to "default" if not specified)
	variantName := pkg.Spec.Variant
	if variantName == "" {
		variantName = "default"
	}

	// Find the variant in PackageSource
	var variant *cozyv1alpha1.Variant
	for i := range packageSource.Spec.Variants {
		if packageSource.Spec.Variants[i].Name == variantName {
			variant = &packageSource.Spec.Variants[i]
			break
		}
	}

	if variant == nil {
		meta.SetStatusCondition(&pkg.Status.Conditions, metav1.Condition{
			Type:    "Ready",
			Status:  metav1.ConditionFalse,
			Reason:  "VariantNotFound",
			Message: fmt.Sprintf("Variant %s not found in PackageSource %s", variantName, pkg.Name),
		})
		if err := r.Status().Update(ctx, pkg); err != nil {
			return ctrl.Result{}, err
		}
		return ctrl.Result{}, nil
	}

	// Reconcile namespaces from components
	if err := r.reconcileNamespaces(ctx, pkg, variant); err != nil {
		logger.Error(err, "failed to reconcile namespaces")
		return ctrl.Result{}, err
	}

	// Update dependencies status
	if err := r.updateDependenciesStatus(ctx, pkg, variant); err != nil {
		logger.Error(err, "failed to update dependencies status")
		// Don't return error, continue with reconciliation
	}

	// Validate variant dependencies before creating HelmReleases
	// Check if all dependencies are ready based on status
	if !r.areDependenciesReady(pkg, variant) {
		logger.Info("variant dependencies not ready, skipping HelmRelease creation", "package", pkg.Name)
		meta.SetStatusCondition(&pkg.Status.Conditions, metav1.Condition{
			Type:    "Ready",
			Status:  metav1.ConditionFalse,
			Reason:  "DependenciesNotReady",
			Message: "One or more dependencies are not ready",
		})
		if err := r.Status().Update(ctx, pkg); err != nil {
			return ctrl.Result{}, err
		}
		// Return success to avoid requeue, but don't create HelmReleases
		return ctrl.Result{}, nil
	}

	// Create HelmReleases for components with Install section
	helmReleaseCount := 0
	for _, component := range variant.Components {
		// Skip components without Install section
		if component.Install == nil {
			continue
		}

		// Check if component is disabled via Package spec
		if pkgComponent, ok := pkg.Spec.Components[component.Name]; ok {
			if pkgComponent.Enabled != nil && !*pkgComponent.Enabled {
				logger.V(1).Info("skipping disabled component", "package", pkg.Name, "component", component.Name)
				continue
			}
		}

		// Build artifact name: <packagesource>-<variant>-<componentname> (with dots replaced by dashes)
		artifactName := fmt.Sprintf("%s-%s-%s",
			strings.ReplaceAll(packageSource.Name, ".", "-"),
			strings.ReplaceAll(variantName, ".", "-"),
			strings.ReplaceAll(component.Name, ".", "-"))

		// Namespace must be set
		namespace := component.Install.Namespace
		if namespace == "" {
			logger.Error(fmt.Errorf("component %s has empty namespace in Install section", component.Name), "namespace validation failed")
			meta.SetStatusCondition(&pkg.Status.Conditions, metav1.Condition{
				Type:    "Ready",
				Status:  metav1.ConditionFalse,
				Reason:  "InvalidConfiguration",
				Message: fmt.Sprintf("Component %s has empty namespace in Install section", component.Name),
			})
			if err := r.Status().Update(ctx, pkg); err != nil {
				return ctrl.Result{}, err
			}
			return ctrl.Result{}, fmt.Errorf("component %s has empty namespace in Install section", component.Name)
		}

		// Determine release name (from Install or use component name)
		releaseName := component.Install.ReleaseName
		if releaseName == "" {
			releaseName = component.Name
		}

		// Build labels
		labels := make(map[string]string)
		labels["cozystack.io/package"] = pkg.Name
		if component.Install.Privileged {
			labels["cozystack.io/privileged"] = "true"
		}

		// Create HelmRelease
		hr := &helmv2.HelmRelease{
			ObjectMeta: metav1.ObjectMeta{
				Name:      releaseName,
				Namespace: namespace,
				Labels:    labels,
			},
			Spec: r.buildHelmReleaseSpec(component.Install, artifactName),
		}

		// Add valuesFrom for cozystack-values secret unless disabled by annotation on PackageSource
		if packageSource.GetAnnotations()[AnnotationSkipCozystackValues] != "true" {
			hr.Spec.ValuesFrom = []helmv2.ValuesReference{
				{
					Kind: "Secret",
					Name: SecretCozystackValues,
				},
			}
		}

		// Set ownerReference
		gvk, err := apiutil.GVKForObject(pkg, r.Scheme)
		if err != nil {
			logger.Error(err, "failed to get GVK for Package")
			meta.SetStatusCondition(&pkg.Status.Conditions, metav1.Condition{
				Type:    "Ready",
				Status:  metav1.ConditionFalse,
				Reason:  "InternalError",
				Message: fmt.Sprintf("Failed to get GVK for Package: %v", err),
			})
			if err := r.Status().Update(ctx, pkg); err != nil {
				return ctrl.Result{}, err
			}
			return ctrl.Result{}, fmt.Errorf("failed to get GVK for Package: %w", err)
		}
		hr.OwnerReferences = []metav1.OwnerReference{
			{
				APIVersion: gvk.GroupVersion().String(),
				Kind:       gvk.Kind,
				Name:       pkg.Name,
				UID:        pkg.UID,
				Controller: func() *bool { b := true; return &b }(),
			},
		}

		// Merge values from Package spec if provided
		if pkgComponent, ok := pkg.Spec.Components[component.Name]; ok && pkgComponent.Values != nil {
			hr.Spec.Values = pkgComponent.Values
		}

		// Build DependsOn from component Install and variant DependsOn
		dependsOn, err := r.buildDependsOn(ctx, pkg, packageSource, variant, &component)
		if err != nil {
			logger.Error(err, "failed to build DependsOn", "component", component.Name)
			meta.SetStatusCondition(&pkg.Status.Conditions, metav1.Condition{
				Type:    "Ready",
				Status:  metav1.ConditionFalse,
				Reason:  "DependsOnFailed",
				Message: fmt.Sprintf("Failed to build DependsOn for component %s: %v", component.Name, err),
			})
			if err := r.Status().Update(ctx, pkg); err != nil {
				return ctrl.Result{}, err
			}
			// Return nil to stop reconciliation, error is recorded in status
			return ctrl.Result{}, nil
		}
		if len(dependsOn) > 0 {
			hr.Spec.DependsOn = dependsOn
		}

		// Set valuesFiles annotation
		if len(component.ValuesFiles) > 0 {
			if hr.Annotations == nil {
				hr.Annotations = make(map[string]string)
			}
			hr.Annotations["cozyhr.cozystack.io/values-files"] = strings.Join(component.ValuesFiles, ",")
		}

		if err := r.createOrUpdateHelmRelease(ctx, hr); err != nil {
			logger.Error(err, "failed to reconcile HelmRelease", "name", releaseName, "namespace", namespace)
			meta.SetStatusCondition(&pkg.Status.Conditions, metav1.Condition{
				Type:    "Ready",
				Status:  metav1.ConditionFalse,
				Reason:  "HelmReleaseFailed",
				Message: fmt.Sprintf("Failed to create HelmRelease %s: %v", releaseName, err),
			})
			if err := r.Status().Update(ctx, pkg); err != nil {
				return ctrl.Result{}, err
			}
			return ctrl.Result{}, err
		}

		helmReleaseCount++
		logger.Info("reconciled HelmRelease", "package", pkg.Name, "component", component.Name, "releaseName", releaseName, "namespace", namespace)
	}

	// Cleanup orphaned HelmReleases
	if err := r.cleanupOrphanedHelmReleases(ctx, pkg, variant); err != nil {
		logger.Error(err, "failed to cleanup orphaned HelmReleases")
		// Don't return error, continue with status update
	}

	// Update status with success message
	message := fmt.Sprintf("reconciliation succeeded, generated %d helmrelease(s)", helmReleaseCount)
	meta.SetStatusCondition(&pkg.Status.Conditions, metav1.Condition{
		Type:    "Ready",
		Status:  metav1.ConditionTrue,
		Reason:  "ReconciliationSucceeded",
		Message: message,
	})

	if err := r.Status().Update(ctx, pkg); err != nil {
		return ctrl.Result{}, err
	}

	logger.Info("reconciled Package", "name", pkg.Name, "helmReleaseCount", helmReleaseCount)

	// Update dependencies status for Packages that depend on this Package
	// This ensures they get re-enqueued when their dependency becomes ready
	if err := r.updateDependentPackagesDependencies(ctx, pkg.Name); err != nil {
		logger.V(1).Error(err, "failed to update dependent packages dependencies", "package", pkg.Name)
		// Don't return error, this is best-effort
	}

	// Dependent Packages will be automatically enqueued by the watch handler
	// when this Package's status is updated (see SetupWithManager watch handler)

	return ctrl.Result{}, nil
}

// createOrUpdateHelmRelease creates or updates a HelmRelease
func (r *PackageReconciler) createOrUpdateHelmRelease(ctx context.Context, hr *helmv2.HelmRelease) error {
	existing := &helmv2.HelmRelease{}
	key := types.NamespacedName{
		Name:      hr.Name,
		Namespace: hr.Namespace,
	}

	err := r.Get(ctx, key, existing)
	if apierrors.IsNotFound(err) {
		return r.Create(ctx, hr)
	} else if err != nil {
		return err
	}

	// Preserve resource version
	hr.SetResourceVersion(existing.GetResourceVersion())

	// Merge labels
	labels := hr.GetLabels()
	if labels == nil {
		labels = make(map[string]string)
	}
	for k, v := range existing.GetLabels() {
		if _, ok := labels[k]; !ok {
			labels[k] = v
		}
	}
	hr.SetLabels(labels)

	// Merge annotations
	annotations := hr.GetAnnotations()
	if annotations == nil {
		annotations = make(map[string]string)
	}
	for k, v := range existing.GetAnnotations() {
		if _, ok := annotations[k]; !ok {
			annotations[k] = v
		}
	}
	hr.SetAnnotations(annotations)

	hr.Spec.Suspend = existing.Spec.Suspend
	// Update Spec
	existing.Spec = hr.Spec
	existing.SetLabels(hr.GetLabels())
	existing.SetAnnotations(hr.GetAnnotations())
	existing.SetOwnerReferences(hr.GetOwnerReferences())

	return r.Update(ctx, existing)
}

// getVariantForPackage retrieves the Variant for a given Package
// Returns the Variant and an error if not found
// If c is nil, uses the reconciler's client
func (r *PackageReconciler) getVariantForPackage(ctx context.Context, pkg *cozyv1alpha1.Package, c client.Client) (*cozyv1alpha1.Variant, error) {
	// Use provided client or fall back to reconciler's client
	cl := c
	if cl == nil {
		cl = r.Client
	}

	// Determine variant name (default to "default" if not specified)
	variantName := pkg.Spec.Variant
	if variantName == "" {
		variantName = "default"
	}

	// Get the PackageSource
	packageSource := &cozyv1alpha1.PackageSource{}
	if err := cl.Get(ctx, types.NamespacedName{Name: pkg.Name}, packageSource); err != nil {
		if apierrors.IsNotFound(err) {
			return nil, fmt.Errorf("PackageSource %s not found", pkg.Name)
		}
		return nil, fmt.Errorf("failed to get PackageSource %s: %w", pkg.Name, err)
	}

	// Find the variant in PackageSource
	var variant *cozyv1alpha1.Variant
	for i := range packageSource.Spec.Variants {
		if packageSource.Spec.Variants[i].Name == variantName {
			variant = &packageSource.Spec.Variants[i]
			break
		}
	}

	if variant == nil {
		return nil, fmt.Errorf("variant %s not found in PackageSource %s", variantName, pkg.Name)
	}

	return variant, nil
}

// buildDependsOn builds DependsOn list for a component
// Includes:
// 1. Dependencies from component.Install.DependsOn (with namespace from referenced component)
// 2. Dependencies from variant.DependsOn (all components with Install from referenced Package)
func (r *PackageReconciler) buildDependsOn(ctx context.Context, pkg *cozyv1alpha1.Package, packageSource *cozyv1alpha1.PackageSource, variant *cozyv1alpha1.Variant, component *cozyv1alpha1.Component) ([]helmv2.DependencyReference, error) {
	logger := log.FromContext(ctx)
	dependsOn := []helmv2.DependencyReference{}

	// Build map of component names to their release names and namespaces in current variant
	componentMap := make(map[string]struct {
		releaseName string
		namespace   string
	})
	for _, comp := range variant.Components {
		if comp.Install == nil {
			continue
		}
		compNamespace := comp.Install.Namespace
		if compNamespace == "" {
			return nil, fmt.Errorf("component %s has empty namespace in Install section", comp.Name)
		}
		compReleaseName := comp.Install.ReleaseName
		if compReleaseName == "" {
			compReleaseName = comp.Name
		}
		componentMap[comp.Name] = struct {
			releaseName string
			namespace   string
		}{
			releaseName: compReleaseName,
			namespace:   compNamespace,
		}
	}

	// Add dependencies from component.Install.DependsOn
	if len(component.Install.DependsOn) > 0 {
		for _, depName := range component.Install.DependsOn {
			depComp, ok := componentMap[depName]
			if !ok {
				return nil, fmt.Errorf("component %s not found in variant for dependency %s", depName, component.Name)
			}
			dependsOn = append(dependsOn, helmv2.DependencyReference{
				Name:      depComp.releaseName,
				Namespace: depComp.namespace,
			})
			logger.V(1).Info("added component dependency", "component", component.Name, "dependsOn", depName, "releaseName", depComp.releaseName, "namespace", depComp.namespace)
		}
	}

	// Add dependencies from variant.DependsOn
	if len(variant.DependsOn) > 0 {
		for _, depPackageName := range variant.DependsOn {
			// Check if dependency is in IgnoreDependencies
			ignore := false
			for _, ignoreDep := range pkg.Spec.IgnoreDependencies {
				if ignoreDep == depPackageName {
					ignore = true
					break
				}
			}
			if ignore {
				logger.V(1).Info("ignoring dependency", "package", pkg.Name, "dependency", depPackageName)
				continue
			}

			// Get the Package
			depPackage := &cozyv1alpha1.Package{}
			if err := r.Get(ctx, types.NamespacedName{Name: depPackageName}, depPackage); err != nil {
				if apierrors.IsNotFound(err) {
					return nil, fmt.Errorf("dependent Package %s not found", depPackageName)
				}
				return nil, fmt.Errorf("failed to get dependent Package %s: %w", depPackageName, err)
			}

			// Get the variant from dependent Package
			depVariant, err := r.getVariantForPackage(ctx, depPackage, nil)
			if err != nil {
				return nil, fmt.Errorf("failed to get variant for dependent Package %s: %w", depPackageName, err)
			}

			// Add all components with Install from dependent variant
			for _, depComp := range depVariant.Components {
				if depComp.Install == nil {
					continue
				}

				// Check if component is disabled in dependent Package
				if depPkgComponent, ok := depPackage.Spec.Components[depComp.Name]; ok {
					if depPkgComponent.Enabled != nil && !*depPkgComponent.Enabled {
						continue
					}
				}

				depCompNamespace := depComp.Install.Namespace
				if depCompNamespace == "" {
					return nil, fmt.Errorf("component %s in dependent Package %s has empty namespace in Install section", depComp.Name, depPackageName)
				}
				depCompReleaseName := depComp.Install.ReleaseName
				if depCompReleaseName == "" {
					depCompReleaseName = depComp.Name
				}

				dependsOn = append(dependsOn, helmv2.DependencyReference{
					Name:      depCompReleaseName,
					Namespace: depCompNamespace,
				})
				logger.V(1).Info("added variant dependency", "package", pkg.Name, "dependency", depPackageName, "component", depComp.Name, "releaseName", depCompReleaseName, "namespace", depCompNamespace)
			}
		}
	}

	return dependsOn, nil
}

// updateDependenciesStatus updates the dependencies status in Package status
// It checks the readiness of each dependency and updates pkg.Status.Dependencies
// Old dependency keys that are no longer in the dependency list are removed
func (r *PackageReconciler) updateDependenciesStatus(ctx context.Context, pkg *cozyv1alpha1.Package, variant *cozyv1alpha1.Variant) error {
	logger := log.FromContext(ctx)

	// Initialize dependencies map if nil
	if pkg.Status.Dependencies == nil {
		pkg.Status.Dependencies = make(map[string]cozyv1alpha1.DependencyStatus)
	}

	// Build set of current dependencies (excluding ignored ones)
	currentDeps := make(map[string]bool)
	if len(variant.DependsOn) > 0 {
		for _, depPackageName := range variant.DependsOn {
			// Check if dependency is in IgnoreDependencies
			ignore := false
			for _, ignoreDep := range pkg.Spec.IgnoreDependencies {
				if ignoreDep == depPackageName {
					ignore = true
					break
				}
			}
			if ignore {
				logger.V(1).Info("ignoring dependency", "package", pkg.Name, "dependency", depPackageName)
				continue
			}
			currentDeps[depPackageName] = true
		}
	}

	// Remove old dependencies that are no longer in the list
	for depName := range pkg.Status.Dependencies {
		if !currentDeps[depName] {
			delete(pkg.Status.Dependencies, depName)
			logger.V(1).Info("removed old dependency from status", "package", pkg.Name, "dependency", depName)
		}
	}

	// Update status for each current dependency
	for depPackageName := range currentDeps {
		// Get the Package
		depPackage := &cozyv1alpha1.Package{}
		if err := r.Get(ctx, types.NamespacedName{Name: depPackageName}, depPackage); err != nil {
			if apierrors.IsNotFound(err) {
				// Dependency not found, mark as not ready
				pkg.Status.Dependencies[depPackageName] = cozyv1alpha1.DependencyStatus{
					Ready: false,
				}
				logger.V(1).Info("dependency not found, marking as not ready", "package", pkg.Name, "dependency", depPackageName)
				continue
			}
			// Error getting dependency, keep existing status or mark as not ready
			if _, exists := pkg.Status.Dependencies[depPackageName]; !exists {
				pkg.Status.Dependencies[depPackageName] = cozyv1alpha1.DependencyStatus{
					Ready: false,
				}
			}
			logger.V(1).Error(err, "failed to get dependency, keeping existing status", "package", pkg.Name, "dependency", depPackageName)
			continue
		}

		// Check Ready condition
		readyCondition := meta.FindStatusCondition(depPackage.Status.Conditions, "Ready")
		isReady := readyCondition != nil && readyCondition.Status == metav1.ConditionTrue

		// Update dependency status
		pkg.Status.Dependencies[depPackageName] = cozyv1alpha1.DependencyStatus{
			Ready: isReady,
		}
		logger.V(1).Info("updated dependency status", "package", pkg.Name, "dependency", depPackageName, "ready", isReady)
	}

	return nil
}

// areDependenciesReady checks if all dependencies are ready based on status
func (r *PackageReconciler) areDependenciesReady(pkg *cozyv1alpha1.Package, variant *cozyv1alpha1.Variant) bool {
	if len(variant.DependsOn) == 0 {
		return true
	}

	for _, depPackageName := range variant.DependsOn {
		// Check if dependency is in IgnoreDependencies
		ignore := false
		for _, ignoreDep := range pkg.Spec.IgnoreDependencies {
			if ignoreDep == depPackageName {
				ignore = true
				break
			}
		}
		if ignore {
			continue
		}

		// Check dependency status
		depStatus, exists := pkg.Status.Dependencies[depPackageName]
		if !exists || !depStatus.Ready {
			return false
		}
	}

	return true
}

// updateDependentPackagesDependencies updates dependencies status for all Packages that depend on the given Package
// This ensures dependent packages get re-enqueued when their dependency status changes
func (r *PackageReconciler) updateDependentPackagesDependencies(ctx context.Context, packageName string) error {
	logger := log.FromContext(ctx)

	// Get all Packages
	packageList := &cozyv1alpha1.PackageList{}
	if err := r.List(ctx, packageList); err != nil {
		return fmt.Errorf("failed to list Packages: %w", err)
	}

	// Get the updated Package to check its readiness
	updatedPkg := &cozyv1alpha1.Package{}
	if err := r.Get(ctx, types.NamespacedName{Name: packageName}, updatedPkg); err != nil {
		if apierrors.IsNotFound(err) {
			return nil // Package not found, nothing to update
		}
		return fmt.Errorf("failed to get Package %s: %w", packageName, err)
	}

	// Check Ready condition of the updated Package
	readyCondition := meta.FindStatusCondition(updatedPkg.Status.Conditions, "Ready")
	isReady := readyCondition != nil && readyCondition.Status == metav1.ConditionTrue

	// For each Package, check if it depends on the given Package
	for _, pkg := range packageList.Items {
		// Skip the Package itself
		if pkg.Name == packageName {
			continue
		}

		// Get variant
		variant, err := r.getVariantForPackage(ctx, &pkg, nil)
		if err != nil {
			// Continue if PackageSource or variant not found (best-effort operation)
			logger.V(1).Info("skipping package, failed to get variant", "package", pkg.Name, "error", err)
			continue
		}

		// Check if this Package depends on the given Package
		dependsOn := false
		for _, dep := range variant.DependsOn {
			// Check if dependency is in IgnoreDependencies
			ignore := false
			for _, ignoreDep := range pkg.Spec.IgnoreDependencies {
				if ignoreDep == dep {
					ignore = true
					break
				}
			}
			if ignore {
				continue
			}

			if dep == packageName {
				dependsOn = true
				break
			}
		}

		if dependsOn {
			// Update the dependency status in this Package
			if pkg.Status.Dependencies == nil {
				pkg.Status.Dependencies = make(map[string]cozyv1alpha1.DependencyStatus)
			}
			pkg.Status.Dependencies[packageName] = cozyv1alpha1.DependencyStatus{
				Ready: isReady,
			}
			if err := r.Status().Update(ctx, &pkg); err != nil {
				logger.V(1).Error(err, "failed to update dependency status for dependent Package", "package", pkg.Name, "dependency", packageName)
				continue
			}
			logger.V(1).Info("updated dependency status for dependent Package", "package", pkg.Name, "dependency", packageName, "ready", isReady)
		}
	}

	return nil
}

// reconcileNamespaces creates or updates namespaces based on components in the variant.
// For each namespace, it checks ALL Packages sharing that namespace to determine whether
// the namespace should be privileged — it is privileged if ANY Package has a privileged
// component installed in it.
func (r *PackageReconciler) reconcileNamespaces(ctx context.Context, pkg *cozyv1alpha1.Package, variant *cozyv1alpha1.Variant) error {
	logger := log.FromContext(ctx)

	// Collect namespaces from this Package's components
	targetNamespaces := make(map[string]struct{})
	for _, component := range variant.Components {
		if component.Install == nil {
			continue
		}
		if pkgComponent, ok := pkg.Spec.Components[component.Name]; ok {
			if pkgComponent.Enabled != nil && !*pkgComponent.Enabled {
				continue
			}
		}
		namespace := component.Install.Namespace
		if namespace == "" {
			return fmt.Errorf("component %s has empty namespace in Install section", component.Name)
		}
		targetNamespaces[namespace] = struct{}{}
	}

	// Determine which namespaces should be privileged by checking ALL Packages
	privileged, err := r.resolvePrivilegedNamespaces(ctx, targetNamespaces)
	if err != nil {
		return fmt.Errorf("failed to resolve privileged namespaces: %w", err)
	}

	// Create or update all namespaces
	for nsName := range targetNamespaces {
		isSystem := !strings.HasPrefix(nsName, "tenant-")

		namespace := &corev1.Namespace{
			ObjectMeta: metav1.ObjectMeta{
				Name:   nsName,
				Labels: make(map[string]string),
				Annotations: map[string]string{
					"helm.sh/resource-policy": "keep",
				},
			},
		}

		if isSystem {
			namespace.Labels["cozystack.io/system"] = "true"
		}

		if privileged[nsName] {
			namespace.Labels["pod-security.kubernetes.io/enforce"] = "privileged"
		}

		if err := r.createOrUpdateNamespace(ctx, namespace); err != nil {
			logger.Error(err, "failed to reconcile namespace", "name", nsName, "privileged", privileged[nsName])
			return fmt.Errorf("failed to reconcile namespace %s: %w", nsName, err)
		}
		logger.Info("reconciled namespace", "name", nsName, "privileged", privileged[nsName])

		if isSystem {
			// Logged and stepped over rather than returned. The LimitRange is
			// opportunistic hardening against the Talos OOM handler's victim
			// selection; a namespace that does not get one is back to the
			// behaviour of every release before it. Failing the Package reconcile
			// instead would leave the namespace's components uninstalled, making
			// the hardening more disruptive than the problem it mitigates.
			if err := r.reconcileSystemDefaultsLimitRange(ctx, nsName); err != nil {
				logger.Error(err, "failed to reconcile system defaults LimitRange", "namespace", nsName)
			}
		}
	}

	return nil
}

// reconcileSystemDefaultsLimitRange maintains the LimitRange that gives every container
// in a system namespace a default memory request and limit.
//
// The Talos userspace OOM handler (v1.12+) selects its victim by ranking cgroups with
// `memory_max.hasValue() ? 0.0 : {Besteffort: 1.0, Burstable: 0.5, ...}[class] *
// memory_current`, and discards every cgroup that scores zero. A pod whose containers all
// carry a memory limit has memory.max set on its cgroup, scores zero, and is never
// selected. A pod without one stays a candidate however little memory it is using and
// whatever actually caused the pressure — victim selection is decoupled from the trigger.
// System components are overwhelmingly the pods without limits, so they were the ones
// being killed on behalf of tenant workloads that were the real source of the pressure.
//
// Defaulting a limit across system namespaces takes those components out of the candidate
// set. Tenant namespaces are skipped because the tenant chart owns LimitRange policy
// there: packages/apps/tenant ships tenant-range-limits, which defaults container memory
// to 128Mi in every tenant namespace that declares resourceQuotas. Handing a second,
// system-owned LimitRange to those namespaces would layer a competing default on top of
// the chart's own, so the operator stays out.
//
// The limit is a ceiling rather than a reservation, so it is set well above real usage —
// the point is that memory.max exists, not that it binds. It must still stay above the
// largest memory request in any system namespace, because a defaulted limit below a
// container's own request is rejected at admission; findRequestAboveDefaultLimit is the
// guard for that and the long comment there explains why it is needed in this repo.
func (r *PackageReconciler) reconcileSystemDefaultsLimitRange(ctx context.Context, nsName string) error {
	logger := log.FromContext(ctx)

	// Disabled: drop a LimitRange left over from an earlier configuration so the knob
	// stays reversible.
	if r.SystemNamespaceMemoryLimit.IsZero() {
		return r.deleteSystemDefaultsLimitRange(ctx, nsName)
	}

	blocker, err := r.findRequestAboveDefaultLimit(ctx, nsName)
	if err != nil {
		// Without the scan there is no way to tell whether applying is safe, and
		// guessing either way risks the wedge the scan exists to prevent. Leave the
		// namespace exactly as it is and try again on the next reconcile.
		logger.Error(err, "skipping the system defaults LimitRange: could not read the namespace's pods and workloads", "namespace", nsName)
		return nil
	}
	if blocker != nil {
		logger.Error(fmt.Errorf("memory request above the configured default limit"),
			"not defaulting container memory in this namespace: the LimitRange would stop these pods being admitted; raise --system-namespace-memory-limit above the request below, or set it to 0 to turn the feature off",
			"namespace", nsName,
			"workload", blocker.workload,
			"container", blocker.container,
			"request", blocker.request.String(),
			"limit", r.SystemNamespaceMemoryLimit.String())
		// Also retract one applied earlier, when the request was still below the
		// limit. Leaving it would arm exactly the trap this branch exists to avoid.
		return r.deleteSystemDefaultsLimitRange(ctx, nsName)
	}

	limitRange := r.systemDefaultsLimitRange(nsName)
	limitRange.SetGroupVersionKind(corev1.SchemeGroupVersion.WithKind("LimitRange"))

	return r.Patch(ctx, limitRange, client.Apply, client.FieldOwner(packageControllerFieldOwner), client.ForceOwnership)
}

// deleteSystemDefaultsLimitRange removes the LimitRange this reconciler maintains,
// treating an already-absent one as success.
func (r *PackageReconciler) deleteSystemDefaultsLimitRange(ctx context.Context, nsName string) error {
	stale := &corev1.LimitRange{
		ObjectMeta: metav1.ObjectMeta{
			Name:      SystemDefaultsLimitRangeName,
			Namespace: nsName,
		},
	}
	if err := r.Delete(ctx, stale); err != nil && !apierrors.IsNotFound(err) {
		return err
	}
	return nil
}

// memoryRequestBlocker names the container that makes the configured default limit
// unsafe to apply in a namespace.
type memoryRequestBlocker struct {
	// workload is the object the request was read from, written as Kind/name:
	// "Pod/vmselect-0" for a live pod, "Deployment/vmselect" for a pod template. The
	// kind is part of the value rather than implied, because it tells an operator
	// reading the log which object to go and change — a pod whose request a mutating
	// webhook raised after admission is a different problem from a template that
	// declares the request outright.
	workload  string
	container string
	request   resource.Quantity
}

// findRequestAboveDefaultLimit reports the largest container memory request in nsName
// that exceeds the configured default limit, or nil when the LimitRange is safe to apply.
//
// A LimitRange default only ever reaches a container that declares no memory limit of its
// own, and the API server then validates the result: a request above the limit is
// rejected with "must be less than or equal to memory limit". So the only containers that
// can be broken by this feature are the ones with a memory request and no memory limit,
// and those are exactly what this scan looks for. A container that sets both is left out,
// because LimitRanger never touches it.
//
// This is not hypothetical in this repository. packages/system/monitoring and
// packages/system/monitoring-agents ship VerticalPodAutoscalers for cozy-monitoring with
// maxAllowed.memory of 8Gi (vmselect, vmstorage, vlselect, vlstorage) and 6G (vmagent),
// both above the 4Gi default this operator ships. They run with updateMode: Initial, so
// the VPA admission webhook writes the recommendation into the pod's requests at creation
// time, and it can do so on a busy cluster without any chart in the tree declaring a
// request that large.
//
// The scan therefore has to read live pods and workload pod templates, and neither alone
// is enough. A live pod is the only place a request raised by a mutating webhook after
// admission appears, and no rendered template can see it. A pod template is the only place
// a workload with no pods appears at all: a Deployment or StatefulSet at replicas: 0 and a
// CronJob between runs are ordinary steady states, and scanning pods alone declares such a
// namespace safe, applies the LimitRange, and turns the next scale-up or the next schedule
// into "must be less than or equal to memory limit" — a workload that cannot come back,
// discovered at the moment somebody needs it.
//
// The remedy is deliberately non-fatal. Applying the LimitRange would stop the affected
// workload from ever rolling, and returning an error would fail the whole Package
// reconcile and wedge the platform — both worse than the unbounded-memory problem this
// feature mitigates. The namespace keeps its pre-feature behaviour and the operator says
// loudly why. A List that fails is treated the same way by the caller, which skips the
// namespace rather than applying blind: the scan not running is not evidence that applying
// is safe.
//
// One gap survives all of this, narrower than the pod-only scan's. A container whose
// template requests less than the limit, whose pod would have a larger request injected at
// admission, and whose very first pod is rejected outright, appears nowhere: there is no
// pod to scan and no template that admits to the request. Only the rejection event records
// it.
func (r *PackageReconciler) findRequestAboveDefaultLimit(ctx context.Context, nsName string) (*memoryRequestBlocker, error) {
	reader := r.APIReader
	if reader == nil {
		reader = r.Client
	}

	var worst *memoryRequestBlocker
	// consider folds one pod spec into the running maximum. Sources below are scanned in
	// a fixed order and the comparison is strict, so equal requests keep the first source
	// scanned and which object gets named is deterministic rather than list-order luck.
	consider := func(spec *corev1.PodSpec, workload string) {
		// Init containers are defaulted by LimitRanger on the same terms.
		for _, containers := range [][]corev1.Container{spec.InitContainers, spec.Containers} {
			for _, c := range containers {
				if _, hasLimit := c.Resources.Limits[corev1.ResourceMemory]; hasLimit {
					continue
				}
				request, ok := c.Resources.Requests[corev1.ResourceMemory]
				if !ok || request.Cmp(r.SystemNamespaceMemoryLimit) <= 0 {
					continue
				}
				if worst == nil || request.Cmp(worst.request) > 0 {
					worst = &memoryRequestBlocker{workload: workload, container: c.Name, request: request}
				}
			}
		}
	}

	// Templates first, so that a workload and its own running pods — which declare the
	// same request — are reported as the workload. That is the object an operator has to
	// edit, and unlike a pod name it survives the next roll. A live pod outranks its own
	// template only when its request is strictly larger, which is exactly the case the
	// template cannot show: a webhook-injected request.
	//
	// ReplicaSets are deliberately absent. A live one is a copy of its Deployment's
	// template, already covered above, while the older revisions a Deployment keeps carry
	// superseded templates that no scale-up will ever instantiate — only an explicit
	// rollout undo brings one back. Scanning them would withhold the LimitRange from a
	// whole namespace over a request that was replaced releases ago.
	deployments := &appsv1.DeploymentList{}
	if err := reader.List(ctx, deployments, client.InNamespace(nsName)); err != nil {
		return nil, fmt.Errorf("failed to list deployments in namespace %s: %w", nsName, err)
	}
	for i := range deployments.Items {
		d := &deployments.Items[i]
		consider(&d.Spec.Template.Spec, "Deployment/"+d.Name)
	}

	statefulSets := &appsv1.StatefulSetList{}
	if err := reader.List(ctx, statefulSets, client.InNamespace(nsName)); err != nil {
		return nil, fmt.Errorf("failed to list statefulsets in namespace %s: %w", nsName, err)
	}
	for i := range statefulSets.Items {
		s := &statefulSets.Items[i]
		consider(&s.Spec.Template.Spec, "StatefulSet/"+s.Name)
	}

	daemonSets := &appsv1.DaemonSetList{}
	if err := reader.List(ctx, daemonSets, client.InNamespace(nsName)); err != nil {
		return nil, fmt.Errorf("failed to list daemonsets in namespace %s: %w", nsName, err)
	}
	for i := range daemonSets.Items {
		d := &daemonSets.Items[i]
		consider(&d.Spec.Template.Spec, "DaemonSet/"+d.Name)
	}

	// A CronJob is scanned whether or not it is suspended: suspend is the same kind of
	// dormancy as replicas: 0, undone by the same kind of human action, and exempting it
	// would leave the gap this scan exists to close open for one kind.
	cronJobs := &batchv1.CronJobList{}
	if err := reader.List(ctx, cronJobs, client.InNamespace(nsName)); err != nil {
		return nil, fmt.Errorf("failed to list cronjobs in namespace %s: %w", nsName, err)
	}
	for i := range cronJobs.Items {
		cj := &cronJobs.Items[i]
		consider(&cj.Spec.JobTemplate.Spec.Template.Spec, "CronJob/"+cj.Name)
	}

	// Jobs are named separately from their owners because not every Job has one: a Helm
	// hook or a migration Job is created standalone, and a Job with spec.suspend has no
	// pods to scan yet. A Job that has already completed or given up is skipped for the
	// same reason a terminated pod is — it will never create another pod.
	jobs := &batchv1.JobList{}
	if err := reader.List(ctx, jobs, client.InNamespace(nsName)); err != nil {
		return nil, fmt.Errorf("failed to list jobs in namespace %s: %w", nsName, err)
	}
	for i := range jobs.Items {
		j := &jobs.Items[i]
		if jobFinished(j) {
			continue
		}
		consider(&j.Spec.Template.Spec, "Job/"+j.Name)
	}

	pods := &corev1.PodList{}
	if err := reader.List(ctx, pods, client.InNamespace(nsName)); err != nil {
		return nil, fmt.Errorf("failed to list pods in namespace %s: %w", nsName, err)
	}
	for i := range pods.Items {
		pod := &pods.Items[i]
		// A finished pod is never recreated from this spec, so its request cannot
		// block anything.
		if pod.Status.Phase == corev1.PodSucceeded || pod.Status.Phase == corev1.PodFailed {
			continue
		}
		consider(&pod.Spec, "Pod/"+pod.Name)
	}

	return worst, nil
}

// jobFinished reports whether a Job has run to completion or given up. Either way it will
// never create another pod, so its template cannot block admission of anything.
func jobFinished(job *batchv1.Job) bool {
	for _, c := range job.Status.Conditions {
		if c.Status != corev1.ConditionTrue {
			continue
		}
		if c.Type == batchv1.JobComplete || c.Type == batchv1.JobFailed {
			return true
		}
	}
	return false
}

// systemDefaultsLimitRange builds the LimitRange applied to a system namespace.
func (r *PackageReconciler) systemDefaultsLimitRange(nsName string) *corev1.LimitRange {
	return &corev1.LimitRange{
		ObjectMeta: metav1.ObjectMeta{
			Name:      SystemDefaultsLimitRangeName,
			Namespace: nsName,
		},
		Spec: corev1.LimitRangeSpec{
			Limits: []corev1.LimitRangeItem{{
				Type:           corev1.LimitTypeContainer,
				Default:        corev1.ResourceList{corev1.ResourceMemory: r.SystemNamespaceMemoryLimit},
				DefaultRequest: corev1.ResourceList{corev1.ResourceMemory: r.SystemNamespaceMemoryRequest},
			}},
		},
	}
}

// resolvePrivilegedNamespaces checks all PackageSources and their corresponding Packages
// to determine which of the given namespaces require the privileged PodSecurity level.
// A namespace is privileged if ANY active Package has a component with privileged: true in it.
func (r *PackageReconciler) resolvePrivilegedNamespaces(ctx context.Context, namespaces map[string]struct{}) (map[string]bool, error) {
	result := make(map[string]bool)

	packageSources := &cozyv1alpha1.PackageSourceList{}
	if err := r.List(ctx, packageSources); err != nil {
		return nil, fmt.Errorf("failed to list PackageSources: %w", err)
	}

	for i := range packageSources.Items {
		ps := &packageSources.Items[i]

		// Check if a Package exists for this PackageSource
		pkg := &cozyv1alpha1.Package{}
		if err := r.Get(ctx, types.NamespacedName{Name: ps.Name}, pkg); err != nil {
			if apierrors.IsNotFound(err) {
				continue
			}
			return nil, fmt.Errorf("failed to get Package %s: %w", ps.Name, err)
		}

		// Resolve active variant
		variantName := pkg.Spec.Variant
		if variantName == "" {
			variantName = "default"
		}

		var variant *cozyv1alpha1.Variant
		for j := range ps.Spec.Variants {
			if ps.Spec.Variants[j].Name == variantName {
				variant = &ps.Spec.Variants[j]
				break
			}
		}
		if variant == nil {
			continue
		}

		for _, component := range variant.Components {
			if component.Install == nil {
				continue
			}
			if pkgComponent, ok := pkg.Spec.Components[component.Name]; ok {
				if pkgComponent.Enabled != nil && !*pkgComponent.Enabled {
					continue
				}
			}
			if _, relevant := namespaces[component.Install.Namespace]; !relevant {
				continue
			}
			if component.Install.Privileged {
				result[component.Install.Namespace] = true
			}
		}
	}

	return result, nil
}

// createOrUpdateNamespace creates or updates a namespace using server-side apply.
func (r *PackageReconciler) createOrUpdateNamespace(ctx context.Context, namespace *corev1.Namespace) error {
	namespace.SetGroupVersionKind(corev1.SchemeGroupVersion.WithKind("Namespace"))
	return r.Patch(ctx, namespace, client.Apply, client.FieldOwner(packageControllerFieldOwner), client.ForceOwnership)
}

// cleanupOrphanedHelmReleases removes HelmReleases that are no longer needed
func (r *PackageReconciler) cleanupOrphanedHelmReleases(ctx context.Context, pkg *cozyv1alpha1.Package, variant *cozyv1alpha1.Variant) error {
	logger := log.FromContext(ctx)

	// Build map of desired HelmRelease names (from components with Install)
	desiredReleases := make(map[types.NamespacedName]bool)
	for _, component := range variant.Components {
		if component.Install == nil {
			continue
		}

		// Check if component is disabled via Package spec
		if pkgComponent, ok := pkg.Spec.Components[component.Name]; ok {
			if pkgComponent.Enabled != nil && !*pkgComponent.Enabled {
				continue
			}
		}

		namespace := component.Install.Namespace
		if namespace == "" {
			// Skip components with empty namespace (they shouldn't exist anyway)
			continue
		}

		releaseName := component.Install.ReleaseName
		if releaseName == "" {
			releaseName = component.Name
		}

		desiredReleases[types.NamespacedName{
			Name:      releaseName,
			Namespace: namespace,
		}] = true
	}

	// Find all HelmReleases owned by this Package
	hrList := &helmv2.HelmReleaseList{}
	if err := r.List(ctx, hrList, client.MatchingLabels{
		"cozystack.io/package": pkg.Name,
	}); err != nil {
		return err
	}

	// Delete HelmReleases that are not in desired list
	for _, hr := range hrList.Items {
		key := types.NamespacedName{
			Name:      hr.Name,
			Namespace: hr.Namespace,
		}
		if !desiredReleases[key] {
			logger.Info("deleting orphaned HelmRelease", "name", hr.Name, "namespace", hr.Namespace, "package", pkg.Name)
			if err := r.Delete(ctx, &hr); err != nil && !apierrors.IsNotFound(err) {
				logger.Error(err, "failed to delete orphaned HelmRelease", "name", hr.Name, "namespace", hr.Namespace)
			}
		}
	}

	return nil
}

// SetupWithManager sets up the controller with the Manager.
func (r *PackageReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		Named("cozystack-package").
		For(&cozyv1alpha1.Package{}).
		Owns(&helmv2.HelmRelease{}).
		Watches(
			&cozyv1alpha1.PackageSource{},
			handler.EnqueueRequestsFromMapFunc(func(ctx context.Context, obj client.Object) []reconcile.Request {
				ps, ok := obj.(*cozyv1alpha1.PackageSource)
				if !ok {
					return nil
				}
				// Find Package with the same name as PackageSource
				// PackageSource and Package share the same name
				pkg := &cozyv1alpha1.Package{}
				if err := mgr.GetClient().Get(ctx, types.NamespacedName{Name: ps.Name}, pkg); err != nil {
					// Package not found, that's ok - it might not exist yet
					return nil
				}
				// Trigger reconcile for the corresponding Package
				return []reconcile.Request{{
					NamespacedName: types.NamespacedName{
						Name: pkg.Name,
					},
				}}
			}),
		).
		Watches(
			&cozyv1alpha1.Package{},
			handler.EnqueueRequestsFromMapFunc(func(ctx context.Context, obj client.Object) []reconcile.Request {
				updatedPkg, ok := obj.(*cozyv1alpha1.Package)
				if !ok {
					return nil
				}
				// Find all Packages that depend on this Package
				packageList := &cozyv1alpha1.PackageList{}
				if err := mgr.GetClient().List(ctx, packageList); err != nil {
					return nil
				}
				var requests []reconcile.Request
				for _, pkg := range packageList.Items {
					if pkg.Name == updatedPkg.Name {
						continue // Skip the Package itself
					}
					// Get variant to check dependencies
					variant, err := r.getVariantForPackage(ctx, &pkg, mgr.GetClient())
					if err != nil {
						// Continue if PackageSource or variant not found
						continue
					}
					// Check if this variant depends on updatedPkg
					for _, dep := range variant.DependsOn {
						// Check if dependency is in IgnoreDependencies
						ignore := false
						for _, ignoreDep := range pkg.Spec.IgnoreDependencies {
							if ignoreDep == dep {
								ignore = true
								break
							}
						}
						if ignore {
							continue
						}
						if dep == updatedPkg.Name {
							requests = append(requests, reconcile.Request{
								NamespacedName: types.NamespacedName{
									Name: pkg.Name,
								},
							})
							break
						}
					}
				}
				return requests
			}),
		).
		Complete(r)
}
