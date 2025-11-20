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

	cozyv1alpha1 "github.com/cozystack/cozystack/api/v1alpha1"
	helmv2 "github.com/fluxcd/helm-controller/api/v2"
	sourcev1 "github.com/fluxcd/source-controller/api/v1"
	"github.com/fluxcd/pkg/apis/meta"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"
)

// CozystackPlatformConfigurationReconciler reconciles CozystackPlatformConfiguration resources
type CozystackPlatformConfigurationReconciler struct {
	client.Client
	Scheme *runtime.Scheme
}

// +kubebuilder:rbac:groups=cozystack.io,resources=cozystackplatformconfigurations,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=cozystack.io,resources=cozystackplatformconfigurations/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=source.toolkit.fluxcd.io,resources=gitrepositories,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=helm.toolkit.fluxcd.io,resources=helmreleases,verbs=get;list;watch;create;update;patch;delete

// Reconcile is part of the main kubernetes reconciliation loop
func (r *CozystackPlatformConfigurationReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	config := &cozyv1alpha1.CozystackPlatformConfiguration{}
	if err := r.Get(ctx, req.NamespacedName, config); err != nil {
		if apierrors.IsNotFound(err) {
			// Resource deleted, cleanup will be handled by ownerReferences
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}

	// Reconcile GitRepository
	if err := r.reconcileGitRepository(ctx, config); err != nil {
		logger.Error(err, "failed to reconcile GitRepository")
		return ctrl.Result{}, err
	}

	// Reconcile HelmRelease
	if err := r.reconcileHelmRelease(ctx, config); err != nil {
		logger.Error(err, "failed to reconcile HelmRelease")
		return ctrl.Result{}, err
	}

	logger.Info("successfully reconciled CozystackPlatformConfiguration")
	return ctrl.Result{}, nil
}

// reconcileGitRepository creates or updates the GitRepository resource
func (r *CozystackPlatformConfigurationReconciler) reconcileGitRepository(ctx context.Context, config *cozyv1alpha1.CozystackPlatformConfiguration) error {
	logger := log.FromContext(ctx)

	// GitRepository name is the same as the configuration name
	gitRepoName := config.Name
	// GitRepository namespace is cozy-system (hardcoded for platform configuration)
	gitRepoNamespace := "cozy-system"

	desiredGitRepo := &sourcev1.GitRepository{
		ObjectMeta: metav1.ObjectMeta{
			Name:      gitRepoName,
			Namespace: gitRepoNamespace,
			OwnerReferences: []metav1.OwnerReference{
				{
					APIVersion: config.APIVersion,
					Kind:       config.Kind,
					Name:       config.Name,
					UID:        config.UID,
					Controller: func() *bool { b := true; return &b }(),
				},
			},
		},
		Spec: sourcev1.GitRepositorySpec{
			URL:       config.Spec.Source.URL,
			Interval:  config.Spec.Source.Interval,
			Timeout:   config.Spec.Source.Timeout,
			Ignore:    config.Spec.Source.Ignore,
			Include:   config.Spec.Source.Include,
			RecurseSubmodules: config.Spec.Source.RecurseSubmodules,
			Verification:      config.Spec.Source.Verification,
		},
	}

	// Set ref if provided
	if config.Spec.Source.Ref != nil {
		desiredGitRepo.Spec.Reference = config.Spec.Source.Ref
	}

	// Set SecretRef if provided (convert from corev1.LocalObjectReference to meta.LocalObjectReference)
	if config.Spec.Source.SecretRef != nil {
		desiredGitRepo.Spec.SecretRef = &meta.LocalObjectReference{
			Name: config.Spec.Source.SecretRef.Name,
		}
	}

	if err := r.createOrUpdate(ctx, desiredGitRepo); err != nil {
		return fmt.Errorf("failed to create or update GitRepository: %w", err)
	}

	logger.Info("reconciled GitRepository", "name", gitRepoName, "namespace", gitRepoNamespace)
	return nil
}

// reconcileHelmRelease creates or updates the HelmRelease resource
func (r *CozystackPlatformConfigurationReconciler) reconcileHelmRelease(ctx context.Context, config *cozyv1alpha1.CozystackPlatformConfiguration) error {
	logger := log.FromContext(ctx)

	// HelmRelease name is the same as the configuration name
	hrName := config.Name
	// HelmRelease namespace is cozy-system (hardcoded for platform configuration)
	hrNamespace := "cozy-system"

	// GitRepository name (used in sourceRef)
	gitRepoName := config.Name
	gitRepoNamespace := "cozy-system"

	desiredHR := &helmv2.HelmRelease{
		ObjectMeta: metav1.ObjectMeta{
			Name:      hrName,
			Namespace: hrNamespace,
			OwnerReferences: []metav1.OwnerReference{
				{
					APIVersion: config.APIVersion,
					Kind:       config.Kind,
					Name:       config.Name,
					UID:        config.UID,
					Controller: func() *bool { b := true; return &b }(),
				},
			},
		},
		Spec: helmv2.HelmReleaseSpec{
			Interval:    config.Spec.Chart.Interval,
			ReleaseName: hrName,
			TargetNamespace: hrNamespace,
			Chart: &helmv2.HelmChartTemplate{
				Spec: helmv2.HelmChartTemplateSpec{
					Chart: config.Spec.Chart.Path,
					SourceRef: helmv2.CrossNamespaceObjectReference{
						Kind:      "GitRepository",
						Name:      gitRepoName,
						Namespace: gitRepoNamespace,
					},
				},
			},
		},
	}

	// Set values if provided
	if config.Spec.Values != nil {
		desiredHR.Spec.Values = config.Spec.Values
	}

	if err := r.createOrUpdate(ctx, desiredHR); err != nil {
		return fmt.Errorf("failed to create or update HelmRelease: %w", err)
	}

	logger.Info("reconciled HelmRelease", "name", hrName, "namespace", hrNamespace)
	return nil
}

// createOrUpdate creates or updates a Kubernetes resource
func (r *CozystackPlatformConfigurationReconciler) createOrUpdate(ctx context.Context, obj client.Object) error {
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

	// Owner references are set by the caller and should be preserved/updated
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

// SetupWithManager sets up the controller with the Manager
func (r *CozystackPlatformConfigurationReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&cozyv1alpha1.CozystackPlatformConfiguration{}).
		Complete(r)
}

