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

// ensureBreadcrumb creates or updates a Breadcrumb resource for the given CRD
func (m *Manager) ensureBreadcrumb(ctx context.Context, crd *cozyv1alpha1.CozystackResourceDefinition) error {
	group, version, kind := pickGVK(crd)

	lowerKind := strings.ToLower(kind)
	detailID := fmt.Sprintf("stock-project-factory-%s-details", lowerKind)

	obj := &dashv1alpha1.Breadcrumb{}
	obj.SetName(detailID)

	plural := pickPlural(kind, crd)

	// Prefer dashboard.Plural for UI label if provided
	labelPlural := titleFromKindPlural(kind, plural)
	if crd != nil && crd.Spec.Dashboard != nil && crd.Spec.Dashboard.Plural != "" {
		labelPlural = crd.Spec.Dashboard.Plural
	}

	key := plural // e.g., "virtualmachines"
	label := labelPlural
	link := fmt.Sprintf("/openapi-ui/{clusterName}/{namespace}/api-table/%s/%s/%s", strings.ToLower(group), strings.ToLower(version), plural)
	// If this is a module, change the first breadcrumb item to "Tenant Modules"
	if crd.Spec.Dashboard != nil && crd.Spec.Dashboard.Module {
		key = "tenantmodules"
		label = "Tenant Modules"
		link = "/openapi-ui/{clusterName}/{namespace}/api-table/core.cozystack.io/v1alpha1/tenantmodules"
	}

	items := []any{
		map[string]any{
			"key":   key,
			"label": label,
			"link":  link,
		},
		map[string]any{
			"key":   strings.ToLower(kind), // "etcd"
			"label": "{6}",                 // literal, as in your example
		},
	}

	spec := map[string]any{
		"id":              detailID,
		"breadcrumbItems": items,
	}

	_, err := controllerutil.CreateOrUpdate(ctx, m.Client, obj, func() error {
		if err := controllerutil.SetOwnerReference(crd, obj, m.Scheme); err != nil {
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
