package dashboard

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	dashv1alpha1 "github.com/cozystack/cozystack/api/dashboard/v1alpha1"
	cozyv1alpha1 "github.com/cozystack/cozystack/api/v1alpha1"

	apiextv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
)

// ensureSidebar creates/updates multiple Sidebar resources that share the same menu:
//   - The "details" sidebar tied to the current kind (stock-project-factory-<kind>-details)
//   - The stock-instance sidebars: api-form, api-table, builtin-form, builtin-table
//   - The stock-project sidebars:  api-form, api-table, builtin-form, builtin-table, crd-form, crd-table
//
// Menu rules:
//   - The first section is "Marketplace" with two hardcoded entries:
//   - Marketplace  (/openapi-ui/{clusterName}/{namespace}/factory/marketplace)
//   - Tenant Info  (/openapi-ui/{clusterName}/{namespace}/factory/info-details/info)
//   - All other sections are built from CRDs where spec.dashboard != nil.
//   - Categories are ordered strictly as:
//     Marketplace, IaaS, PaaS, NaaS, <others A→Z>, Resources, Backups, Administration
//   - Items within each category: sort by Weight (desc), then Label (A→Z).
func (m *Manager) ensureSidebar(ctx context.Context, crd *cozyv1alpha1.CozystackResourceDefinition) error {
	// Build the full menu once.

	// 1) Fetch all CRDs
	var all []cozyv1alpha1.CozystackResourceDefinition
	var crdList cozyv1alpha1.CozystackResourceDefinitionList
	if err := m.List(ctx, &crdList, &client.ListOptions{}); err != nil {
		return err
	}
	all = crdList.Items

	// 2) Build category -> []item map (only for CRDs with spec.dashboard != nil)
	type item struct {
		Key    string
		Label  string
		Link   string
		Weight int
	}
	categories := map[string][]item{} // category label -> children
	keysAndTags := map[string]any{}   // plural -> []string{ "<lower(kind)>-sidebar" }

	// Collect sidebar names for module resources
	var moduleSidebars []any

	for i := range all {
		def := &all[i]

		// Include ONLY when spec.dashboard != nil
		if def.Spec.Dashboard == nil {
			continue
		}

		g, v, kind := pickGVK(def)
		plural := pickPlural(kind, def)
		lowerKind := strings.ToLower(kind)

		// Check if this resource is a module
		if def.Spec.Dashboard.Module {
			// Special case: info should have its own keysAndTags, not be in modules
			if lowerKind == "info" {
				keysAndTags[plural] = []any{fmt.Sprintf("%s-sidebar", lowerKind)}
			} else {
				// Add to modules sidebar list
				moduleSidebars = append(moduleSidebars, fmt.Sprintf("%s-sidebar", lowerKind))
			}
		} else {
			// Add to keysAndTags for non-module resources
			keysAndTags[plural] = []any{fmt.Sprintf("%s-sidebar", lowerKind)}
		}

		// Only add to menu categories if not a module
		if !def.Spec.Dashboard.Module {
			cat := safeCategory(def) // falls back to "Resources" if empty

			// Label: prefer dashboard.Plural if provided
			label := titleFromKindPlural(kind, plural)
			if def.Spec.Dashboard.Plural != "" {
				label = def.Spec.Dashboard.Plural
			}

			// Weight (default 0)
			weight := def.Spec.Dashboard.Weight

			link := fmt.Sprintf("/openapi-ui/{clusterName}/{namespace}/api-table/%s/%s/%s", g, v, plural)

			categories[cat] = append(categories[cat], item{
				Key:    plural,
				Label:  label,
				Link:   link,
				Weight: weight,
			})
		}
	}

	// Add modules to keysAndTags if we have any module sidebars
	if len(moduleSidebars) > 0 {
		keysAndTags["modules"] = moduleSidebars
	}

	// Add sidebars for built-in Kubernetes resources
	keysAndTags["services"] = []any{"service-sidebar"}
	keysAndTags["secrets"] = []any{"secret-sidebar"}
	keysAndTags["ingresses"] = []any{"ingress-sidebar"}

	// Add sidebar for backups.cozystack.io Plan resource
	keysAndTags["plans"] = []any{"plan-sidebar"}

	// 3) Sort items within each category by Weight (desc), then Label (A→Z)
	for cat := range categories {
		sort.Slice(categories[cat], func(i, j int) bool {
			if categories[cat][i].Weight != categories[cat][j].Weight {
				return categories[cat][i].Weight < categories[cat][j].Weight // lower weight first
			}
			return strings.ToLower(categories[cat][i].Label) < strings.ToLower(categories[cat][j].Label)
		})
	}

	// 4) Order categories strictly:
	//    Marketplace (hardcoded), IaaS, PaaS, NaaS, <others A→Z>, Resources, Backups (hardcoded), Administration (hardcoded)
	orderedCats := orderCategoryLabels(categories)

	// 5) Build menuItems (hardcode "Marketplace"; then dynamic categories; then hardcode "Backups" and "Administration")
	menuItems := []any{
		map[string]any{
			"key":   "marketplace",
			"label": "Marketplace",
			"children": []any{
				map[string]any{
					"key":   "marketplace",
					"label": "Marketplace",
					"link":  "/openapi-ui/{clusterName}/{namespace}/factory/marketplace",
				},
			},
		},
	}

	for _, cat := range orderedCats {
		// Skip "Marketplace", "Backups", and "Administration" here since they're hardcoded
		if strings.EqualFold(cat, "Marketplace") || strings.EqualFold(cat, "Backups") || strings.EqualFold(cat, "Administration") {
			continue
		}
		children := []any{}
		for _, it := range categories[cat] {
			children = append(children, map[string]any{
				"key":   it.Key,
				"label": it.Label,
				"link":  it.Link,
			})
		}
		if len(children) > 0 {
			menuItems = append(menuItems, map[string]any{
				"key":      slugify(cat),
				"label":    cat,
				"children": children,
			})
		}
	}

	// Add hardcoded Backups section
	menuItems = append(menuItems, map[string]any{
		"key":   "backups",
		"label": "Backups",
		"children": []any{
			map[string]any{
				"key":   "plans",
				"label": "Plans",
				"link":  "/openapi-ui/{clusterName}/{namespace}/api-table/backups.cozystack.io/v1alpha1/plans",
			},
		},
	})

	// Add hardcoded Administration section
	menuItems = append(menuItems, map[string]any{
		"key":   "administration",
		"label": "Administration",
		"children": []any{
			map[string]any{
				"key":   "info",
				"label": "Info",
				"link":  "/openapi-ui/{clusterName}/{namespace}/factory/info-details/info",
			},
			map[string]any{
				"key":   "modules",
				"label": "Modules",
				"link":  "/openapi-ui/{clusterName}/{namespace}/api-table/core.cozystack.io/v1alpha1/tenantmodules",
			},
			map[string]any{
				"key":   "tenants",
				"label": "Tenants",
				"link":  "/openapi-ui/{clusterName}/{namespace}/api-table/apps.cozystack.io/v1alpha1/tenants",
			},
		},
	})

	// 6) Prepare the list of Sidebar IDs to upsert with the SAME content
	// Create sidebars for ALL CRDs with dashboard config
	targetIDs := []string{
		// stock-instance sidebars
		"stock-instance-api-form",
		"stock-instance-api-table",
		"stock-instance-builtin-form",
		"stock-instance-builtin-table",

		// stock-project sidebars
		"stock-project-factory-marketplace",
		"stock-project-factory-workloadmonitor-details",
		"stock-project-factory-kube-service-details",
		"stock-project-factory-kube-secret-details",
		"stock-project-factory-kube-ingress-details",
		"stock-project-factory-plan-details",
		"stock-project-api-form",
		"stock-project-api-table",
		"stock-project-builtin-form",
		"stock-project-builtin-table",
		"stock-project-crd-form",
		"stock-project-crd-table",
	}

	// Add details sidebars for all CRDs with dashboard config
	for i := range all {
		def := &all[i]
		if def.Spec.Dashboard == nil {
			continue
		}
		_, _, kind := pickGVK(def)
		lowerKind := strings.ToLower(kind)
		detailsID := fmt.Sprintf("stock-project-factory-%s-details", lowerKind)
		targetIDs = append(targetIDs, detailsID)
	}

	// 7) Upsert all target sidebars with identical menuItems and keysAndTags
	return m.upsertMultipleSidebars(ctx, crd, targetIDs, keysAndTags, menuItems)
}

