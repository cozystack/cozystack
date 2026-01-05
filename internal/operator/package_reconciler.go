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

	cozyv1alpha1 "github.com/cozystack/cozystack/api/v1alpha1"
	helmv2 "github.com/fluxcd/helm-controller/api/v2"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
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
)

// PackageReconciler reconciles Package resources
type PackageReconciler struct {
	client.Client
	Scheme *runtime.Scheme
}

// +kubebuilder:rbac:groups=cozystack.io,resources=packages,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=cozystack.io,resources=packages/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=cozystack.io,resources=packagesources,verbs=get;list;watch
// +kubebuilder:rbac:groups=helm.toolkit.fluxcd.io,resources=helmreleases,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=core,resources=namespaces,verbs=get;list;watch;create;update;patch

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
			Spec: helmv2.HelmReleaseSpec{
				Interval: metav1.Duration{Duration: 5 * 60 * 1000000000}, // 5m
				ChartRef: &helmv2.CrossNamespaceSourceReference{
					Kind:      "ExternalArtifact",
					Name:      artifactName,
					Namespace: "cozy-system",
				},
				Install: &helmv2.Install{
					Remediation: &helmv2.InstallRemediation{
						Retries: -1,
					},
				},
				Upgrade: &helmv2.Upgrade{
					Remediation: &helmv2.UpgradeRemediation{
						Retries: -1,
					},
				},
			},
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

// reconcileNamespaces creates or updates namespaces based on components in the variant
func (r *PackageReconciler) reconcileNamespaces(ctx context.Context, pkg *cozyv1alpha1.Package, variant *cozyv1alpha1.Variant) error {
	logger := log.FromContext(ctx)

	// Collect namespaces from components
	// Map: namespace -> {isPrivileged}
	type namespaceInfo struct {
		privileged bool
	}
	namespacesMap := make(map[string]namespaceInfo)

	for _, component := range variant.Components {
		// Skip components without Install section
		if component.Install == nil {
			continue
		}

		// Check if component is disabled via Package spec
		if pkgComponent, ok := pkg.Spec.Components[component.Name]; ok {
			if pkgComponent.Enabled != nil && !*pkgComponent.Enabled {
				continue
			}
		}

		// Namespace must be set
		namespace := component.Install.Namespace
		if namespace == "" {
			return fmt.Errorf("component %s has empty namespace in Install section", component.Name)
		}

		info, exists := namespacesMap[namespace]
		if !exists {
			info = namespaceInfo{
				privileged: false,
			}
		}

		// If component is privileged, mark namespace as privileged
		if component.Install.Privileged {
			info.privileged = true
		}

		namespacesMap[namespace] = info
	}

	// Create or update all namespaces
	for nsName, info := range namespacesMap {
		namespace := &corev1.Namespace{
			ObjectMeta: metav1.ObjectMeta{
				Name:   nsName,
				Labels: make(map[string]string),
				Annotations: map[string]string{
					"helm.sh/resource-policy": "keep",
				},
			},
		}

		// Add system label only for non-tenant namespaces
		if !strings.HasPrefix(nsName, "tenant-") {
			namespace.Labels["cozystack.io/system"] = "true"
		}

		// Add privileged label if needed
		if info.privileged {
			namespace.Labels["pod-security.kubernetes.io/enforce"] = "privileged"
		}

		if err := r.createOrUpdateNamespace(ctx, namespace); err != nil {
			logger.Error(err, "failed to reconcile namespace", "name", nsName, "privileged", info.privileged)
			return fmt.Errorf("failed to reconcile namespace %s: %w", nsName, err)
		}
		logger.Info("reconciled namespace", "name", nsName, "privileged", info.privileged)
	}

	return nil
}

// createOrUpdateNamespace creates or updates a namespace using server-side apply
func (r *PackageReconciler) createOrUpdateNamespace(ctx context.Context, namespace *corev1.Namespace) error {
	// Ensure TypeMeta is set for server-side apply
	namespace.SetGroupVersionKind(corev1.SchemeGroupVersion.WithKind("Namespace"))
	
	// Use server-side apply with field manager
	// This is atomic and avoids race conditions from Get/Create/Update pattern
	// Labels and annotations will be merged automatically by the server
	// Each label/annotation key is treated as a separate field, so existing ones are preserved
	return r.Patch(ctx, namespace, client.Apply, client.FieldOwner("cozystack-package-controller"))
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
