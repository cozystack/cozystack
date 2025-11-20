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
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"
)

// CozystackResourceDefinitionReconciler reconciles CozystackResourceDefinition resources
type CozystackResourceDefinitionReconciler struct {
	client.Client
	Scheme *runtime.Scheme
}

// +kubebuilder:rbac:groups=cozystack.io,resources=cozystackresourcedefinitions,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=cozystack.io,resources=cozystackresourcedefinitions/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=source.extensions.fluxcd.io,resources=artifactgenerators,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=cozystack.io,resources=cozystackbundles,verbs=get;list;watch

// Reconcile is part of the main kubernetes reconciliation loop
func (r *CozystackResourceDefinitionReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	crd := &cozyv1alpha1.CozystackResourceDefinition{}
	if err := r.Get(ctx, req.NamespacedName, crd); err != nil {
		if apierrors.IsNotFound(err) {
			// Cleanup orphaned resources
			return r.cleanupOrphanedResources(ctx, req.NamespacedName)
		}
		return ctrl.Result{}, err
	}

	// Only process if source is specified
	if crd.Spec.Source == nil {
		return ctrl.Result{}, nil
	}

	// Get the bundle to get sourceRef
	bundle := &cozyv1alpha1.CozystackBundle{}
	if err := r.Get(ctx, types.NamespacedName{Name: crd.Spec.Source.BundleName}, bundle); err != nil {
		return ctrl.Result{}, fmt.Errorf("failed to get bundle %s: %w", crd.Spec.Source.BundleName, err)
	}

	// Generate ArtifactGenerator for this resource definition
	if err := r.reconcileArtifactGenerator(ctx, crd, bundle); err != nil {
		logger.Error(err, "failed to reconcile ArtifactGenerator")
		return ctrl.Result{}, err
	}

	return ctrl.Result{}, nil
}

// reconcileArtifactGenerator generates ArtifactGenerator from CozystackResourceDefinition
func (r *CozystackResourceDefinitionReconciler) reconcileArtifactGenerator(ctx context.Context, crd *cozyv1alpha1.CozystackResourceDefinition, bundle *cozyv1alpha1.CozystackBundle) error {
	logger := log.FromContext(ctx)

	// Determine artifact name from path
	prefix := r.getPackagePrefix(crd.Spec.Source.Path)
	pkgName := r.getPackageNameFromPath(crd.Spec.Source.Path)
	if prefix == "" || pkgName == "" {
		logger.Info("skipping resource definition with invalid path", "name", crd.Name, "path", crd.Spec.Source.Path)
		return nil
	}

	artifactName := fmt.Sprintf("%s-%s", prefix, pkgName)
	namespace := r.getNamespaceForPrefix(prefix)

	// Build copy operations
	copyOps := []sourcewatcherv1beta1.CopyOperation{
		{
			From: fmt.Sprintf("@%s/%s/**", bundle.Spec.SourceRef.Name, crd.Spec.Source.Path),
			To:   fmt.Sprintf("@artifact/%s/", pkgName),
		},
	}

	// Add libraries if specified
	libraryMap := make(map[string]cozyv1alpha1.BundleLibrary)
	for _, lib := range bundle.Spec.Libraries {
		libraryMap[lib.Name] = lib
	}

	for _, libName := range crd.Spec.Source.Libraries {
		if lib, ok := libraryMap[libName]; ok {
			copyOps = append(copyOps, sourcewatcherv1beta1.CopyOperation{
				From: fmt.Sprintf("@%s/%s/**", bundle.Spec.SourceRef.Name, lib.Path),
				To:   fmt.Sprintf("@artifact/%s/charts/%s/", pkgName, libName),
			})
		}
	}

	// Create or get ArtifactGenerator
	agName := artifactName
	ag := &sourcewatcherv1beta1.ArtifactGenerator{}
	key := types.NamespacedName{Name: agName, Namespace: namespace}

	err := r.Get(ctx, key, ag)
	if apierrors.IsNotFound(err) {
		// Create new ArtifactGenerator
		ag = &sourcewatcherv1beta1.ArtifactGenerator{
			ObjectMeta: metav1.ObjectMeta{
				Name:      agName,
				Namespace: namespace,
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
				OutputArtifacts: []sourcewatcherv1beta1.OutputArtifact{
					{
						Name: artifactName,
						Copy: copyOps,
					},
				},
			},
		}

		if err := r.Create(ctx, ag); err != nil {
			return fmt.Errorf("failed to create ArtifactGenerator: %w", err)
		}
		logger.Info("created ArtifactGenerator", "name", agName, "namespace", namespace)
	} else if err != nil {
		return fmt.Errorf("failed to get ArtifactGenerator: %w", err)
	} else {
		// Update existing ArtifactGenerator - add this output artifact if not present
		found := false
		for i, outputArtifact := range ag.Spec.OutputArtifacts {
			if outputArtifact.Name == artifactName {
				// Update existing artifact
				ag.Spec.OutputArtifacts[i].Copy = copyOps
				found = true
				break
			}
		}

		if !found {
			// Add new output artifact
			ag.Spec.OutputArtifacts = append(ag.Spec.OutputArtifacts, sourcewatcherv1beta1.OutputArtifact{
				Name: artifactName,
				Copy: copyOps,
			})
		}

		// Ensure source reference exists
		sourceFound := false
		for _, source := range ag.Spec.Sources {
			if source.Name == bundle.Spec.SourceRef.Name && source.Namespace == bundle.Spec.SourceRef.Namespace {
				sourceFound = true
				break
			}
		}
		if !sourceFound {
			ag.Spec.Sources = append(ag.Spec.Sources, sourcewatcherv1beta1.SourceReference{
				Alias:     bundle.Spec.SourceRef.Name,
				Kind:      bundle.Spec.SourceRef.Kind,
				Name:      bundle.Spec.SourceRef.Name,
				Namespace: bundle.Spec.SourceRef.Namespace,
			})
		}

		if err := r.Update(ctx, ag); err != nil {
			return fmt.Errorf("failed to update ArtifactGenerator: %w", err)
		}
		logger.Info("updated ArtifactGenerator", "name", agName, "namespace", namespace)
	}

	return nil
}

// Helper functions
func (r *CozystackResourceDefinitionReconciler) getPackagePrefix(path string) string {
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

func (r *CozystackResourceDefinitionReconciler) getNamespaceForPrefix(prefix string) string {
	if prefix == "system" {
		return "cozy-system"
	}
	return "cozy-public"
}

func (r *CozystackResourceDefinitionReconciler) getPackageNameFromPath(path string) string {
	parts := strings.Split(path, "/")
	if len(parts) > 0 {
		return parts[len(parts)-1]
	}
	return ""
}

func (r *CozystackResourceDefinitionReconciler) cleanupOrphanedResources(ctx context.Context, crdKey types.NamespacedName) (ctrl.Result, error) {
	// ArtifactGenerators are shared, so we don't delete them here
	// They will be cleaned up when all referencing CRDs are deleted
	return ctrl.Result{}, nil
}

// SetupWithManager sets up the controller with the Manager.
func (r *CozystackResourceDefinitionReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		Named("cozystack-resource-definition").
		For(&cozyv1alpha1.CozystackResourceDefinition{}).
		Complete(r)
}