// upsertMultipleSidebars creates/updates several Sidebar resources with the same menu spec.
func (m *Manager) upsertMultipleSidebars(
	ctx context.Context,
	crd *cozyv1alpha1.CozystackResourceDefinition,
	ids []string,
	keysAndTags map[string]any,
	menuItems []any,
) error {
	for _, id := range ids {
		spec := map[string]any{
			"id":          id,
			"keysAndTags": keysAndTags,
			"menuItems":   menuItems,
		}

		obj := &dashv1alpha1.Sidebar{}
		obj.SetName(id)

		if _, err := controllerutil.CreateOrUpdate(ctx, m.Client, obj, func() error {
			// Only set owner reference for dynamic sidebars (stock-project-factory-{kind}-details)
			// Static sidebars (stock-instance-*, stock-project-*) should not have owner references
			if strings.HasPrefix(id, "stock-project-factory-") && strings.HasSuffix(id, "-details") {
				// This is a dynamic sidebar, set owner reference only if it matches the current CRD
				_, _, kind := pickGVK(crd)
				lowerKind := strings.ToLower(kind)
				expectedID := fmt.Sprintf("stock-project-factory-%s-details", lowerKind)
				if id == expectedID {
					if err := controllerutil.SetOwnerReference(crd, obj, m.Scheme); err != nil {
						return err
					}
					// Add dashboard labels to dynamic resources
					m.addDashboardLabels(obj, crd, ResourceTypeDynamic)
				} else {
					// This is a different CRD's sidebar, don't modify owner references or labels
					// Just update the spec
				}
			} else {
				// This is a static sidebar, don't set owner references
				// Add static labels
				labels := obj.GetLabels()
				if labels == nil {
					labels = make(map[string]string)
				}
				labels[LabelManagedBy] = ManagedByValue
				labels[LabelResourceType] = ResourceTypeStatic
				obj.SetLabels(labels)
			}

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
		}); err != nil {
			return err
		}
	}
	return nil
}

