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
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	cozyv1alpha1 "github.com/cozystack/cozystack/api/v1alpha1"
	helmv2 "github.com/fluxcd/helm-controller/api/v2"
	sourcewatcherv1beta1 "github.com/fluxcd/source-watcher/api/v2/v1beta1"
	corev1 "k8s.io/api/core/v1"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

// BundleReconciler reconciles Bundle resources
type BundleReconciler struct {
	client.Client
	Scheme *runtime.Scheme
}

// +kubebuilder:rbac:groups=cozystack.io,resources=bundles,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=cozystack.io,resources=bundles/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=helm.toolkit.fluxcd.io,resources=helmreleases,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=source.extensions.fluxcd.io,resources=artifactgenerators,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=core,resources=namespaces,verbs=get;list;watch;create;update;patch

// Reconcile is part of the main kubernetes reconciliation loop
func (r *BundleReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	bundle := &cozyv1alpha1.Bundle{}
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

	// Reconcile namespaces from packages
	if err := r.reconcileNamespaces(ctx, bundle, resolvedPackages); err != nil {
		logger.Error(err, "failed to reconcile namespaces")
		return ctrl.Result{}, err
	}

	// Check for conflicts between packages with artifact and artifacts
	if err := r.checkArtifactConflicts(ctx, bundle, resolvedPackages); err != nil {
		logger.Error(err, "failed to check artifact conflicts")
		return ctrl.Result{}, err
	}

	// Generate ArtifactGenerator for bundle (one generator per bundle with all OutputArtifacts)
	if err := r.reconcileArtifactGenerators(ctx, bundle, resolvedPackages); err != nil {
		logger.Error(err, "failed to reconcile ArtifactGenerator")
		return ctrl.Result{}, err
	}

	// Generate HelmReleases for packages
	if err := r.reconcileHelmReleases(ctx, bundle, resolvedPackages); err != nil {
		logger.Error(err, "failed to reconcile HelmReleases")
		return ctrl.Result{}, err
	}

	return ctrl.Result{}, nil
}

