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
	"errors"
	"fmt"
	"strings"
	"time"

	cozyv1alpha1 "github.com/cozystack/cozystack/api/v1alpha1"
	helmv2 "github.com/fluxcd/helm-controller/api/v2"
	sourcewatcherv1beta1 "github.com/fluxcd/source-watcher/api/v2/v1beta1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"
)

// CozystackBundleReconciler reconciles CozystackBundle resources
type CozystackBundleReconciler struct {
	client.Client
	Scheme *runtime.Scheme
}

// +kubebuilder:rbac:groups=cozystack.io,resources=cozystackbundles,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=cozystack.io,resources=cozystackbundles/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=helm.toolkit.fluxcd.io,resources=helmreleases,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=source.extensions.fluxcd.io,resources=artifactgenerators,verbs=get;list;watch;create;update;patch;delete

// Reconcile is part of the main kubernetes reconciliation loop
func (r *CozystackBundleReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	bundle := &cozyv1alpha1.CozystackBundle{}
	if err := r.Get(ctx, req.NamespacedName, bundle); err != nil {
		if apierrors.IsNotFound(err) {
			// Cleanup orphaned resources
			return r.cleanupOrphanedResources(ctx, req.NamespacedName)
		}
		return ctrl.Result{}, err
	}

	// Resolve dependencies from other bundles
	resolvedPackages, err := r.resolveDependencies(ctx, bundle)
	if err != nil {
		// If dependency bundle is not found, requeue to try again later
		// Check if the error is wrapped IsNotFound
		unwrappedErr := errors.Unwrap(err)
		if unwrappedErr != nil && apierrors.IsNotFound(unwrappedErr) {
			logger.Info("Dependency bundle not found, requeuing", "bundle", bundle.Name, "error", err)
			return ctrl.Result{RequeueAfter: 10 * time.Second}, nil
		}
		logger.Error(err, "failed to resolve dependencies")
		return ctrl.Result{}, err
	}

	// Generate ArtifactGenerators for packages
	if err := r.reconcileArtifactGenerators(ctx, bundle, resolvedPackages); err != nil {
		logger.Error(err, "failed to reconcile ArtifactGenerators")
		return ctrl.Result{}, err
	}

	// Generate HelmReleases for packages
	if err := r.reconcileHelmReleases(ctx, bundle, resolvedPackages); err != nil {
		logger.Error(err, "failed to reconcile HelmReleases")
		return ctrl.Result{}, err
	}

	// Check if we need to run phase2 (install basic charts)
	// Phase2 should run when:
	// 1. Any bundle contains cilium and kubeovn
	// 2. Flux is not ready
	hasCilium, hasKubeovn, err := HasCiliumAndKubeovn(ctx, r.Client)
	if err != nil {
		logger.Error(err, "failed to check bundles for cilium/kubeovn")
	} else if hasCilium && hasKubeovn {
		fluxOK, err := FluxIsOK(ctx, r.Client)
		if err != nil {
			logger.Error(err, "failed to check flux status")
		} else if !fluxOK {
			logger.Info("Bundles contain cilium and kubeovn, and flux is not ready, running phase2")
			if err := InstallBasicCharts(ctx, r.Client); err != nil {
				logger.Error(err, "failed to install basic charts in phase2")
				// Don't return error, just log it - phase2 is best effort
			}
		}
	}

	return ctrl.Result{}, nil
}