// orderCategoryLabels returns category labels ordered strictly as:
//
//	Marketplace, IaaS, PaaS, NaaS, <others A→Z>, Resources, Administration.
//
// It only returns labels that exist in `cats` (except "Marketplace" which is hardcoded by caller).
func orderCategoryLabels[T any](cats map[string][]T) []string {
	if len(cats) == 0 {
		return []string{"Marketplace", "IaaS", "PaaS", "NaaS", "Resources", "Administration"}
	}

	head := []string{"Marketplace", "IaaS", "PaaS", "NaaS"}
	tail := []string{"Resources", "Administration"}

	present := make(map[string]struct{}, len(cats))
	for k := range cats {
		present[k] = struct{}{}
	}

	var result []string

	// Add head anchors (keep "Marketplace" in the order signature for the caller)
	for _, h := range head {
		result = append(result, h)
		delete(present, h)
	}

	// Collect "others": exclude tail
	var others []string
	for k := range present {
		if k == "Resources" || k == "Administration" {
			continue
		}
		others = append(others, k)
	}
	sort.Slice(others, func(i, j int) bool { return strings.ToLower(others[i]) < strings.ToLower(others[j]) })

	// Append others, then tail (always in fixed order)
	result = append(result, others...)
	result = append(result, tail...)

	return result
}

// safeCategory returns spec.dashboard.category or "Resources" if not set.
func safeCategory(def *cozyv1alpha1.CozystackResourceDefinition) string {
	if def == nil || def.Spec.Dashboard == nil {
		return "Resources"
	}
	if def.Spec.Dashboard.Category != "" {
		return def.Spec.Dashboard.Category
	}
	return "Resources"
}

// slugify converts a category label to a key-friendly identifier.
// "User Management" -> "usermanagement", "PaaS" -> "paas".
func slugify(s string) string {
	s = strings.TrimSpace(strings.ToLower(s))
	out := make([]byte, 0, len(s))
	for i := 0; i < len(s); i++ {
		c := s[i]
		if (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') {
			out = append(out, c)
		}
	}
	return string(out)
}
