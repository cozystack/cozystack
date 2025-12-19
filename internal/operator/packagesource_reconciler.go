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
	sourcewatcherv1beta1 "github.com/fluxcd/source-watcher/api/v2/v1beta1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

// PackageSourceReconciler reconciles PackageSource resources
type PackageSourceReconciler struct {
	client.Client
	Scheme *runtime.Scheme
}

// +kubebuilder:rbac:groups=cozystack.io,resources=packagesources,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=cozystack.io,resources=packagesources/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=source.extensions.fluxcd.io,resources=artifactgenerators,verbs=get;list;watch;create;update;patch;delete

// Reconcile is part of the main kubernetes reconciliation loop
func (r *PackageSourceReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	packageSource := &cozyv1alpha1.PackageSource{}
	if err := r.Get(ctx, req.NamespacedName, packageSource); err != nil {
		if apierrors.IsNotFound(err) {
			// Resource not found, return (ownerReference will handle cleanup)
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}

	// Generate ArtifactGenerator for package source
	if err := r.reconcileArtifactGenerators(ctx, packageSource); err != nil {
		logger.Error(err, "failed to reconcile ArtifactGenerator")
		return ctrl.Result{}, err
	}

	// Update PackageSource status (variants and conditions from ArtifactGenerator)
	if err := r.updateStatus(ctx, packageSource); err != nil {
		logger.Error(err, "failed to update status")
		// Don't return error, status update is not critical
	}

	return ctrl.Result{}, nil
}

// reconcileArtifactGenerators generates a single ArtifactGenerator for the package source
// Creates one ArtifactGenerator per package source with all OutputArtifacts from components
func (r *PackageSourceReconciler) reconcileArtifactGenerators(ctx context.Context, packageSource *cozyv1alpha1.PackageSource) error {
	logger := log.FromContext(ctx)

	// Check if SourceRef is set
	if packageSource.Spec.SourceRef == nil {
		logger.Info("skipping ArtifactGenerator creation, SourceRef not set", "packageSource", packageSource.Name)
		return nil
	}

	// Build library map from all variants
	// Map key is the library name (from lib.Name or extracted from path)
	// This allows components to reference libraries by name
	libraryMap := make(map[string]cozyv1alpha1.Library)
	for _, variant := range packageSource.Spec.Variants {
		for _, lib := range variant.Libraries {
			libName := lib.Name
			if libName == "" {
				// If library name is not set, extract from path
				libName = r.getPackageNameFromPath(lib.Path)
			}
			if libName != "" {
				// Store library with the resolved name
				libraryMap[libName] = lib
			}
		}
	}

	// Namespace is always cozy-system
	namespace := "cozy-system"
	// ArtifactGenerator name is the package source name
	agName := packageSource.Name

	// Collect all OutputArtifacts
	outputArtifacts := []sourcewatcherv1beta1.OutputArtifact{}

	// Process all variants and their components
	for _, variant := range packageSource.Spec.Variants {
		for _, component := range variant.Components {
			// Skip components without path
			if component.Path == "" {
				logger.V(1).Info("skipping component without path", "packageSource", packageSource.Name, "variant", variant.Name, "component", component.Name)
				continue
			}

			logger.V(1).Info("processing component", "packageSource", packageSource.Name, "variant", variant.Name, "component", component.Name, "path", component.Path)

			// Extract component name from path (last component)
			componentPathName := r.getPackageNameFromPath(component.Path)
			if componentPathName == "" {
				logger.Info("skipping component with invalid path", "packageSource", packageSource.Name, "variant", variant.Name, "component", component.Name, "path", component.Path)
				continue
			}

			// Get basePath with default values
			basePath := r.getBasePath(packageSource)

			// Build copy operations
			copyOps := []sourcewatcherv1beta1.CopyOperation{
				{
					From: r.buildSourcePath(packageSource.Spec.SourceRef.Name, basePath, component.Path),
					To:   fmt.Sprintf("@artifact/%s/", componentPathName),
				},
			}

			// Add libraries if specified
			for _, libName := range component.Libraries {
				if lib, ok := libraryMap[libName]; ok {
					copyOps = append(copyOps, sourcewatcherv1beta1.CopyOperation{
						From: r.buildSourcePath(packageSource.Spec.SourceRef.Name, basePath, lib.Path),
						To:   fmt.Sprintf("@artifact/%s/charts/%s/", componentPathName, libName),
					})
				}
			}

			// Add valuesFiles if specified
			for i, valuesFile := range component.ValuesFiles {
				strategy := "Merge"
				if i == 0 {
					strategy = "Overwrite"
				}
				copyOps = append(copyOps, sourcewatcherv1beta1.CopyOperation{
					From:     r.buildSourceFilePath(packageSource.Spec.SourceRef.Name, basePath, fmt.Sprintf("%s/%s", component.Path, valuesFile)),
					To:       fmt.Sprintf("@artifact/%s/values.yaml", componentPathName),
					Strategy: strategy,
				})
			}

			// Artifact name: <packagesource>-<variant>-<componentname>
			// Replace dots with dashes to comply with Kubernetes naming requirements
			artifactName := fmt.Sprintf("%s-%s-%s",
				strings.ReplaceAll(packageSource.Name, ".", "-"),
				strings.ReplaceAll(variant.Name, ".", "-"),
				strings.ReplaceAll(component.Name, ".", "-"))

			outputArtifacts = append(outputArtifacts, sourcewatcherv1beta1.OutputArtifact{
				Name: artifactName,
				Copy: copyOps,
			})

			logger.Info("added OutputArtifact for component", "packageSource", packageSource.Name, "variant", variant.Name, "component", component.Name, "artifactName", artifactName)
		}
	}

	// If there are no OutputArtifacts, return (ownerReference will handle cleanup if needed)
	if len(outputArtifacts) == 0 {
		logger.Info("no OutputArtifacts to generate, skipping ArtifactGenerator creation", "packageSource", packageSource.Name)
		return nil
	}

	// Build labels
	labels := make(map[string]string)
	labels["cozystack.io/packagesource"] = packageSource.Name

	// Create single ArtifactGenerator for the package source
	ag := &sourcewatcherv1beta1.ArtifactGenerator{
		ObjectMeta: metav1.ObjectMeta{
			Name:      agName,
			Namespace: namespace,
			Labels:    labels,
		},
		Spec: sourcewatcherv1beta1.ArtifactGeneratorSpec{
			Sources: []sourcewatcherv1beta1.SourceReference{
				{
					Alias:     packageSource.Spec.SourceRef.Name,
					Kind:      packageSource.Spec.SourceRef.Kind,
					Name:      packageSource.Spec.SourceRef.Name,
					Namespace: packageSource.Spec.SourceRef.Namespace,
				},
			},
			OutputArtifacts: outputArtifacts,
		},
	}

	// Set ownerReference
	ag.OwnerReferences = []metav1.OwnerReference{
		{
			APIVersion: packageSource.APIVersion,
			Kind:       packageSource.Kind,
			Name:       packageSource.Name,
			UID:        packageSource.UID,
			Controller: func() *bool { b := true; return &b }(),
		},
	}

	logger.Info("creating ArtifactGenerator for package source", "packageSource", packageSource.Name, "agName", agName, "namespace", namespace, "outputArtifactCount", len(outputArtifacts))

	if err := r.createOrUpdate(ctx, ag); err != nil {
		return fmt.Errorf("failed to reconcile ArtifactGenerator %s: %w", agName, err)
	}

	logger.Info("reconciled ArtifactGenerator for package source", "name", agName, "namespace", namespace, "outputArtifactCount", len(outputArtifacts))

	return nil
}

// Helper functions
func (r *PackageSourceReconciler) getPackageNameFromPath(path string) string {
	parts := strings.Split(path, "/")
	if len(parts) > 0 {
		return parts[len(parts)-1]
	}
	return ""
}

// getBasePath returns the basePath with default values based on source kind
func (r *PackageSourceReconciler) getBasePath(packageSource *cozyv1alpha1.PackageSource) string {
	// If path is explicitly set in SourceRef, use it (but normalize "/" to empty)
	if packageSource.Spec.SourceRef.Path != "" {
		path := strings.Trim(packageSource.Spec.SourceRef.Path, "/")
		// If path is "/" or empty after trim, return empty string
		if path == "" {
			return ""
		}
		return path
	}
	// Default values based on kind
	if packageSource.Spec.SourceRef.Kind == "OCIRepository" {
		return "" // Root for OCI
	}
	// Default for GitRepository
	return "packages"
}

// buildSourcePath builds the full source path using basePath with glob pattern
func (r *PackageSourceReconciler) buildSourcePath(sourceName, basePath, path string) string {
	// Remove leading/trailing slashes and combine
	parts := []string{}
	if basePath != "" {
		trimmed := strings.Trim(basePath, "/")
		if trimmed != "" {
			parts = append(parts, trimmed)
		}
	}
	if path != "" {
		trimmed := strings.Trim(path, "/")
		if trimmed != "" {
			parts = append(parts, trimmed)
		}
	}

	fullPath := strings.Join(parts, "/")
	if fullPath == "" {
		return fmt.Sprintf("@%s/**", sourceName)
	}
	return fmt.Sprintf("@%s/%s/**", sourceName, fullPath)
}

// buildSourceFilePath builds the full source path for a specific file (without glob pattern)
func (r *PackageSourceReconciler) buildSourceFilePath(sourceName, basePath, path string) string {
	// Remove leading/trailing slashes and combine
	parts := []string{}
	if basePath != "" {
		trimmed := strings.Trim(basePath, "/")
		if trimmed != "" {
			parts = append(parts, trimmed)
		}
	}
	if path != "" {
		trimmed := strings.Trim(path, "/")
		if trimmed != "" {
			parts = append(parts, trimmed)
		}
	}

	fullPath := strings.Join(parts, "/")
	if fullPath == "" {
		return fmt.Sprintf("@%s", sourceName)
	}
	return fmt.Sprintf("@%s/%s", sourceName, fullPath)
}

// createOrUpdate creates or updates a resource
func (r *PackageSourceReconciler) createOrUpdate(ctx context.Context, obj client.Object) error {
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

	// For ArtifactGenerator, explicitly update Spec (OutputArtifacts and Sources)
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

	return r.Update(ctx, obj)
}

// updateStatus updates PackageSource status (variants and conditions from ArtifactGenerator)
func (r *PackageSourceReconciler) updateStatus(ctx context.Context, packageSource *cozyv1alpha1.PackageSource) error {
	logger := log.FromContext(ctx)

	// Update variants in status from spec
	variantNames := make([]string, 0, len(packageSource.Spec.Variants))
	for _, variant := range packageSource.Spec.Variants {
		variantNames = append(variantNames, variant.Name)
	}
	packageSource.Status.Variants = strings.Join(variantNames, ",")

	// Check if SourceRef is set
	if packageSource.Spec.SourceRef == nil {
		// Set status to unknown if SourceRef is not set
		meta.SetStatusCondition(&packageSource.Status.Conditions, metav1.Condition{
			Type:    "Ready",
			Status:  metav1.ConditionUnknown,
			Reason:  "SourceRefNotSet",
			Message: "SourceRef is not configured",
		})
		return r.Status().Update(ctx, packageSource)
	}

	// Get ArtifactGenerator
	ag := &sourcewatcherv1beta1.ArtifactGenerator{}
	agKey := types.NamespacedName{
		Name:      packageSource.Name,
		Namespace: "cozy-system",
	}

	if err := r.Get(ctx, agKey, ag); err != nil {
		if apierrors.IsNotFound(err) {
			// ArtifactGenerator not found, set status to unknown
			meta.SetStatusCondition(&packageSource.Status.Conditions, metav1.Condition{
				Type:    "Ready",
				Status:  metav1.ConditionUnknown,
				Reason:  "ArtifactGeneratorNotFound",
				Message: "ArtifactGenerator not found",
			})
			return r.Status().Update(ctx, packageSource)
		}
		return fmt.Errorf("failed to get ArtifactGenerator: %w", err)
	}

	// Find Ready condition in ArtifactGenerator
	readyCondition := meta.FindStatusCondition(ag.Status.Conditions, "Ready")
	if readyCondition == nil {
		// No Ready condition in ArtifactGenerator, set status to unknown
		meta.SetStatusCondition(&packageSource.Status.Conditions, metav1.Condition{
			Type:    "Ready",
			Status:  metav1.ConditionUnknown,
			Reason:  "ArtifactGeneratorNotReady",
			Message: "ArtifactGenerator Ready condition not found",
		})
		return r.Status().Update(ctx, packageSource)
	}

	// Copy Ready condition from ArtifactGenerator to PackageSource
	meta.SetStatusCondition(&packageSource.Status.Conditions, metav1.Condition{
		Type:               "Ready",
		Status:             readyCondition.Status,
		Reason:             readyCondition.Reason,
		Message:            readyCondition.Message,
		ObservedGeneration: packageSource.Generation,
		LastTransitionTime: readyCondition.LastTransitionTime,
	})

	logger.V(1).Info("updated PackageSource status from ArtifactGenerator",
		"packageSource", packageSource.Name,
		"status", readyCondition.Status,
		"reason", readyCondition.Reason)

	return r.Status().Update(ctx, packageSource)
}

// SetupWithManager sets up the controller with the Manager.
func (r *PackageSourceReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		Named("cozystack-packagesource").
		For(&cozyv1alpha1.PackageSource{}).
		Watches(
			&sourcewatcherv1beta1.ArtifactGenerator{},
			handler.EnqueueRequestsFromMapFunc(func(ctx context.Context, obj client.Object) []reconcile.Request {
				ag, ok := obj.(*sourcewatcherv1beta1.ArtifactGenerator)
				if !ok {
					return nil
				}
				// Find the PackageSource that owns this ArtifactGenerator by ownerReference
				for _, ownerRef := range ag.OwnerReferences {
					if ownerRef.Kind == "PackageSource" {
						return []reconcile.Request{{
							NamespacedName: types.NamespacedName{
								Name: ownerRef.Name,
							},
						}}
					}
				}
				return nil
			}),
		).
		Complete(r)
}

