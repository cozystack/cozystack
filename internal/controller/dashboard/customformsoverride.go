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
)

// ensureCustomFormsOverride creates or updates a CustomFormsOverride resource for the given CRD
func (m *Manager) ensureCustomFormsOverride(ctx context.Context, crd *cozyv1alpha1.CozystackResourceDefinition) error {
	g, v, kind := pickGVK(crd)
	plural := pickPlural(kind, crd)

	name := fmt.Sprintf("%s.%s.%s", g, v, plural)
	customizationID := fmt.Sprintf("default-/%s/%s/%s", g, v, plural)

	obj := &dashv1alpha1.CustomFormsOverride{}
	obj.SetName(name)

	// Replicates your Helm includes (system metadata + api + status).
	hidden := []any{}
	hidden = append(hidden, hiddenMetadataSystem()...)
	hidden = append(hidden, hiddenMetadataAPI()...)
	hidden = append(hidden, hiddenStatus()...)

	// If Name is set, hide metadata
	if crd.Spec.Dashboard != nil && strings.TrimSpace(crd.Spec.Dashboard.Name) != "" {
		hidden = append([]interface{}{
			[]any{"metadata"},
		}, hidden...)
	}

	var sort []any
	if crd.Spec.Dashboard != nil && len(crd.Spec.Dashboard.KeysOrder) > 0 {
		sort = make([]any, len(crd.Spec.Dashboard.KeysOrder))
		for i, v := range crd.Spec.Dashboard.KeysOrder {
			sort[i] = v
		}
	}

	spec := map[string]any{
		"customizationId": customizationID,
		"hidden":          hidden,
		"sort":            sort,
		"schema":          map[string]any{}, // {}
		"strategy":        "merge",
	}

	_, err := controllerutil.CreateOrUpdate(ctx, m.client, obj, func() error {
		if err := controllerutil.SetOwnerReference(crd, obj, m.scheme); err != nil {
			return err
		}
		// Add dashboard labels to dynamic resources
		m.addDashboardLabels(obj, crd, ResourceTypeDynamic)
		b, err := json.Marshal(spec)
		if err != nil {
			return err
		}

		// Only update spec if it's different to avoid unnecessary updates
		newSpec := dashv1alpha1.ArbitrarySpec{JSON: apiextv1.JSON{Raw: b}}
		if !compareArbitrarySpecs(obj.Spec, newSpec) {
			obj.Spec = newSpec
		}
		return nil
	})
	return err
}
