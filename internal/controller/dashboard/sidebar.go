package dashboard

import (
	"context"
	"fmt"
	"sort"
	"strings"

	cozyv1alpha1 "github.com/cozystack/cozystack/api/v1alpha1"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
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
//     Marketplace, IaaS, PaaS, NaaS, <others A→Z>, Resources, Administration
//   - Items within each category: sort by Weight (desc), then Label (A→Z).
func (m *Manager) ensureSidebar(ctx context.Context, crd *cozyv1alpha1.CozystackResourceDefinition) error {
	// Build the full menu once.

	// 1) Fetch all CRDs
	var all []cozyv1alpha1.CozystackResourceDefinition
	if m.crdListFn != nil {
		s, err := m.crdListFn(ctx)
		if err != nil {
			return err
		}
		all = s
	} else {
		var crdList cozyv1alpha1.CozystackResourceDefinitionList
		if err := m.client.List(ctx, &crdList, &client.ListOptions{}); err != nil {
			return err
		}
		all = crdList.Items
	}

	// 2) Build category -> []item map (only for CRDs with spec.dashboard != nil)
	type item struct {
		Key    string
		Label  string
		Link   string
		Weight int
	}
	categories := map[string][]item{} // category label -> children
	keysAndTags := map[string]any{}   // plural -> []string{ "<lower(kind)>-sidebar" }

	for i := range all {
		def := &all[i]

		// Include ONLY when spec.dashboard != nil
		if def.Spec.Dashboard == nil {
			continue
		}

		g, v, kind := pickGVK(def)
		plural := pickPlural(kind, def)
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

		// keysAndTags: plural -> [ "<lower(kind)>-sidebar" ]
		keysAndTags[plural] = []any{fmt.Sprintf("%s-sidebar", strings.ToLower(kind))}
	}

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
	//    Marketplace (hardcoded), IaaS, PaaS, NaaS, <others A→Z>, Resources, Administration
	orderedCats := orderCategoryLabels(categories)

	// 5) Build menuItems (hardcode "Marketplace"; then dynamic categories)
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
		// Skip "Marketplace" here since it's already hardcoded
		if strings.EqualFold(cat, "Marketplace") {
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

	// 6) Prepare the list of Sidebar IDs to upsert with the SAME content
	_, _, thisKind := pickGVK(crd)
	lowerThisKind := strings.ToLower(thisKind)
	detailsID := fmt.Sprintf("stock-project-factory-%s-details", lowerThisKind)

	targetIDs := []string{
		// original details sidebar
		detailsID,

		// stock-instance sidebars
		"stock-instance-api-form",
		"stock-instance-api-table",
		"stock-instance-builtin-form",
		"stock-instance-builtin-table",

		// stock-project sidebars
		"stock-project-factory-marketplace",
		"stock-project-api-form",
		"stock-project-api-table",
		"stock-project-builtin-form",
		"stock-project-builtin-table",
		"stock-project-crd-form",
		"stock-project-crd-table",
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

		obj := &unstructured.Unstructured{}
		obj.SetGroupVersionKind(schema.GroupVersionKind{
			Group:   "dashboard.cozystack.io",
			Version: "v1alpha1",
			Kind:    "Sidebar",
		})
		obj.SetName(id)

		if _, err := controllerutil.CreateOrUpdate(ctx, m.client, obj, func() error {
			if err := controllerutil.SetOwnerReference(crd, obj, m.scheme); err != nil {
				return err
			}
			return unstructured.SetNestedField(obj.Object, spec, "spec")
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
