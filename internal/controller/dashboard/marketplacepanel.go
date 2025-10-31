package dashboard

import (
	"context"
	"encoding/json"

	dashv1alpha1 "github.com/cozystack/cozystack/api/dashboard/v1alpha1"
	cozyv1alpha1 "github.com/cozystack/cozystack/api/v1alpha1"

	apiextv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

// ensureMarketplacePanel creates or updates a MarketplacePanel resource for the given CRD
func (m *Manager) ensureMarketplacePanel(ctx context.Context, crd *cozyv1alpha1.CozystackResourceDefinition) (reconcile.Result, error) {
	logger := log.FromContext(ctx)

	mp := &dashv1alpha1.MarketplacePanel{}
	mp.Name = crd.Name // cluster-scoped resource, name mirrors CRD name

	// If dashboard is not set, delete the panel if it exists.
	if crd.Spec.Dashboard == nil {
		err := m.Get(ctx, client.ObjectKey{Name: mp.Name}, mp)
		if apierrors.IsNotFound(err) {
			return reconcile.Result{}, nil
		}
		if err != nil {
			return reconcile.Result{}, err
		}
		if err := m.Delete(ctx, mp); err != nil && !apierrors.IsNotFound(err) {
			return reconcile.Result{}, err
		}
		logger.Info("Deleted MarketplacePanel because dashboard is not set", "name", mp.Name)
		return reconcile.Result{}, nil
	}

	// Skip module and tenant resources (they don't need MarketplacePanel)
	if crd.Spec.Dashboard.Module || crd.Spec.Application.Kind == "Tenant" {
		err := m.Get(ctx, client.ObjectKey{Name: mp.Name}, mp)
		if apierrors.IsNotFound(err) {
			return reconcile.Result{}, nil
		}
		if err != nil {
			return reconcile.Result{}, err
		}
		if err := m.Delete(ctx, mp); err != nil && !apierrors.IsNotFound(err) {
			return reconcile.Result{}, err
		}
		logger.Info("Deleted MarketplacePanel because resource is a module", "name", mp.Name)
		return reconcile.Result{}, nil
	}

	// Build desired spec from CRD fields
	d := crd.Spec.Dashboard
	app := crd.Spec.Application

	displayName := d.Singular
	if displayName == "" {
		displayName = app.Kind
	}

	tags := make([]any, len(d.Tags))
	for i, t := range d.Tags {
		tags[i] = t
	}

	specMap := map[string]any{
		"description": d.Description,
		"name":        displayName,
		"type":        "nonCrd",
		"apiGroup":    "apps.cozystack.io",
		"apiVersion":  "v1alpha1",
		"typeName":    app.Plural, // e.g., "buckets"
		"disabled":    false,
		"hidden":      false,
		"tags":        tags,
		"icon":        d.Icon,
	}

	specBytes, err := json.Marshal(specMap)
	if err != nil {
		return reconcile.Result{}, err
	}

	_, err = controllerutil.CreateOrUpdate(ctx, m.Client, mp, func() error {
		if err := controllerutil.SetOwnerReference(crd, mp, m.Scheme); err != nil {
			return err
		}
		// Add dashboard labels to dynamic resources
		m.addDashboardLabels(mp, crd, ResourceTypeDynamic)

		// Only update spec if it's different to avoid unnecessary updates
		newSpec := dashv1alpha1.ArbitrarySpec{
			JSON: apiextv1.JSON{Raw: specBytes},
		}
		if !compareArbitrarySpecs(mp.Spec, newSpec) {
			mp.Spec = newSpec
		}
		return nil
	})
	if err != nil {
		return reconcile.Result{}, err
	}

	logger.Info("Applied MarketplacePanel", "name", mp.Name)
	return reconcile.Result{}, nil
}