// resolveDependencies resolves dependencies from other bundles
func (r *CozystackBundleReconciler) resolveDependencies(ctx context.Context, bundle *cozyv1alpha1.CozystackBundle) ([]cozyv1alpha1.BundleRelease, error) {
	resolved := make([]cozyv1alpha1.BundleRelease, 0, len(bundle.Spec.Packages))
	packageMap := make(map[string]bool)

	// Add all packages from this bundle
	for _, pkg := range bundle.Spec.Packages {
		if !pkg.Disabled {
			resolved = append(resolved, pkg)
			packageMap[pkg.Name] = true
		}
	}

	// Resolve dependencies from other bundles
	for _, dependsOn := range bundle.Spec.DependsOn {
		parts := strings.Split(dependsOn, "/")
		if len(parts) != 2 {
			return nil, fmt.Errorf("invalid dependsOn format: %s (expected bundleName/target)", dependsOn)
		}

		bundleName := parts[0]
		targetName := parts[1]

		// Get the bundle
		depBundle := &cozyv1alpha1.CozystackBundle{}
		if err := r.Get(ctx, types.NamespacedName{Name: bundleName}, depBundle); err != nil {
			// If bundle is not found, return wrapped error so we can check it in Reconcile
			if apierrors.IsNotFound(err) {
				return nil, fmt.Errorf("failed to get bundle %s: %w", bundleName, err)
			}
			return nil, fmt.Errorf("failed to get bundle %s: %w", bundleName, err)
		}

		// Find the target
		var target *cozyv1alpha1.BundleDependencyTarget
		for i := range depBundle.Spec.DependencyTargets {
			if depBundle.Spec.DependencyTargets[i].Name == targetName {
				target = &depBundle.Spec.DependencyTargets[i]
				break
			}
		}

		if target == nil {
			return nil, fmt.Errorf("target %s not found in bundle %s", targetName, bundleName)
		}

		// Add packages from target to all packages in this bundle
		for i := range resolved {
			// Add target packages to dependsOn
			for _, targetPkg := range target.Packages {
				// Check if already in dependsOn
				found := false
				for _, dep := range resolved[i].DependsOn {
					if dep == targetPkg {
						found = true
						break
					}
				}
				if !found {
					resolved[i].DependsOn = append(resolved[i].DependsOn, targetPkg)
				}
			}
		}
	}

	return resolved, nil
}

// reconcileArtifactGenerators generates ArtifactGenerators from bundle packages
func (r *CozystackBundleReconciler) reconcileArtifactGenerators(ctx context.Context, bundle *cozyv1alpha1.CozystackBundle, packages []cozyv1alpha1.BundleRelease) error {
	logger := log.FromContext(ctx)

	// Group packages by prefix (system, apps, extra)
	packageGroups := make(map[string][]cozyv1alpha1.BundleRelease)
	libraryMap := make(map[string]cozyv1alpha1.BundleLibrary)
	for _, lib := range bundle.Spec.Libraries {
		libraryMap[lib.Name] = lib
	}

	for _, pkg := range packages {
		// Determine prefix from path
		prefix := r.getPackagePrefix(pkg.Path)
		if prefix == "" {
			logger.Info("skipping package with unknown prefix", "name", pkg.Name, "path", pkg.Path)
			continue
		}

		packageGroups[prefix] = append(packageGroups[prefix], pkg)
	}

	// Create ArtifactGenerator for each group
	for prefix, pkgs := range packageGroups {
		namespace := r.getNamespaceForPrefix(prefix)
		agName := fmt.Sprintf("%s-%s", bundle.Name, prefix)

		// Build output artifacts
		outputArtifacts := []sourcewatcherv1beta1.OutputArtifact{}
		for _, pkg := range pkgs {
			// Extract package name from path (last component)
			pkgName := r.getPackageNameFromPath(pkg.Path)
			if pkgName == "" {
				logger.Info("skipping package with invalid path", "name", pkg.Name, "path", pkg.Path)
				continue
			}

			copyOps := []sourcewatcherv1beta1.CopyOperation{
				{
					From: fmt.Sprintf("@%s/%s/**", bundle.Spec.SourceRef.Name, pkg.Path),
					To:   fmt.Sprintf("@artifact/%s/", pkgName),
				},
			}

			// Add libraries if specified
			for _, libName := range pkg.Libraries {
				if lib, ok := libraryMap[libName]; ok {
					copyOps = append(copyOps, sourcewatcherv1beta1.CopyOperation{
						From: fmt.Sprintf("@%s/%s/**", bundle.Spec.SourceRef.Name, lib.Path),
						To:   fmt.Sprintf("@artifact/%s/charts/%s/", pkgName, libName),
					})
				}
			}

			// Add valuesFiles if specified
			for i, valuesFile := range pkg.ValuesFiles {
				strategy := "Merge"
				if i == 0 {
					strategy = "Overwrite"
				}
				copyOps = append(copyOps, sourcewatcherv1beta1.CopyOperation{
					From:     fmt.Sprintf("@%s/%s/%s", bundle.Spec.SourceRef.Name, pkg.Path, valuesFile),
					To:       fmt.Sprintf("@artifact/%s/values.yaml", pkgName),
					Strategy: strategy,
				})
			}

			artifactName := fmt.Sprintf("%s-%s", prefix, pkgName)
			outputArtifacts = append(outputArtifacts, sourcewatcherv1beta1.OutputArtifact{
				Name: artifactName,
				Copy: copyOps,
			})
		}

		// Create ArtifactGenerator
		ag := &sourcewatcherv1beta1.ArtifactGenerator{
			ObjectMeta: metav1.ObjectMeta{
				Name:      agName,
				Namespace: namespace,
				OwnerReferences: []metav1.OwnerReference{
					{
						APIVersion: bundle.APIVersion,
						Kind:       bundle.Kind,
						Name:       bundle.Name,
						UID:        bundle.UID,
						Controller: func() *bool { b := true; return &b }(),
					},
				},
			},
			Spec: sourcewatcherv1beta1.ArtifactGeneratorSpec{
				Sources: []sourcewatcherv1beta1.SourceReference{
					{
						Alias:     bundle.Spec.SourceRef.Name,
						Kind:      bundle.Spec.SourceRef.Kind,
						Name:      bundle.Spec.SourceRef.Name,
						Namespace: bundle.Spec.SourceRef.Namespace,
					},
				},
				OutputArtifacts: outputArtifacts,
			},
		}

		if err := r.createOrUpdate(ctx, ag); err != nil {
			return fmt.Errorf("failed to reconcile ArtifactGenerator %s: %w", agName, err)
		}
		logger.Info("reconciled ArtifactGenerator", "name", agName, "namespace", namespace)
	}

	// Cleanup orphaned ArtifactGenerators
	return r.cleanupOrphanedArtifactGenerators(ctx, bundle)
}

