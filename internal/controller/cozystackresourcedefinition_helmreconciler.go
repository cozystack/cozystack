package controller

import (
	"context"
	"fmt"

	cozyv1alpha1 "github.com/cozystack/cozystack/api/v1alpha1"
	helmv2 "github.com/fluxcd/helm-controller/api/v2"

	"k8s.io/apimachinery/pkg/runtime"

	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"
)

// +kubebuilder:rbac:groups=cozystack.io,resources=cozystackresourcedefinitions,verbs=get;list;watch
// +kubebuilder:rbac:groups=helm.toolkit.fluxcd.io,resources=helmreleases,verbs=get;list;watch;update;patch

// CozystackResourceDefinitionHelmReconciler reconciles CozystackResourceDefinitions
// and updates related HelmReleases when a CozyRD changes.
// This controller does NOT watch HelmReleases to avoid mutual reconciliation storms
// with Flux's helm-controller.
type CozystackResourceDefinitionHelmReconciler struct {
	client.Client
	Scheme *runtime.Scheme
}

func (r *CozystackResourceDefinitionHelmReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	// Get the CozystackResourceDefinition that triggered this reconciliation
	crd := &cozyv1alpha1.CozystackResourceDefinition{}
	if err := r.Get(ctx, req.NamespacedName, crd); err != nil {
		logger.Error(err, "failed to get CozystackResourceDefinition", "name", req.Name)
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	// Update HelmReleases related to this specific CozyRD
	if err := r.updateHelmReleasesForCRD(ctx, crd); err != nil {
		logger.Error(err, "failed to update HelmReleases for CRD", "crd", crd.Name)
		return ctrl.Result{}, err
	}

	return ctrl.Result{}, nil
}

func (r *CozystackResourceDefinitionHelmReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		Named("cozystackresourcedefinition-helm-reconciler").
		For(&cozyv1alpha1.CozystackResourceDefinition{}).
		Complete(r)
}

// updateHelmReleasesForCRD updates all HelmReleases that match the application labels from CozystackResourceDefinition
func (r *CozystackResourceDefinitionHelmReconciler) updateHelmReleasesForCRD(ctx context.Context, crd *cozyv1alpha1.CozystackResourceDefinition) error {
	logger := log.FromContext(ctx)

	// Use application labels to find HelmReleases
	// Labels: apps.cozystack.io/application.kind and apps.cozystack.io/application.group
	applicationKind := crd.Spec.Application.Kind

	// Validate that applicationKind is non-empty
	if applicationKind == "" {
		logger.V(4).Info("Skipping HelmRelease update: Application.Kind is empty", "crd", crd.Name)
		return nil
	}

	applicationGroup := "apps.cozystack.io" // All applications use this group

	// Build label selector for HelmReleases
	// Only reconcile HelmReleases with cozystack.io/ui=true label
	labelSelector := client.MatchingLabels{
		"apps.cozystack.io/application.kind":  applicationKind,
		"apps.cozystack.io/application.group": applicationGroup,
		"cozystack.io/ui":                      "true",
	}

	// List all HelmReleases with matching labels
	hrList := &helmv2.HelmReleaseList{}
	if err := r.List(ctx, hrList, labelSelector); err != nil {
		logger.Error(err, "failed to list HelmReleases", "kind", applicationKind, "group", applicationGroup)
		return err
	}

	logger.V(4).Info("Found HelmReleases to update", "crd", crd.Name, "kind", applicationKind, "count", len(hrList.Items))

	// Update each HelmRelease
	for i := range hrList.Items {
		hr := &hrList.Items[i]
		if err := r.updateHelmReleaseChart(ctx, hr, crd); err != nil {
			logger.Error(err, "failed to update HelmRelease", "name", hr.Name, "namespace", hr.Namespace)
			continue
		}
	}

	return nil
}

// updateHelmReleaseChart updates the chart in HelmRelease based on CozystackResourceDefinition
func (r *CozystackResourceDefinitionHelmReconciler) updateHelmReleaseChart(ctx context.Context, hr *helmv2.HelmRelease, crd *cozyv1alpha1.CozystackResourceDefinition) error {
	logger := log.FromContext(ctx)
	hrCopy := hr.DeepCopy()
	updated := false

	// Validate Chart configuration exists
	if crd.Spec.Release.Chart.Name == "" {
		logger.V(4).Info("Skipping HelmRelease chart update: Chart.Name is empty", "crd", crd.Name)
		return nil
	}

	// Validate SourceRef fields
	if crd.Spec.Release.Chart.SourceRef.Kind == "" ||
		crd.Spec.Release.Chart.SourceRef.Name == "" ||
		crd.Spec.Release.Chart.SourceRef.Namespace == "" {
		logger.Error(fmt.Errorf("invalid SourceRef in CRD"), "Skipping HelmRelease chart update: SourceRef fields are incomplete",
			"crd", crd.Name,
			"kind", crd.Spec.Release.Chart.SourceRef.Kind,
			"name", crd.Spec.Release.Chart.SourceRef.Name,
			"namespace", crd.Spec.Release.Chart.SourceRef.Namespace)
		return nil
	}

	// Get version and reconcileStrategy from CRD or use defaults
	version := ">= 0.0.0-0"
	reconcileStrategy := "Revision"
	// TODO: Add Version and ReconcileStrategy fields to CozystackResourceDefinitionChart if needed

	// Build expected SourceRef
	expectedSourceRef := helmv2.CrossNamespaceObjectReference{
		Kind:      crd.Spec.Release.Chart.SourceRef.Kind,
		Name:      crd.Spec.Release.Chart.SourceRef.Name,
		Namespace: crd.Spec.Release.Chart.SourceRef.Namespace,
	}

	if hrCopy.Spec.Chart == nil {
		// Need to create Chart spec
		hrCopy.Spec.Chart = &helmv2.HelmChartTemplate{
			Spec: helmv2.HelmChartTemplateSpec{
				Chart:             crd.Spec.Release.Chart.Name,
				Version:           version,
				ReconcileStrategy: reconcileStrategy,
				SourceRef:         expectedSourceRef,
			},
		}
		updated = true
	} else {
		// Update existing Chart spec
		if hrCopy.Spec.Chart.Spec.Chart != crd.Spec.Release.Chart.Name ||
			hrCopy.Spec.Chart.Spec.SourceRef != expectedSourceRef {
			hrCopy.Spec.Chart.Spec.Chart = crd.Spec.Release.Chart.Name
			hrCopy.Spec.Chart.Spec.SourceRef = expectedSourceRef
			updated = true
		}
	}

	if updated {
		logger.V(4).Info("Updating HelmRelease chart", "name", hr.Name, "namespace", hr.Namespace)
		if err := r.Update(ctx, hrCopy); err != nil {
			return fmt.Errorf("failed to update HelmRelease: %w", err)
		}
	}

	return nil
}

