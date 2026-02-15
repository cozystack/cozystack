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

// ensureNavigation updates the Navigation resource to include a baseFactoriesMapping entry for the given CRD
func (m *Manager) ensureNavigation(ctx context.Context, crd *cozyv1alpha1.ApplicationDefinition) error {
	g, v, kind := pickGVK(crd)
	plural := pickPlural(kind, crd)

	lowerKind := strings.ToLower(kind)
	factoryKey := fmt.Sprintf("%s-details", lowerKind)

	// All CRD resources are namespaced API resources
	mappingKey := fmt.Sprintf("base-factory-namespaced-api-%s-%s-%s", g, v, plural)

	obj := &dashv1alpha1.Navigation{}
	obj.SetName("navigation")

	_, err := controllerutil.CreateOrUpdate(ctx, m.Client, obj, func() error {
		// Parse existing spec
		spec := make(map[string]any)
		if obj.Spec.JSON.Raw != nil {
			if err := json.Unmarshal(obj.Spec.JSON.Raw, &spec); err != nil {
				spec = make(map[string]any)
			}
		}

		// Get or create baseFactoriesMapping
		var mappings map[string]string
		if existing, ok := spec["baseFactoriesMapping"].(map[string]any); ok {
			mappings = make(map[string]string, len(existing))
			for k, val := range existing {
				if s, ok := val.(string); ok {
					mappings[k] = s
				}
			}
		} else {
			mappings = make(map[string]string)
		}

		// Add/update the mapping for this CRD
		mappings[mappingKey] = factoryKey

		spec["baseFactoriesMapping"] = mappings

		b, err := json.Marshal(spec)
		if err != nil {
			return err
		}

		newSpec := dashv1alpha1.ArbitrarySpec{JSON: apiextv1.JSON{Raw: b}}
		if !compareArbitrarySpecs(obj.Spec, newSpec) {
			obj.Spec = newSpec
		}
		return nil
	})
	return err
}
