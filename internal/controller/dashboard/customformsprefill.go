package dashboard

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	dashv1alpha1 "github.com/cozystack/cozystack/api/dashboard/v1alpha1"
	cozyv1alpha1 "github.com/cozystack/cozystack/api/v1alpha1"

	apiextv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

// ensureCustomFormsPrefill creates or updates a CustomFormsPrefill resource for the given CRD
func (m *Manager) ensureCustomFormsPrefill(ctx context.Context, crd *cozyv1alpha1.CozystackResourceDefinition) (reconcile.Result, error) {
	logger := log.FromContext(ctx)

	app := crd.Spec.Application
	group, version, kind := pickGVK(crd)
	plural := pickPlural(kind, crd)

	name := fmt.Sprintf("%s.%s.%s", group, version, plural)
	customizationID := fmt.Sprintf("default-/%s/%s/%s", group, version, plural)

	values, err := buildPrefillValues(app.OpenAPISchema)
	if err != nil {
		return reconcile.Result{}, err
	}

	// Always prefill metadata.name (empty string if not specified in CRD)
	var nameValue string
	if crd.Spec.Dashboard != nil {
		nameValue = strings.TrimSpace(crd.Spec.Dashboard.Name)
	}
	values = append([]interface{}{
		map[string]interface{}{
			"path":  toIfaceSlice([]string{"metadata", "name"}),
			"value": nameValue,
		},
	}, values...)

	cfp := &dashv1alpha1.CustomFormsPrefill{}
	cfp.Name = name // cluster-scoped

	specMap := map[string]any{
		"customizationId": customizationID,
		"values":          values,
	}
	// Use json.Marshal with sorted keys to ensure consistent output
	specBytes, err := json.Marshal(specMap)
	if err != nil {
		return reconcile.Result{}, err
	}

	_, err = controllerutil.CreateOrUpdate(ctx, m.Client, cfp, func() error {
		if err := controllerutil.SetOwnerReference(crd, cfp, m.Scheme); err != nil {
			return err
		}
		// Add dashboard labels to dynamic resources
		m.addDashboardLabels(cfp, crd, ResourceTypeDynamic)

		// Only update spec if it's different to avoid unnecessary updates
		newSpec := dashv1alpha1.ArbitrarySpec{
			JSON: apiextv1.JSON{Raw: specBytes},
		}
		if !compareArbitrarySpecs(cfp.Spec, newSpec) {
			cfp.Spec = newSpec
		}
		return nil
	})
	if err != nil {
		return reconcile.Result{}, err
	}

	logger.Info("Applied CustomFormsPrefill", "name", cfp.Name)
	return reconcile.Result{}, nil
}