// reconcileHelmReleases generates HelmReleases from bundle packages
func (r *CozystackBundleReconciler) reconcileHelmReleases(ctx context.Context, bundle *cozyv1alpha1.CozystackBundle, packages []cozyv1alpha1.BundleRelease) error {
	logger := log.FromContext(ctx)

	// Build package name map for dependency resolution
	packageNameMap := make(map[string]cozyv1alpha1.BundleRelease)
	for _, pkg := range packages {
		packageNameMap[pkg.Name] = pkg
	}

	// Create HelmRelease for each package
	for _, pkg := range packages {
		// Determine artifact name from path
		prefix := r.getPackagePrefix(pkg.Path)
		pkgName := r.getPackageNameFromPath(pkg.Path)
		if prefix == "" || pkgName == "" {
			logger.Info("skipping package with invalid path", "name", pkg.Name, "path", pkg.Path)
			continue
		}

		artifactName := fmt.Sprintf("%s-%s", prefix, pkgName)
		artifactNamespace := r.getNamespaceForPrefix(prefix)

		// Create HelmRelease
		hr := &helmv2.HelmRelease{
			ObjectMeta: metav1.ObjectMeta{
				Name:      pkg.Name,
				Namespace: pkg.Namespace,
				OwnerReferences: []metav1.OwnerReference{
					{
						APIVersion: bundle.APIVersion,
						Kind:       bundle.Kind,
						Name:       bundle.Name,
						UID:        bundle.UID,
						Controller: func() *bool { b := true; return &b }(),
					},
				},
			},
			Spec: helmv2.HelmReleaseSpec{
				Interval:    metav1.Duration{Duration: 5 * 60 * 1000000000}, // 5m
				ReleaseName: pkg.ReleaseName,
				ChartRef: &helmv2.CrossNamespaceSourceReference{
					Kind:      "ExternalArtifact",
					Name:      artifactName,
					Namespace: artifactNamespace,
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

		// Set values if provided
		if pkg.Values != nil {
			hr.Spec.Values = pkg.Values
		}

		// Set DependsOn
		if len(pkg.DependsOn) > 0 {
			dependsOn := make([]helmv2.DependencyReference, 0, len(pkg.DependsOn))
			for _, depName := range pkg.DependsOn {
				depPkg, ok := packageNameMap[depName]
				if !ok {
					logger.Info("dependent package not found, using same namespace", "name", pkg.Name, "dependsOn", depName)
					dependsOn = append(dependsOn, helmv2.DependencyReference{
						Name:      depName,
						Namespace: pkg.Namespace,
					})
				} else {
					dependsOn = append(dependsOn, helmv2.DependencyReference{
						Name:      depPkg.Name,
						Namespace: depPkg.Namespace,
					})
				}
			}
			hr.Spec.DependsOn = dependsOn
		}

		// Set valuesFiles annotation
		if len(pkg.ValuesFiles) > 0 {
			if hr.Annotations == nil {
				hr.Annotations = make(map[string]string)
			}
			hr.Annotations["cozypkg.cozystack.io/values-files"] = strings.Join(pkg.ValuesFiles, ",")
		}

		if err := r.createOrUpdate(ctx, hr); err != nil {
			return fmt.Errorf("failed to reconcile HelmRelease %s: %w", pkg.Name, err)
		}
		logger.Info("reconciled HelmRelease", "name", pkg.Name, "namespace", pkg.Namespace)
	}

	// Cleanup orphaned HelmReleases
	return r.cleanupOrphanedHelmReleases(ctx, bundle)
}

// createOrUpdate creates or updates a resource
func (r *CozystackBundleReconciler) createOrUpdate(ctx context.Context, obj client.Object) error {
	existing := obj.DeepCopyObject().(client.Object)
	key := client.ObjectKeyFromObject(obj)

	err := r.Get(ctx, key, existing)
	if apierrors.IsNotFound(err) {
		return r.Create(ctx, obj)
	} else if err != nil {
		return err
	}

	// Preserve resource version
	obj.SetResourceVersion(existing.GetResourceVersion())
	// Merge labels and annotations
	labels := obj.GetLabels()
	if labels == nil {
		labels = make(map[string]string)
	}
	for k, v := range existing.GetLabels() {
		if _, ok := labels[k]; !ok {
			labels[k] = v
		}
	}
	obj.SetLabels(labels)

	annotations := obj.GetAnnotations()
	if annotations == nil {
		annotations = make(map[string]string)
	}
	for k, v := range existing.GetAnnotations() {
		if _, ok := annotations[k]; !ok {
			annotations[k] = v
		}
	}
	obj.SetAnnotations(annotations)

	return r.Update(ctx, obj)
}

// Helper functions
func (r *CozystackBundleReconciler) getPackagePrefix(path string) string {
	if strings.HasPrefix(path, "packages/system/") {
		return "system"
	}
	if strings.HasPrefix(path, "packages/apps/") {
		return "apps"
	}
	if strings.HasPrefix(path, "packages/extra/") {
		return "extra"
	}
	return ""
}

func (r *CozystackBundleReconciler) getNamespaceForPrefix(prefix string) string {
	if prefix == "system" {
		return "cozy-system"
	}
	return "cozy-public"
}

func (r *CozystackBundleReconciler) getPackageNameFromPath(path string) string {
	parts := strings.Split(path, "/")
	if len(parts) > 0 {
		return parts[len(parts)-1]
	}
	return ""
}

func (r *CozystackBundleReconciler) cleanupOrphanedArtifactGenerators(ctx context.Context, bundle *cozyv1alpha1.CozystackBundle) error {
	logger := log.FromContext(ctx)

	agList := &sourcewatcherv1beta1.ArtifactGeneratorList{}
	if err := r.List(ctx, agList); err != nil {
		if apierrors.IsNotFound(err) {
			return nil
		}
		return err
	}

	// Build desired names
	desiredNames := make(map[string]bool)
	for prefix := range map[string]bool{"system": true, "apps": true, "extra": true} {
		desiredNames[fmt.Sprintf("%s-%s", bundle.Name, prefix)] = true
	}

	// Find ArtifactGenerators owned by this bundle
	for _, ag := range agList.Items {
		isOwned := false
		for _, ownerRef := range ag.OwnerReferences {
			if ownerRef.Kind == "CozystackBundle" && ownerRef.Name == bundle.Name && ownerRef.UID == bundle.UID {
				isOwned = true
				break
			}
		}

		if isOwned && !desiredNames[ag.Name] {
			if err := r.Delete(ctx, &ag); err != nil {
				if !apierrors.IsNotFound(err) {
					logger.Error(err, "failed to delete ArtifactGenerator", "name", ag.Name)
				}
			} else {
				logger.Info("deleted orphaned ArtifactGenerator", "name", ag.Name)
			}
		}
	}

	return nil
}

func (r *CozystackBundleReconciler) cleanupOrphanedHelmReleases(ctx context.Context, bundle *cozyv1alpha1.CozystackBundle) error {
	logger := log.FromContext(ctx)

	hrList := &helmv2.HelmReleaseList{}
	if err := r.List(ctx, hrList); err != nil {
		return err
	}

	// Build desired names
	desiredNames := make(map[types.NamespacedName]bool)
	for _, pkg := range bundle.Spec.Packages {
		desiredNames[types.NamespacedName{Name: pkg.Name, Namespace: pkg.Namespace}] = true
	}

	// Find HelmReleases owned by this bundle
	for _, hr := range hrList.Items {
		isOwned := false
		for _, ownerRef := range hr.OwnerReferences {
			if ownerRef.Kind == "CozystackBundle" && ownerRef.Name == bundle.Name && ownerRef.UID == bundle.UID {
				isOwned = true
				break
			}
		}

		if isOwned {
			key := types.NamespacedName{Name: hr.Name, Namespace: hr.Namespace}
			if !desiredNames[key] {
				if err := r.Delete(ctx, &hr); err != nil {
					if !apierrors.IsNotFound(err) {
						logger.Error(err, "failed to delete HelmRelease", "name", hr.Name, "namespace", hr.Namespace)
					}
				} else {
					logger.Info("deleted orphaned HelmRelease", "name", hr.Name, "namespace", hr.Namespace)
				}
			}
		}
	}

	return nil
}

func (r *CozystackBundleReconciler) cleanupOrphanedResources(ctx context.Context, bundleKey types.NamespacedName) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	// Cleanup ArtifactGenerators
	agList := &sourcewatcherv1beta1.ArtifactGeneratorList{}
	if err := r.List(ctx, agList); err == nil {
		for _, ag := range agList.Items {
			for _, ownerRef := range ag.OwnerReferences {
				if ownerRef.Kind == "CozystackBundle" && ownerRef.Name == bundleKey.Name {
					if err := r.Delete(ctx, &ag); err != nil && !apierrors.IsNotFound(err) {
						logger.Error(err, "failed to delete orphaned ArtifactGenerator", "name", ag.Name)
					}
				}
			}
		}
	}

	// Cleanup HelmReleases
	hrList := &helmv2.HelmReleaseList{}
	if err := r.List(ctx, hrList); err == nil {
		for _, hr := range hrList.Items {
			for _, ownerRef := range hr.OwnerReferences {
				if ownerRef.Kind == "CozystackBundle" && ownerRef.Name == bundleKey.Name {
					if err := r.Delete(ctx, &hr); err != nil && !apierrors.IsNotFound(err) {
						logger.Error(err, "failed to delete orphaned HelmRelease", "name", hr.Name, "namespace", hr.Namespace)
					}
				}
			}
		}
	}

	return ctrl.Result{}, nil
}

// SetupWithManager sets up the controller with the Manager.
func (r *CozystackBundleReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		Named("cozystack-bundle").
		For(&cozyv1alpha1.CozystackBundle{}).
		Complete(r)
}

