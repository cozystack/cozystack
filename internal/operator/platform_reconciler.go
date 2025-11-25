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
	"fmt"
	"strings"

	cozyv1alpha1 "github.com/cozystack/cozystack/api/v1alpha1"
	helmv2 "github.com/fluxcd/helm-controller/api/v2"
	sourcewatcherv1beta1 "github.com/fluxcd/source-watcher/api/v2/v1beta1"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

// CozystackPlatformReconciler reconciles CozystackPlatform resources
type CozystackPlatformReconciler struct {
	client.Client
	Scheme *runtime.Scheme
}

// +kubebuilder:rbac:groups=cozystack.io,resources=cozystackplatforms,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=cozystack.io,resources=cozystackplatforms/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=helm.toolkit.fluxcd.io,resources=helmreleases,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=source.extensions.fluxcd.io,resources=artifactgenerators,verbs=get;list;watch;create;update;patch;delete

// Reconcile is part of the main kubernetes reconciliation loop
func (r *CozystackPlatformReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	platform := &cozyv1alpha1.CozystackPlatform{}
	if err := r.Get(ctx, req.NamespacedName, platform); err != nil {
		if apierrors.IsNotFound(err) {
			// Cleanup orphaned resources
			return r.cleanupOrphanedResources(ctx, req.NamespacedName)
		}
		return ctrl.Result{}, err
	}

	// Set defaults
	if platform.Spec.Interval == nil {
		platform.Spec.Interval = &metav1.Duration{Duration: 5 * 60 * 1000000000} // 5m
	}

	// Reconcile ArtifactGenerator
	if err := r.reconcileArtifactGenerator(ctx, platform); err != nil {
		logger.Error(err, "failed to reconcile ArtifactGenerator")
		return ctrl.Result{}, err
	}

	// Reconcile HelmRelease
	if err := r.reconcileHelmRelease(ctx, platform); err != nil {
		logger.Error(err, "failed to reconcile HelmRelease")
		return ctrl.Result{}, err
	}

	return ctrl.Result{}, nil
}

// reconcileArtifactGenerator creates or updates the ArtifactGenerator for the platform
func (r *CozystackPlatformReconciler) reconcileArtifactGenerator(ctx context.Context, platform *cozyv1alpha1.CozystackPlatform) error {
	logger := log.FromContext(ctx)

	// ArtifactGenerator name is the sourceRef name
	agName := platform.Spec.SourceRef.Name
	// Use fixed namespace for cluster-scoped resource
	namespace := "cozy-system"

	// Get basePath with default values (already includes full path to platform)
	basePath := r.getBasePath(platform)
	
	// Build full path from basePath (basePath already contains the full path)
	fullPath := r.buildSourcePath(platform.Spec.SourceRef.Name, basePath, "")
	// Extract the last component for the artifact name
	artifactPathParts := strings.Split(strings.Trim(basePath, "/"), "/")
	artifactName := artifactPathParts[len(artifactPathParts)-1]
	
	copyOps := []sourcewatcherv1beta1.CopyOperation{
		{
			From: fullPath + "/**",
			To:   fmt.Sprintf("@artifact/%s/", artifactName),
		},
	}

	// Create ArtifactGenerator
	ag := &sourcewatcherv1beta1.ArtifactGenerator{
		ObjectMeta: metav1.ObjectMeta{
			Name:      agName,
			Namespace: namespace,
			Labels: map[string]string{
				"cozystack.io/platform": platform.Name,
			},
		},
		Spec: sourcewatcherv1beta1.ArtifactGeneratorSpec{
			Sources: []sourcewatcherv1beta1.SourceReference{
				{
					Alias:     platform.Spec.SourceRef.Name,
					Kind:      platform.Spec.SourceRef.Kind,
					Name:      platform.Spec.SourceRef.Name,
					Namespace: platform.Spec.SourceRef.Namespace,
				},
			},
			OutputArtifacts: []sourcewatcherv1beta1.OutputArtifact{
				{
					Name: artifactName,
					Copy: copyOps,
				},
			},
		},
	}

	// Set ownerReference
	ag.OwnerReferences = []metav1.OwnerReference{
		{
			APIVersion: platform.APIVersion,
			Kind:       platform.Kind,
			Name:       platform.Name,
			UID:        platform.UID,
			Controller: func() *bool { b := true; return &b }(),
		},
	}

	logger.Info("reconciling ArtifactGenerator", "name", agName, "namespace", namespace)

	if err := r.createOrUpdate(ctx, ag); err != nil {
		return fmt.Errorf("failed to reconcile ArtifactGenerator %s: %w", agName, err)
	}

	logger.Info("reconciled ArtifactGenerator", "name", agName, "namespace", namespace)
	return nil
}