// resolveDependencies resolves dependencies from other bundles
func (r *BundleReconciler) resolveDependencies(ctx context.Context, bundle *cozyv1alpha1.Bundle) ([]cozyv1alpha1.BundleRelease, error) {
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
		depBundle := &cozyv1alpha1.Bundle{}
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

// reconcileArtifactGenerators generates a single ArtifactGenerator for the bundle
// Creates one ArtifactGenerator per bundle with all OutputArtifacts from packages and artifacts
func (r *BundleReconciler) reconcileArtifactGenerators(ctx context.Context, bundle *cozyv1alpha1.Bundle, packages []cozyv1alpha1.BundleRelease) error {
	logger := log.FromContext(ctx)

	libraryMap := make(map[string]cozyv1alpha1.BundleLibrary)
	for _, lib := range bundle.Spec.Libraries {
		libraryMap[lib.Name] = lib
	}

	// Namespace is always cozy-system
	namespace := "cozy-system"
	// ArtifactGenerator name is the bundle name
	agName := bundle.Name

	// Collect all OutputArtifacts
	outputArtifacts := []sourcewatcherv1beta1.OutputArtifact{}

	// Process packages
	for _, pkg := range packages {
		logger.V(1).Info("processing package for artifact", "bundle", bundle.Name, "package", pkg.Name, "path", pkg.Path, "disabled", pkg.Disabled)

		// Skip packages without path (they might use artifacts)
		if pkg.Path == "" {
			logger.V(1).Info("skipping package without path", "name", pkg.Name)
			continue
		}

		// Extract package name from path (last component)
		pkgName := r.getPackageNameFromPath(pkg.Path)
		if pkgName == "" {
			logger.Info("skipping package with invalid path", "name", pkg.Name, "path", pkg.Path)
			continue
		}

		logger.V(1).Info("extracted package name from path", "name", pkg.Name, "path", pkg.Path, "pkgName", pkgName)

		// Get basePath with default values
		basePath := r.getBasePath(bundle)

		// Build copy operations
		copyOps := []sourcewatcherv1beta1.CopyOperation{
			{
				From: r.buildSourcePath(bundle.Spec.SourceRef.Name, basePath, pkg.Path),
				To:   fmt.Sprintf("@artifact/%s/", pkgName),
			},
		}

		// Add libraries if specified
		for _, libName := range pkg.Libraries {
			if lib, ok := libraryMap[libName]; ok {
				copyOps = append(copyOps, sourcewatcherv1beta1.CopyOperation{
					From: r.buildSourcePath(bundle.Spec.SourceRef.Name, basePath, lib.Path),
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
				From:     r.buildSourceFilePath(bundle.Spec.SourceRef.Name, basePath, fmt.Sprintf("%s/%s", pkg.Path, valuesFile)),
				To:       fmt.Sprintf("@artifact/%s/values.yaml", pkgName),
				Strategy: strategy,
			})
		}

		// Artifact name: bundle-name-package-name (e.g., cozystack-system-cilium)
		artifactName := fmt.Sprintf("%s-%s", bundle.Name, pkgName)

		outputArtifacts = append(outputArtifacts, sourcewatcherv1beta1.OutputArtifact{
			Name: artifactName,
			Copy: copyOps,
		})

		logger.Info("added OutputArtifact for package", "bundle", bundle.Name, "package", pkg.Name, "artifactName", artifactName)
	}

	// Process artifacts
	for _, artifact := range bundle.Spec.Artifacts {
		logger.Info("processing artifact", "bundle", bundle.Name, "artifact", artifact.Name, "path", artifact.Path)
		// Extract artifact name from path (last component)
		artifactPathName := r.getPackageNameFromPath(artifact.Path)
		if artifactPathName == "" {
			logger.Info("skipping artifact with invalid path", "name", artifact.Name, "path", artifact.Path)
			continue
		}

		// Get basePath with default values
		basePath := r.getBasePath(bundle)

		// Build copy operations
		copyOps := []sourcewatcherv1beta1.CopyOperation{
			{
				From: r.buildSourcePath(bundle.Spec.SourceRef.Name, basePath, artifact.Path),
				To:   fmt.Sprintf("@artifact/%s/", artifactPathName),
			},
		}

		// Add libraries if specified
		for _, libName := range artifact.Libraries {
			if lib, ok := libraryMap[libName]; ok {
				copyOps = append(copyOps, sourcewatcherv1beta1.CopyOperation{
					From: r.buildSourcePath(bundle.Spec.SourceRef.Name, basePath, lib.Path),
					To:   fmt.Sprintf("@artifact/%s/charts/%s/", artifactPathName, libName),
				})
			}
		}

		// Artifact name: {bundle-name}-{artifact-name}
		artifactName := fmt.Sprintf("%s-%s", bundle.Name, artifact.Name)

		outputArtifacts = append(outputArtifacts, sourcewatcherv1beta1.OutputArtifact{
			Name: artifactName,
			Copy: copyOps,
		})

		logger.Info("added OutputArtifact for artifact", "bundle", bundle.Name, "artifact", artifact.Name, "artifactName", artifactName)
	}

	// If there are no OutputArtifacts, cleanup and return
	if len(outputArtifacts) == 0 {
		logger.Info("no OutputArtifacts to generate, skipping ArtifactGenerator creation", "bundle", bundle.Name)
		// Cleanup orphaned ArtifactGenerators (to remove existing generator if it exists)
		return r.cleanupOrphanedArtifactGenerators(ctx, bundle)
	}

	// Build labels: merge bundle labels with default cozystack.io/bundle label
	labels := make(map[string]string)
	if bundle.Spec.Labels != nil {
		for k, v := range bundle.Spec.Labels {
			labels[k] = v
		}
	}
	labels["cozystack.io/bundle"] = bundle.Name

	// Create single ArtifactGenerator for the bundle
	ag := &sourcewatcherv1beta1.ArtifactGenerator{
		ObjectMeta: metav1.ObjectMeta{
			Name:      agName,
			Namespace: namespace,
			Labels:    labels,
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

	// Set ownerReference only if deletionPolicy is not Orphan
	if bundle.Spec.DeletionPolicy != cozyv1alpha1.DeletionPolicyOrphan {
		ag.OwnerReferences = []metav1.OwnerReference{
			{
				APIVersion: bundle.APIVersion,
				Kind:       bundle.Kind,
				Name:       bundle.Name,
				UID:        bundle.UID,
				Controller: func() *bool { b := true; return &b }(),
			},
		}
	} else {
		// Explicitly set empty ownerReferences for Orphan policy
		ag.OwnerReferences = []metav1.OwnerReference{}
	}

	logger.Info("creating ArtifactGenerator for bundle", "bundle", bundle.Name, "agName", agName, "namespace", namespace, "outputArtifactCount", len(outputArtifacts))

	if err := r.createOrUpdate(ctx, ag); err != nil {
		return fmt.Errorf("failed to reconcile ArtifactGenerator %s: %w", agName, err)
	}

	logger.Info("reconciled ArtifactGenerator for bundle", "name", agName, "namespace", namespace, "outputArtifactCount", len(outputArtifacts))

	// Cleanup orphaned ArtifactGenerators
	return r.cleanupOrphanedArtifactGenerators(ctx, bundle)
}

// reconcileHelmReleases generates HelmReleases from bundle packages
func (r *BundleReconciler) reconcileHelmReleases(ctx context.Context, bundle *cozyv1alpha1.Bundle, packages []cozyv1alpha1.BundleRelease) error {
	logger := log.FromContext(ctx)

	// Build package name map for dependency resolution (from current bundle)
	packageNameMap := make(map[string]cozyv1alpha1.BundleRelease)
	for _, pkg := range packages {
		packageNameMap[pkg.Name] = pkg
	}

	// Build global package name map from all bundles for finding dependencies
	globalPackageMap := make(map[string]cozyv1alpha1.BundleRelease)
	bundleList := &cozyv1alpha1.BundleList{}
	if err := r.List(ctx, bundleList); err == nil {
		for _, b := range bundleList.Items {
			for _, pkg := range b.Spec.Packages {
				// Only add if not already in map (first occurrence wins, or use current bundle's packages)
				if _, exists := globalPackageMap[pkg.Name]; !exists {
					globalPackageMap[pkg.Name] = pkg
				}
			}
		}
	}
	// Override with packages from current bundle (they take precedence)
	for _, pkg := range packages {
		globalPackageMap[pkg.Name] = pkg
	}

	// Build artifact name map from bundle artifacts for conflict checking
	artifactNameMap := make(map[string]bool)
	for _, artifact := range bundle.Spec.Artifacts {
		artifactNameMap[artifact.Name] = true
	}

	// Create HelmRelease for each package
	for _, pkg := range packages {
		// Skip disabled packages
		if pkg.Disabled {
			logger.V(1).Info("skipping disabled package", "name", pkg.Name, "namespace", pkg.Namespace)
			continue
		}

		var artifactName string
		artifactNamespace := "cozy-system"

		if pkg.Artifact != "" {
			// Package uses an artifact reference
			// Check if artifact exists in bundle
			if !artifactNameMap[pkg.Artifact] {
				logger.Error(fmt.Errorf("artifact %s not found in bundle artifacts", pkg.Artifact), "skipping package", "name", pkg.Name)
				continue
			}
			// Artifact name format: {bundle-name}-{artifact-name}
			artifactName = fmt.Sprintf("%s-%s", bundle.Name, pkg.Artifact)
		} else if pkg.Path != "" {
			// Package uses a path
			pkgName := r.getPackageNameFromPath(pkg.Path)
			if pkgName == "" {
				logger.Info("skipping package with invalid path", "name", pkg.Name, "path", pkg.Path)
				continue
			}
			// Artifact name format: {bundle-name}-{package-name}
			artifactName = fmt.Sprintf("%s-%s", bundle.Name, pkgName)
		} else {
			logger.Error(fmt.Errorf("neither artifact nor path specified"), "skipping package", "name", pkg.Name)
			continue
		}

		// Build labels: merge bundle labels, package labels, and default cozystack.io/bundle label
		hrLabels := make(map[string]string)
		// First, add bundle-level labels
		if bundle.Spec.Labels != nil {
			for k, v := range bundle.Spec.Labels {
				hrLabels[k] = v
			}
		}
		// Then, add package-level labels (they override bundle labels)
		if pkg.Labels != nil {
			for k, v := range pkg.Labels {
				hrLabels[k] = v
			}
		}
		// Finally, add default bundle label (it always takes precedence)
		hrLabels["cozystack.io/bundle"] = bundle.Name

		// Add system-app label if namespace starts with "cozy-"
		if pkg.Namespace == "kube-system" || strings.HasPrefix(pkg.Namespace, "cozy-") {
			hrLabels["cozystack.io/system-app"] = "true"
		}

		// Create HelmRelease
		hr := &helmv2.HelmRelease{
			ObjectMeta: metav1.ObjectMeta{
				Name:      pkg.Name,
				Namespace: pkg.Namespace,
				Labels:    hrLabels,
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

		// Set ownerReference only if deletionPolicy is not Orphan
		if bundle.Spec.DeletionPolicy != cozyv1alpha1.DeletionPolicyOrphan {
			hr.OwnerReferences = []metav1.OwnerReference{
				{
					APIVersion: bundle.APIVersion,
					Kind:       bundle.Kind,
					Name:       bundle.Name,
					UID:        bundle.UID,
					Controller: func() *bool { b := true; return &b }(),
				},
			}
		} else {
			// Explicitly set empty ownerReferences for Orphan policy
			hr.OwnerReferences = []metav1.OwnerReference{}
		}

		// Set values if provided
		if pkg.Values != nil {
			hr.Spec.Values = pkg.Values
		}

		// Add system-app label if TargetNamespace starts with "cozy-"
		if hr.Spec.TargetNamespace != "" && (hr.Spec.TargetNamespace == "kube-system" || strings.HasPrefix(hr.Spec.TargetNamespace, "cozy-")) {
			hr.Labels["cozystack.io/system-app"] = "true"
		}

		// Set DependsOn
		if len(pkg.DependsOn) > 0 {
			dependsOn := make([]helmv2.DependencyReference, 0, len(pkg.DependsOn))
			for _, depName := range pkg.DependsOn {
				depPkg, ok := globalPackageMap[depName]
				if !ok {
					logger.Info("dependent package not found in any bundle, using same namespace", "name", pkg.Name, "dependsOn", depName)
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
func (r *BundleReconciler) createOrUpdate(ctx context.Context, obj client.Object) error {
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

	// Update ownerReferences: always use the ones from obj
	// This allows deletionPolicy = Orphan to work by setting empty ownerReferences array
	// When ownerReferences field is set in obj (even as empty array), use it
	// Empty array will clear ownerReferences (for deletionPolicy = Orphan or when policy changes)
	// If ownerReferences field is not set in obj (nil), preserve existing ones
	objOwnerRefs := obj.GetOwnerReferences()
	if objOwnerRefs != nil {
		// obj has ownerReferences set (either populated or empty array), use them
		// Empty array (len == 0) means we want to remove all ownerReferences (deletionPolicy = Orphan)
		// This handles policy changes from Delete to Orphan
		// objOwnerRefs is already set in obj, so it will be used in Update
		// No need to do anything else - Update will use the ownerReferences from obj
	} else if len(existing.GetOwnerReferences()) > 0 {
		// obj doesn't have ownerReferences set (nil), but existing does
		// Preserve existing ones (they might be from other owners)
		obj.SetOwnerReferences(existing.GetOwnerReferences())
	}

	// For ArtifactGenerator, explicitly update Spec (OutputArtifacts and Sources)
	// This ensures that OutputArtifacts from both packages and artifacts are properly updated
	if ag, ok := obj.(*sourcewatcherv1beta1.ArtifactGenerator); ok {
		if existingAG, ok := existing.(*sourcewatcherv1beta1.ArtifactGenerator); ok {
			logger := log.FromContext(ctx)
			logger.V(1).Info("updating ArtifactGenerator Spec", "name", ag.Name, "namespace", ag.Namespace,
				"outputArtifactCount", len(ag.Spec.OutputArtifacts))
			// Update Spec from obj (which contains the desired state with all OutputArtifacts)
			existingAG.Spec = ag.Spec
			// Preserve metadata updates we made above
			existingAG.SetLabels(ag.GetLabels())
			existingAG.SetAnnotations(ag.GetAnnotations())
			existingAG.SetOwnerReferences(ag.GetOwnerReferences())
			// Use existingAG for Update
			obj = existingAG
		}
	}

	// For HelmRelease, explicitly update Spec to ensure values and dependsOn are properly updated
	if hr, ok := obj.(*helmv2.HelmRelease); ok {
		if existingHR, ok := existing.(*helmv2.HelmRelease); ok {
			logger := log.FromContext(ctx)
			logger.V(1).Info("updating HelmRelease Spec", "name", hr.Name, "namespace", hr.Namespace)

			// Check if this HelmRelease is managed through Application API or Controller
			// If it has apps.cozystack.io/application.* labels OR cozystack.io/ui=true label, merge values with bundle priority
			isApplicationManaged := existingHR.Labels["apps.cozystack.io/application.kind"] != "" &&
				existingHR.Labels["apps.cozystack.io/application.group"] != ""
			isControllerManaged := existingHR.Labels["cozystack.io/ui"] == "true"

			if isApplicationManaged || isControllerManaged {
				// For Application/Controller-managed HelmReleases, merge values with bundle priority
				logger.V(1).Info("merging values for Application/Controller-managed HelmRelease with bundle priority", "name", hr.Name, "namespace", hr.Namespace, "isApplicationManaged", isApplicationManaged, "isControllerManaged", isControllerManaged)
				existingHR.Spec.Chart = hr.Spec.Chart
				existingHR.Spec.ChartRef = hr.Spec.ChartRef
				existingHR.Spec.Interval = hr.Spec.Interval
				existingHR.Spec.Timeout = hr.Spec.Timeout
				existingHR.Spec.ReleaseName = hr.Spec.ReleaseName
				existingHR.Spec.DependsOn = hr.Spec.DependsOn
				existingHR.Spec.Install = hr.Spec.Install
				existingHR.Spec.Upgrade = hr.Spec.Upgrade
				existingHR.Spec.Uninstall = hr.Spec.Uninstall
				existingHR.Spec.Rollback = hr.Spec.Rollback
				existingHR.Spec.StorageNamespace = hr.Spec.StorageNamespace
				existingHR.Spec.KubeConfig = hr.Spec.KubeConfig
				existingHR.Spec.TargetNamespace = hr.Spec.TargetNamespace
				existingHR.Spec.PostRenderers = hr.Spec.PostRenderers
				existingHR.Spec.ServiceAccountName = hr.Spec.ServiceAccountName
				existingHR.Spec.Suspend = hr.Spec.Suspend

				// Merge values: bundle values have priority (override existing)
				mergedValues, err := mergeHelmReleaseValuesWithBundlePriority(existingHR.Spec.Values, hr.Spec.Values)
				if err != nil {
					logger.Error(err, "failed to merge values, using bundle values", "name", hr.Name, "namespace", hr.Namespace)
					existingHR.Spec.Values = hr.Spec.Values
				} else {
					existingHR.Spec.Values = mergedValues
				}
			} else {
				// For bundle-managed HelmReleases, update everything including values
				existingHR.Spec = hr.Spec
			}

			// Preserve metadata updates we made above
			existingHR.SetLabels(hr.GetLabels())
			existingHR.SetAnnotations(hr.GetAnnotations())
			existingHR.SetOwnerReferences(hr.GetOwnerReferences())
			// Use existingHR for Update
			obj = existingHR
		}
	}

	return r.Update(ctx, obj)
}

// mergeHelmReleaseValues merges two HelmRelease values JSON objects
// Existing values have priority (bundle values are merged into existing)
func mergeHelmReleaseValues(existingValues, bundleValues *apiextensionsv1.JSON) (*apiextensionsv1.JSON, error) {
	// If bundle has no values, preserve existing
	if bundleValues == nil || len(bundleValues.Raw) == 0 {
		return existingValues, nil
	}

	// If existing has no values, use bundle values
	if existingValues == nil || len(existingValues.Raw) == 0 {
		return bundleValues, nil
	}

	// Parse both values
	var existingMap map[string]interface{}
	if err := json.Unmarshal(existingValues.Raw, &existingMap); err != nil {
		return nil, fmt.Errorf("failed to unmarshal existing values: %w", err)
	}

	var bundleMap map[string]interface{}
	if err := json.Unmarshal(bundleValues.Raw, &bundleMap); err != nil {
		return nil, fmt.Errorf("failed to unmarshal bundle values: %w", err)
	}

	// Merge: existing values have priority (bundle is merged into existing)
	mergedMap := deepMergeMaps(bundleMap, existingMap)

	// Marshal back to JSON
	mergedJSON, err := json.Marshal(mergedMap)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal merged values: %w", err)
	}

	return &apiextensionsv1.JSON{Raw: mergedJSON}, nil
}

// mergeHelmReleaseValuesWithBundlePriority merges two HelmRelease values JSON objects
// Bundle values have priority (override existing values)
// All fields from bundle override existing, except nested merges for maps
func mergeHelmReleaseValuesWithBundlePriority(existingValues, bundleValues *apiextensionsv1.JSON) (*apiextensionsv1.JSON, error) {
	// If bundle has no values, preserve existing
	if bundleValues == nil || len(bundleValues.Raw) == 0 {
		return existingValues, nil
	}

	// If existing has no values, use bundle values
	if existingValues == nil || len(existingValues.Raw) == 0 {
		return bundleValues, nil
	}

	// Parse both values
	var existingMap map[string]interface{}
	if err := json.Unmarshal(existingValues.Raw, &existingMap); err != nil {
		return nil, fmt.Errorf("failed to unmarshal existing values: %w", err)
	}

	var bundleMap map[string]interface{}
	if err := json.Unmarshal(bundleValues.Raw, &bundleMap); err != nil {
		return nil, fmt.Errorf("failed to unmarshal bundle values: %w", err)
	}

	// Merge: start with existing values, then bundle values override (bundle has priority)
	mergedMap := deepMergeMaps(existingMap, bundleMap)

	// Marshal back to JSON
	mergedJSON, err := json.Marshal(mergedMap)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal merged values: %w", err)
	}

	return &apiextensionsv1.JSON{Raw: mergedJSON}, nil
}

// deepMergeMaps performs a deep merge of two maps
// Values from override map take precedence, but nested maps are merged recursively
func deepMergeMaps(base, override map[string]interface{}) map[string]interface{} {
	result := make(map[string]interface{})

	// Copy base map
	for k, v := range base {
		result[k] = v
	}

	// Merge override map
	for k, v := range override {
		if baseVal, exists := result[k]; exists {
			// If both are maps, recursively merge
			if baseMap, ok := baseVal.(map[string]interface{}); ok {
				if overrideMap, ok := v.(map[string]interface{}); ok {
					result[k] = deepMergeMaps(baseMap, overrideMap)
					continue
				}
			}
		}
		// Override takes precedence for non-map values or new keys
		result[k] = v
	}

	return result
}

// Helper functions
func (r *BundleReconciler) getPackageNameFromPath(path string) string {
	parts := strings.Split(path, "/")
	if len(parts) > 0 {
		return parts[len(parts)-1]
	}
	return ""
}

// getBasePath returns the basePath with default values based on source kind
func (r *BundleReconciler) getBasePath(bundle *cozyv1alpha1.Bundle) string {
	// If basePath is explicitly set, use it
	if bundle.Spec.BasePath != "" {
		return bundle.Spec.BasePath
	}
	// Default values based on kind
	if bundle.Spec.SourceRef.Kind == "OCIRepository" {
		return "" // Root for OCI
	}
	// Default for GitRepository
	return "packages"
}

// buildSourcePath builds the full source path using basePath with glob pattern
func (r *BundleReconciler) buildSourcePath(sourceName, basePath, path string) string {
	// Remove leading/trailing slashes and combine
	parts := []string{}
	if basePath != "" {
		parts = append(parts, strings.Trim(basePath, "/"))
	}
	if path != "" {
		parts = append(parts, strings.Trim(path, "/"))
	}

	fullPath := strings.Join(parts, "/")
	if fullPath == "" {
		return fmt.Sprintf("@%s/**", sourceName)
	}
	return fmt.Sprintf("@%s/%s/**", sourceName, fullPath)
}

// buildSourceFilePath builds the full source path for a specific file (without glob pattern)
func (r *BundleReconciler) buildSourceFilePath(sourceName, basePath, path string) string {
	// Remove leading/trailing slashes and combine
	parts := []string{}
	if basePath != "" {
		parts = append(parts, strings.Trim(basePath, "/"))
	}
	if path != "" {
		parts = append(parts, strings.Trim(path, "/"))
	}

	fullPath := strings.Join(parts, "/")
	if fullPath == "" {
		return fmt.Sprintf("@%s", sourceName)
	}
	return fmt.Sprintf("@%s/%s", sourceName, fullPath)
}

func (r *BundleReconciler) cleanupOrphanedArtifactGenerators(ctx context.Context, bundle *cozyv1alpha1.Bundle) error {
	logger := log.FromContext(ctx)

	// Find ArtifactGenerators by label
	agList := &sourcewatcherv1beta1.ArtifactGeneratorList{}
	if err := r.List(ctx, agList, client.MatchingLabels{
		"cozystack.io/bundle": bundle.Name,
	}); err != nil {
		if apierrors.IsNotFound(err) {
			return nil
		}
		return err
	}

	// Desired name: bundle name (one ArtifactGenerator per bundle)
	desiredName := bundle.Name

	// Find ArtifactGenerators with this bundle label
	for _, ag := range agList.Items {
		// Check if it's the desired name
		isDesired := ag.Name == desiredName

		if !isDesired {
			// Delete ArtifactGenerators that don't match the desired name
			// This includes old pattern ArtifactGenerators (for migration from per-package/per-artifact to per-bundle)
			logger.Info("deleting orphaned ArtifactGenerator", "name", ag.Name, "bundle", bundle.Name, "desiredName", desiredName)
			if err := r.Delete(ctx, &ag); err != nil {
				if !apierrors.IsNotFound(err) {
					logger.Error(err, "failed to delete orphaned ArtifactGenerator", "name", ag.Name)
				}
			} else {
				logger.Info("deleted orphaned ArtifactGenerator", "name", ag.Name)
			}
		}
	}

	return nil
}

func (r *BundleReconciler) cleanupOrphanedHelmReleases(ctx context.Context, bundle *cozyv1alpha1.Bundle) error {
	logger := log.FromContext(ctx)

	// Find HelmReleases by label
	hrList := &helmv2.HelmReleaseList{}
	if err := r.List(ctx, hrList, client.MatchingLabels{
		"cozystack.io/bundle": bundle.Name,
	}); err != nil {
		return err
	}

	// Build desired names (excluding disabled packages)
	desiredNames := make(map[types.NamespacedName]bool)
	for _, pkg := range bundle.Spec.Packages {
		// Only include non-disabled packages in desired names
		if !pkg.Disabled {
			desiredNames[types.NamespacedName{Name: pkg.Name, Namespace: pkg.Namespace}] = true
		}
	}

	// Find HelmReleases with this bundle label
	for _, hr := range hrList.Items {
		key := types.NamespacedName{Name: hr.Name, Namespace: hr.Namespace}
		if !desiredNames[key] {
			logger.Info("deleting orphaned HelmRelease", "name", hr.Name, "namespace", hr.Namespace, "bundle", bundle.Name)
			if err := r.Delete(ctx, &hr); err != nil {
				if !apierrors.IsNotFound(err) {
					logger.Error(err, "failed to delete HelmRelease", "name", hr.Name, "namespace", hr.Namespace)
				}
			} else {
				logger.Info("deleted orphaned HelmRelease", "name", hr.Name, "namespace", hr.Namespace)
			}
		}
	}

	return nil
}

func (r *BundleReconciler) cleanupOrphanedResources(ctx context.Context, bundleKey types.NamespacedName) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	// Cleanup ArtifactGenerators by label
	// Only delete if they have ownerReferences to this bundle (deletionPolicy != Orphan)
	agList := &sourcewatcherv1beta1.ArtifactGeneratorList{}
	if err := r.List(ctx, agList, client.MatchingLabels{
		"cozystack.io/bundle": bundleKey.Name,
	}); err == nil {
		for _, ag := range agList.Items {
			// Check if this resource has ownerReference to the deleted bundle
			hasOwnerRef := false
			for _, ownerRef := range ag.OwnerReferences {
				if ownerRef.Kind == "Bundle" && ownerRef.Name == bundleKey.Name {
					hasOwnerRef = true
					break
				}
			}

			// Only delete if it has ownerReference (deletionPolicy != Orphan)
			// If no ownerReference, it means deletionPolicy was Orphan, so we should not delete it
			if hasOwnerRef {
				logger.Info("deleting orphaned ArtifactGenerator", "name", ag.Name, "bundle", bundleKey.Name)
				if err := r.Delete(ctx, &ag); err != nil && !apierrors.IsNotFound(err) {
					logger.Error(err, "failed to delete orphaned ArtifactGenerator", "name", ag.Name)
				}
			} else {
				logger.Info("skipping ArtifactGenerator deletion (deletionPolicy=Orphan)", "name", ag.Name, "bundle", bundleKey.Name)
			}
		}
	}

	// Cleanup HelmReleases by label
	// Only delete if they have ownerReferences to this bundle (deletionPolicy != Orphan)
	hrList := &helmv2.HelmReleaseList{}
	if err := r.List(ctx, hrList, client.MatchingLabels{
		"cozystack.io/bundle": bundleKey.Name,
	}); err == nil {
		for _, hr := range hrList.Items {
			// Check if this resource has ownerReference to the deleted bundle
			hasOwnerRef := false
			for _, ownerRef := range hr.OwnerReferences {
				if ownerRef.Kind == "Bundle" && ownerRef.Name == bundleKey.Name {
					hasOwnerRef = true
					break
				}
			}

			// Only delete if it has ownerReference (deletionPolicy != Orphan)
			// If no ownerReference, it means deletionPolicy was Orphan, so we should not delete it
			if hasOwnerRef {
				logger.Info("deleting orphaned HelmRelease", "name", hr.Name, "namespace", hr.Namespace, "bundle", bundleKey.Name)
				if err := r.Delete(ctx, &hr); err != nil && !apierrors.IsNotFound(err) {
					logger.Error(err, "failed to delete orphaned HelmRelease", "name", hr.Name, "namespace", hr.Namespace)
				}
			} else {
				logger.Info("skipping HelmRelease deletion (deletionPolicy=Orphan)", "name", hr.Name, "namespace", hr.Namespace, "bundle", bundleKey.Name)
			}
		}
	}

	return ctrl.Result{}, nil
}

// reconcileNamespaces creates or updates namespaces based on packages in the bundle.
func (r *BundleReconciler) reconcileNamespaces(ctx context.Context, bundle *cozyv1alpha1.Bundle, packages []cozyv1alpha1.BundleRelease) error {
	logger := log.FromContext(ctx)

	// Collect namespaces from packages
	// Map: namespace -> {isPrivileged, labels}
	type namespaceInfo struct {
		privileged bool
		labels     map[string]string
	}
	namespacesMap := make(map[string]namespaceInfo)

	for _, pkg := range packages {
		// Skip disabled packages
		if pkg.Disabled {
			continue
		}

		// Skip if namespace is empty
		if pkg.Namespace == "" {
			continue
		}

		info, exists := namespacesMap[pkg.Namespace]
		if !exists {
			info = namespaceInfo{
				privileged: false,
				labels:     make(map[string]string),
			}
		}

		// If package is privileged, mark namespace as privileged
		if pkg.Privileged {
			info.privileged = true
		}

		// Merge namespace labels from package
		if pkg.NamespaceLabels != nil {
			for k, v := range pkg.NamespaceLabels {
				info.labels[k] = v
			}
		}

		namespacesMap[pkg.Namespace] = info
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

		// Merge namespace labels from packages
		for k, v := range info.labels {
			namespace.Labels[k] = v
		}

		if err := r.createOrUpdateNamespace(ctx, namespace); err != nil {
			logger.Error(err, "failed to reconcile namespace", "name", nsName, "privileged", info.privileged)
			return fmt.Errorf("failed to reconcile namespace %s: %w", nsName, err)
		}
		logger.Info("reconciled namespace", "name", nsName, "privileged", info.privileged, "labels", info.labels)
	}

	return nil
}

// createOrUpdateNamespace creates or updates a namespace.
func (r *BundleReconciler) createOrUpdateNamespace(ctx context.Context, namespace *corev1.Namespace) error {
	existing := &corev1.Namespace{}
	key := types.NamespacedName{Name: namespace.Name}

	err := r.Get(ctx, key, existing)
	if apierrors.IsNotFound(err) {
		return r.Create(ctx, namespace)
	} else if err != nil {
		return err
	}

	// Preserve resource version
	namespace.SetResourceVersion(existing.GetResourceVersion())

	// Merge labels
	labels := namespace.GetLabels()
	if labels == nil {
		labels = make(map[string]string)
	}
	for k, v := range existing.GetLabels() {
		if _, ok := labels[k]; !ok {
			labels[k] = v
		}
	}
	namespace.SetLabels(labels)

	// Merge annotations
	annotations := namespace.GetAnnotations()
	if annotations == nil {
		annotations = make(map[string]string)
	}
	for k, v := range existing.GetAnnotations() {
		if _, ok := annotations[k]; !ok {
			annotations[k] = v
		}
	}
	namespace.SetAnnotations(annotations)

	return r.Update(ctx, namespace)
}

// checkArtifactConflicts checks for conflicts between packages using artifacts and bundle artifacts
func (r *BundleReconciler) checkArtifactConflicts(ctx context.Context, bundle *cozyv1alpha1.Bundle, packages []cozyv1alpha1.BundleRelease) error {
	// Build artifact name map from bundle artifacts
	artifactNameMap := make(map[string]bool)
	for _, artifact := range bundle.Spec.Artifacts {
		artifactNameMap[artifact.Name] = true
	}

	// Check packages that use artifacts
	for _, pkg := range packages {
		if pkg.Artifact != "" {
			if !artifactNameMap[pkg.Artifact] {
				return fmt.Errorf("package %s references artifact %s which is not defined in bundle artifacts", pkg.Name, pkg.Artifact)
			}
		}
	}

	return nil
}

// removeOwnerReferences removes ownerReferences from all resources with bundle label
func (r *BundleReconciler) removeOwnerReferences(ctx context.Context, bundle *cozyv1alpha1.Bundle) error {
	logger := log.FromContext(ctx)

	// Remove ownerReferences from ArtifactGenerators by label
	agList := &sourcewatcherv1beta1.ArtifactGeneratorList{}
	if err := r.List(ctx, agList, client.MatchingLabels{
		"cozystack.io/bundle": bundle.Name,
	}); err == nil {
		for i := range agList.Items {
			ag := &agList.Items[i]
			updated := false
			newOwnerRefs := []metav1.OwnerReference{}

			for _, ownerRef := range ag.OwnerReferences {
				if ownerRef.Kind == "Bundle" && ownerRef.Name == bundle.Name {
					// Skip this ownerReference (remove it)
					// Check by name only, not UID, to handle bundle updates
					updated = true
				} else {
					// Keep other ownerReferences
					newOwnerRefs = append(newOwnerRefs, ownerRef)
				}
			}

			if updated {
				ag.SetOwnerReferences(newOwnerRefs)
				if err := r.Update(ctx, ag); err != nil {
					logger.Error(err, "failed to remove ownerReference from ArtifactGenerator", "name", ag.Name, "namespace", ag.Namespace)
				} else {
					logger.Info("removed ownerReference from ArtifactGenerator", "name", ag.Name, "namespace", ag.Namespace)
				}
			}
		}
	}

	// Remove ownerReferences from HelmReleases by label
	hrList := &helmv2.HelmReleaseList{}
	if err := r.List(ctx, hrList, client.MatchingLabels{
		"cozystack.io/bundle": bundle.Name,
	}); err == nil {
		for i := range hrList.Items {
			hr := &hrList.Items[i]
			updated := false
			newOwnerRefs := []metav1.OwnerReference{}

			for _, ownerRef := range hr.OwnerReferences {
				if ownerRef.Kind == "Bundle" && ownerRef.Name == bundle.Name {
					// Skip this ownerReference (remove it)
					// Check by name only, not UID, to handle bundle updates
					updated = true
				} else {
					// Keep other ownerReferences
					newOwnerRefs = append(newOwnerRefs, ownerRef)
				}
			}

			if updated {
				hr.SetOwnerReferences(newOwnerRefs)
				if err := r.Update(ctx, hr); err != nil {
					logger.Error(err, "failed to remove ownerReference from HelmRelease", "name", hr.Name, "namespace", hr.Namespace)
				} else {
					logger.Info("removed ownerReference from HelmRelease", "name", hr.Name, "namespace", hr.Namespace)
				}
			}
		}
	}

	return nil
}

// SetupWithManager sets up the controller with the Manager.
func (r *BundleReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		Named("cozystack-bundle").
		For(&cozyv1alpha1.Bundle{}).
		Watches(
			&helmv2.HelmRelease{},
			handler.EnqueueRequestsFromMapFunc(func(ctx context.Context, obj client.Object) []reconcile.Request {
				hr, ok := obj.(*helmv2.HelmRelease)
				if !ok {
					return nil
				}
				// Find the bundle that owns this HelmRelease by label
				bundleName := hr.Labels["cozystack.io/bundle"]
				if bundleName == "" {
					return nil
				}
				// Reconcile the bundle to recreate the HelmRelease if it was deleted
				return []reconcile.Request{{
					NamespacedName: types.NamespacedName{
						Name: bundleName,
					},
				}}
			}),
		).
		Complete(r)
}