// reconcileHelmRelease creates or updates the HelmRelease for the platform
func (r *CozystackPlatformReconciler) reconcileHelmRelease(ctx context.Context, platform *cozyv1alpha1.CozystackPlatform) error {
	logger := log.FromContext(ctx)

	// HelmRelease name is fixed: cozystack-platform
	hrName := "cozystack-platform"
	// Use fixed namespace for cluster-scoped resource
	namespace := "cozy-system"

	// Get artifact name (last component of basePath)
	basePath := r.getBasePath(platform)
	artifactPathParts := strings.Split(strings.Trim(basePath, "/"), "/")
	artifactName := artifactPathParts[len(artifactPathParts)-1]

	// Merge values with sourceRef
	values := r.mergeValuesWithSourceRef(platform.Spec.Values, platform.Spec.SourceRef)

	// Create HelmRelease
	hr := &helmv2.HelmRelease{
		ObjectMeta: metav1.ObjectMeta{
			Name:      hrName,
			Namespace: namespace,
			Labels: map[string]string{
				"cozystack.io/platform": platform.Name,
			},
		},
		Spec: helmv2.HelmReleaseSpec{
			Interval:        *platform.Spec.Interval,
			TargetNamespace: "cozy-system",
			ReleaseName:     "cozystack-platform",
			ChartRef: &helmv2.CrossNamespaceSourceReference{
				Kind:      "ExternalArtifact",
				Name:      artifactName,
				Namespace: namespace,
			},
			Values: values,
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

	// Set ownerReference
	hr.OwnerReferences = []metav1.OwnerReference{
		{
			APIVersion: platform.APIVersion,
			Kind:       platform.Kind,
			Name:       platform.Name,
			UID:        platform.UID,
			Controller: func() *bool { b := true; return &b }(),
		},
	}

	logger.Info("reconciling HelmRelease", "name", hrName, "namespace", namespace)

	if err := r.createOrUpdate(ctx, hr); err != nil {
		return fmt.Errorf("failed to reconcile HelmRelease %s: %w", hrName, err)
	}

	logger.Info("reconciled HelmRelease", "name", hrName, "namespace", namespace)
	return nil
}

// mergeValuesWithSourceRef merges platform values with sourceRef
func (r *CozystackPlatformReconciler) mergeValuesWithSourceRef(values *apiextensionsv1.JSON, sourceRef cozyv1alpha1.SourceRef) *apiextensionsv1.JSON {
	// Build sourceRef map
	sourceRefMap := map[string]interface{}{
		"kind":      sourceRef.Kind,
		"name":      sourceRef.Name,
		"namespace": sourceRef.Namespace,
	}

	// If values is nil or empty, create new values with sourceRef
	if values == nil || len(values.Raw) == 0 {
		valuesMap := map[string]interface{}{
			"sourceRef": sourceRefMap,
		}
		raw, _ := json.Marshal(valuesMap)
		return &apiextensionsv1.JSON{Raw: raw}
	}

	// Parse existing values
	var valuesMap map[string]interface{}
	if err := json.Unmarshal(values.Raw, &valuesMap); err != nil {
		// If unmarshal fails, create new values with sourceRef
		valuesMap = map[string]interface{}{
			"sourceRef": sourceRefMap,
		}
		raw, _ := json.Marshal(valuesMap)
		return &apiextensionsv1.JSON{Raw: raw}
	}

	// Merge sourceRef into values (overwrite if exists)
	valuesMap["sourceRef"] = sourceRefMap

	// Marshal back to JSON
	raw, err := json.Marshal(valuesMap)
	if err != nil {
		// If marshal fails, return original values
		return values
	}

	return &apiextensionsv1.JSON{Raw: raw}
}

// getBasePath returns the basePath with default values based on source kind
func (r *CozystackPlatformReconciler) getBasePath(platform *cozyv1alpha1.CozystackPlatform) string {
	if platform.Spec.BasePath != "" {
		return platform.Spec.BasePath
	}
	// Default values based on kind
	if platform.Spec.SourceRef.Kind == "OCIRepository" {
		return "core/platform" // Full path for OCI
	}
	// Default for GitRepository
	return "packages/core/platform" // Full path for Git
}

// buildSourcePath builds the full source path from basePath and chart path
func (r *CozystackPlatformReconciler) buildSourcePath(sourceName, basePath, chartPath string) string {
	// Remove leading/trailing slashes and combine
	parts := []string{}
	if basePath != "" {
		parts = append(parts, strings.Trim(basePath, "/"))
	}
	if chartPath != "" {
		parts = append(parts, strings.Trim(chartPath, "/"))
	}
	fullPath := strings.Join(parts, "/")
	if fullPath == "" {
		return fmt.Sprintf("@%s", sourceName)
	}
	return fmt.Sprintf("@%s/%s", sourceName, fullPath)
}

// cleanupOrphanedResources removes ArtifactGenerator and HelmRelease when CozystackPlatform is deleted
func (r *CozystackPlatformReconciler) cleanupOrphanedResources(ctx context.Context, name client.ObjectKey) (ctrl.Result, error) {
	// OwnerReferences should handle cleanup automatically
	// This function is kept for potential future cleanup logic
	// Note: name is ObjectKey, but for cluster-scoped resources only Name is used
	return ctrl.Result{}, nil
}

// createOrUpdate creates or updates a resource
func (r *CozystackPlatformReconciler) createOrUpdate(ctx context.Context, obj client.Object) error {
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

	// For ArtifactGenerator, explicitly update Spec and ownerReferences
	if ag, ok := obj.(*sourcewatcherv1beta1.ArtifactGenerator); ok {
		if existingAG, ok := existing.(*sourcewatcherv1beta1.ArtifactGenerator); ok {
			logger := log.FromContext(ctx)
			logger.V(1).Info("updating ArtifactGenerator Spec", "name", ag.Name, "namespace", ag.Namespace)
			existingAG.Spec = ag.Spec
			existingAG.SetLabels(ag.GetLabels())
			existingAG.SetAnnotations(ag.GetAnnotations())
			// Always use ownerReferences from the new object (set in reconcileArtifactGenerator)
			existingAG.SetOwnerReferences(ag.GetOwnerReferences())
			obj = existingAG
		}
	}

	// For HelmRelease, explicitly update Spec and ownerReferences
	if hr, ok := obj.(*helmv2.HelmRelease); ok {
		if existingHR, ok := existing.(*helmv2.HelmRelease); ok {
			logger := log.FromContext(ctx)
			logger.V(1).Info("updating HelmRelease Spec", "name", hr.Name, "namespace", hr.Namespace)
			existingHR.Spec = hr.Spec
			existingHR.SetLabels(hr.GetLabels())
			existingHR.SetAnnotations(hr.GetAnnotations())
			// Always use ownerReferences from the new object (set in reconcileHelmRelease)
			existingHR.SetOwnerReferences(hr.GetOwnerReferences())
			obj = existingHR
		}
	}

	return r.Update(ctx, obj)
}

// SetupWithManager sets up the controller with the Manager
func (r *CozystackPlatformReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		Named("cozystack-platform").
		For(&cozyv1alpha1.CozystackPlatform{}).
		Watches(
			&helmv2.HelmRelease{},
			handler.EnqueueRequestsFromMapFunc(func(ctx context.Context, obj client.Object) []reconcile.Request {
				hr, ok := obj.(*helmv2.HelmRelease)
				if !ok {
					return nil
				}
				// Only watch HelmReleases with cozystack.io/platform label
				platformName := hr.Labels["cozystack.io/platform"]
				if platformName == "" {
					return nil
				}
				return []reconcile.Request{
					{
						NamespacedName: client.ObjectKey{
							Name: platformName,
							// Cluster-scoped resource has no namespace
						},
					},
				}
			}),
			builder.WithPredicates(
				predicate.NewPredicateFuncs(func(obj client.Object) bool {
					// Only watch resources with cozystack.io/platform label
					labels := obj.GetLabels()
					return labels != nil && labels["cozystack.io/platform"] != ""
				}),
			),
		).
		Watches(
			&sourcewatcherv1beta1.ArtifactGenerator{},
			handler.EnqueueRequestsFromMapFunc(func(ctx context.Context, obj client.Object) []reconcile.Request {
				ag, ok := obj.(*sourcewatcherv1beta1.ArtifactGenerator)
				if !ok {
					return nil
				}
				// Only watch ArtifactGenerators with cozystack.io/platform label
				platformName := ag.Labels["cozystack.io/platform"]
				if platformName == "" {
					return nil
				}
				return []reconcile.Request{
					{
						NamespacedName: client.ObjectKey{
							Name: platformName,
							// Cluster-scoped resource has no namespace
						},
					},
				}
			}),
			builder.WithPredicates(
				predicate.NewPredicateFuncs(func(obj client.Object) bool {
					// Only watch resources with cozystack.io/platform label
					labels := obj.GetLabels()
					return labels != nil && labels["cozystack.io/platform"] != ""
				}),
			),
		).
		Complete(r)
}

