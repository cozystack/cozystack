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
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
)

// ensureFactory creates or updates a Factory resource for the given CRD
func (m *Manager) ensureFactory(ctx context.Context, crd *cozyv1alpha1.CozystackResourceDefinition) error {
	g, v, kind := pickGVK(crd)
	plural := pickPlural(kind, crd)

	lowerKind := strings.ToLower(kind)
	factoryName := fmt.Sprintf("%s-details", lowerKind)
	resourceFetch := fmt.Sprintf("/api/clusters/{2}/k8s/apis/%s/%s/namespaces/{3}/%s/{6}", g, v, plural)

	flags := factoryFeatureFlags(crd)

	var keysOrder [][]string
	if crd.Spec.Dashboard != nil {
		keysOrder = crd.Spec.Dashboard.KeysOrder
	}
	tabs := []any{
		detailsTab(kind, resourceFetch, crd.Spec.Application.OpenAPISchema, keysOrder),
	}
	if flags.Workloads {
		tabs = append(tabs, workloadsTab(kind))
	}
	if flags.Ingresses {
		tabs = append(tabs, ingressesTab(kind))
	}
	if flags.Services {
		tabs = append(tabs, servicesTab(kind))
	}
	if flags.Secrets {
		tabs = append(tabs, secretsTab(kind))
	}
	tabs = append(tabs, yamlTab(plural))

	// Use unified factory creation
	config := UnifiedResourceConfig{
		Name:         factoryName,
		ResourceType: "factory",
		Kind:         kind,
		Plural:       plural,
		Title:        strings.ToLower(plural),
	}

	spec := createUnifiedFactory(config, tabs, []any{resourceFetch})

	obj := &dashv1alpha1.Factory{}
	obj.SetName(factoryName)

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

// ---------------- Tabs builders ----------------

func detailsTab(kind, endpoint, schemaJSON string, keysOrder [][]string) map[string]any {
	paramsBlocks := buildOpenAPIParamsBlocks(schemaJSON, keysOrder)
	paramsList := map[string]any{
		"type": "antdFlex",
		"data": map[string]any{
			"id":       "params-list",
			"vertical": true,
			"gap":      float64(24),
		},
		"children": paramsBlocks,
	}

	leftColStack := []any{
		antdText("details-title", true, kind, map[string]any{
			"fontSize":     float64(20),
			"marginBottom": float64(12),
		}),
		antdFlexVertical("meta-name-block", 4, []any{
			antdText("meta-name-label", true, "Name", nil),
			parsedText("meta-name-value", "{reqsJsonPath[0]['.metadata.name']['-']}", nil),
		}),
		antdFlexVertical("meta-namespace-block", 8, []any{
			antdText("meta-namespace-label", true, "Namespace", nil),
			map[string]any{
				"type": "antdFlex",
				"data": map[string]any{
					"id":    "namespace-row",
					"align": "center",
					"gap":   float64(6),
				},
				"children": []any{
					createUnifiedBadgeFromKind("ns-badge", "Namespace"),
					antdLink("namespace-link",
						"{reqsJsonPath[0]['.metadata.namespace']['-']}",
						"/openapi-ui/{2}/{reqsJsonPath[0]['.metadata.namespace']['-']}/factory/marketplace",
					),
				},
			},
		}),
		antdFlexVertical("meta-created-block", 4, []any{
			antdText("time-label", true, "Created", nil),
			antdFlex("time-block", 6, []any{
				antdText("time-icon", false, "🌐", nil),
				parsedTextWithFormatter("time-value", "{reqsJsonPath[0]['.metadata.creationTimestamp']['-']}", "timestamp"),
			}),
		}),
		antdFlexVertical("meta-version-block", 4, []any{
			antdText("version-label", true, "Version", nil),
			parsedText("version-value", "{reqsJsonPath[0]['.status.version']['-']}", nil),
		}),
		antdFlexVertical("meta-released-block", 4, []any{
			antdText("released-label", true, "Released", nil),
			parsedText("released-value", "{reqsJsonPath[0]['.status.conditions[?(@.type==\"Released\")].status']['-']}", nil),
		}),
		antdFlexVertical("meta-ready-block", 4, []any{
			antdText("ready-label", true, "Ready", nil),
			parsedText("ready-value", "{reqsJsonPath[0]['.status.conditions[?(@.type==\"Ready\")].status']['-']}", nil),
		}),
	}

	rightColStack := []any{
		antdText("params-title", true, "Parameters", map[string]any{
			"fontSize":     float64(20),
			"marginBottom": float64(12),
		}),
		paramsList,
	}

	return map[string]any{
		"key":   "details",
		"label": "Details",
		"children": []any{
			contentCard("details-card", map[string]any{"marginBottom": float64(24)}, []any{
				map[string]any{
					"type": "antdRow",
					"data": map[string]any{
						"id":     "details-grid",
						"gutter": []any{float64(48), float64(12)},
					},
					"children": []any{
						map[string]any{
							"type": "antdCol",
							"data": map[string]any{"id": "col-left", "span": float64(12)},
							"children": []any{
								map[string]any{
									"type":     "antdFlex",
									"data":     map[string]any{"id": "col-left-stack", "vertical": true, "gap": float64(24)},
									"children": leftColStack,
								},
							},
						},
						map[string]any{
							"type": "antdCol",
							"data": map[string]any{"id": "col-right", "span": float64(12)},
							"children": []any{
								map[string]any{
									"type":     "antdFlex",
									"data":     map[string]any{"id": "col-right-stack", "vertical": true, "gap": float64(24)},
									"children": rightColStack,
								},
							},
						},
					},
				},
				spacer("conditions-top-spacer", float64(16)),
				antdText("conditions-title", true, "Conditions", map[string]any{"fontSize": float64(20)}),
				spacer("conditions-spacer", float64(8)),
				map[string]any{
					"type": "EnrichedTable",
					"data": map[string]any{
						"id":                   "conditions-table",
						"fetchUrl":             endpoint,
						"clusterNamePartOfUrl": "{2}",
						"customizationId":      "factory-status-conditions",
						"baseprefix":           "/openapi-ui",
						"withoutControls":      true,
						"pathToItems":          []any{"status", "conditions"},
					},
				},
			}),
		},
	}
}

func workloadsTab(kind string) map[string]any {
	return map[string]any{
		"key":   "workloads",
		"label": "Workloads",
		"children": []any{
			map[string]any{
				"type": "EnrichedTable",
				"data": map[string]any{
					"id":                   "workloads-table",
					"fetchUrl":             "/api/clusters/{2}/k8s/apis/cozystack.io/v1alpha1/namespaces/{3}/workloadmonitors",
					"clusterNamePartOfUrl": "{2}",
					"baseprefix":           "/openapi-ui",
					"customizationId":      "factory-details-v1alpha1.cozystack.io.workloadmonitors",
					"pathToItems":          []any{"items"},
					"labelSelector": map[string]any{
						"apps.cozystack.io/application.group": "apps.cozystack.io",
						"apps.cozystack.io/application.kind":  kind,
						"apps.cozystack.io/application.name":  "{reqs[0]['metadata','name']}",
					},
				},
			},
		},
	}
}

func servicesTab(kind string) map[string]any {
	return map[string]any{
		"key":   "services",
		"label": "Services",
		"children": []any{
			map[string]any{
				"type": "EnrichedTable",
				"data": map[string]any{
					"id":                   "services-table",
					"fetchUrl":             "/api/clusters/{2}/k8s/api/v1/namespaces/{3}/services",
					"clusterNamePartOfUrl": "{2}",
					"baseprefix":           "/openapi-ui",
					"customizationId":      "factory-details-v1.services",
					"pathToItems":          []any{"items"},
					"labelSelector": map[string]any{
						"apps.cozystack.io/application.group":  "apps.cozystack.io",
						"apps.cozystack.io/application.kind":   kind,
						"apps.cozystack.io/application.name":   "{reqs[0]['metadata','name']}",
						"internal.cozystack.io/tenantresource": "true",
					},
				},
			},
		},
	}
}

func ingressesTab(kind string) map[string]any {
	return map[string]any{
		"key":   "ingresses",
		"label": "Ingresses",
		"children": []any{
			map[string]any{
				"type": "EnrichedTable",
				"data": map[string]any{
					"id":                   "ingresses-table",
					"fetchUrl":             "/api/clusters/{2}/k8s/apis/networking.k8s.io/v1/namespaces/{3}/ingresses",
					"clusterNamePartOfUrl": "{2}",
					"baseprefix":           "/openapi-ui",
					"customizationId":      "factory-details-networking.k8s.io.v1.ingresses",
					"pathToItems":          []any{"items"},
					"labelSelector": map[string]any{
						"apps.cozystack.io/application.group":  "apps.cozystack.io",
						"apps.cozystack.io/application.kind":   kind,
						"apps.cozystack.io/application.name":   "{reqs[0]['metadata','name']}",
						"internal.cozystack.io/tenantresource": "true",
					},
				},
			},
		},
	}
}

func secretsTab(kind string) map[string]any {
	return map[string]any{
		"key":   "secrets",
		"label": "Secrets",
		"children": []any{
			map[string]any{
				"type": "EnrichedTable",
				"data": map[string]any{
					"id":                   "secrets-table",
					"fetchUrl":             "/api/clusters/{2}/k8s/apis/core.cozystack.io/v1alpha1/namespaces/{3}/tenantsecrets",
					"clusterNamePartOfUrl": "{2}",
					"baseprefix":           "/openapi-ui",
					"customizationId":      "factory-details-v1alpha1.core.cozystack.io.tenantsecrets",
					"pathToItems":          []any{"items"},
					"labelSelector": map[string]any{
						"apps.cozystack.io/application.group": "apps.cozystack.io",
						"apps.cozystack.io/application.kind":  kind,
						"apps.cozystack.io/application.name":  "{reqs[0]['metadata','name']}",
					},
				},
			},
		},
	}
}

func yamlTab(plural string) map[string]any {
	return map[string]any{
		"key":   "yaml",
		"label": "YAML",
		"children": []any{
			map[string]any{
				"type": "YamlEditorSingleton",
				"data": map[string]any{
					"id":                        "yaml-editor",
					"cluster":                   "{2}",
					"isNameSpaced":              true,
					"type":                      "builtin",
					"typeName":                  plural,
					"prefillValuesRequestIndex": float64(0),
					"readOnly":                  true,
					"substractHeight":           float64(400),
				},
			},
		},
	}
}

// ---------------- OpenAPI → Right column ----------------

func buildOpenAPIParamsBlocks(schemaJSON string, keysOrder [][]string) []any {
	var blocks []any
	fields := collectOpenAPILeafFields(schemaJSON, 2, 20)

	// Sort fields according to keysOrder if provided
	if len(keysOrder) > 0 {
		fields = sortFieldsByKeysOrder(fields, keysOrder)
	}

	for idx, f := range fields {
		id := fmt.Sprintf("param-%d", idx)
		blocks = append(blocks,
			antdFlexVertical(id, 4, []any{
				antdText(id+"-label", true, f.Label, nil),
				parsedText(id+"-value", fmt.Sprintf("{reqsJsonPath[0]['.spec.%s']['-']}", f.JSONPathSpec), nil),
			}),
		)
	}
	if len(fields) == 0 {
		blocks = append(blocks,
			antdText("params-empty", false, "No scalar parameters detected in schema (see YAML tab for full spec).", map[string]any{"opacity": float64(0.7)}),
		)
	}
	return blocks
}

// sortFieldsByKeysOrder sorts fields according to the provided keysOrder
func sortFieldsByKeysOrder(fields []fieldInfo, keysOrder [][]string) []fieldInfo {
	// Create a map for quick lookup of field positions
	orderMap := make(map[string]int)
	for i, path := range keysOrder {
		// Convert path to dot notation (e.g., ["spec", "systemDisk", "image"] -> "systemDisk.image")
		if len(path) > 1 && path[0] == "spec" {
			dotPath := strings.Join(path[1:], ".")
			orderMap[dotPath] = i
		}
	}

	// Sort fields based on their position in keysOrder
	sort.Slice(fields, func(i, j int) bool {
		posI, existsI := orderMap[fields[i].JSONPathSpec]
		posJ, existsJ := orderMap[fields[j].JSONPathSpec]

		// If both exist in orderMap, sort by position
		if existsI && existsJ {
			return posI < posJ
		}
		// If only one exists, prioritize the one that exists
		if existsI {
			return true
		}
		if existsJ {
			return false
		}
		// If neither exists, maintain original order (stable sort)
		return i < j
	})

	return fields
}

func collectOpenAPILeafFields(schemaJSON string, maxDepth, maxFields int) []fieldInfo {
	type node = map[string]any

	if strings.TrimSpace(schemaJSON) == "" {
		return nil
	}

	var root any
	if err := json.Unmarshal([]byte(schemaJSON), &root); err != nil {
		// invalid JSON — skip
		return nil
	}

	props := map[string]any{}
	if m, ok := root.(node); ok {
		if p, ok := m["properties"].(node); ok {
			props = p
		}
	}
	if len(props) == 0 {
		return nil
	}

	var out []fieldInfo
	var visit func(prefix []string, n node, depth int)

	addField := func(path []string, schema node) {
		// Skip excluded paths (backup/bootstrap/password)
		if shouldExcludeParamPath(path) {
			return
		}
		// build label "Foo Bar / Baz"
		label := humanizePath(path)
		desc := getString(schema, "description")
		out = append(out, fieldInfo{
			JSONPathSpec: strings.Join(path, "."),
			Label:        label,
			Description:  desc,
		})
	}

	visit = func(prefix []string, n node, depth int) {
		if len(out) >= maxFields {
			return
		}
		// Scalar?
		if isScalarType(n) || isIntOrString(n) || hasEnum(n) {
			addField(prefix, n)
			return
		}
		// Object with properties
		if props, ok := n["properties"].(node); ok {
			if depth >= maxDepth {
				// too deep — stop
				return
			}
			// deterministic ordering
			keys := make([]string, 0, len(props))
			for k := range props {
				keys = append(keys, k)
			}
			sort.Strings(keys)
			for _, k := range keys {
				child, _ := props[k].(node)
				visit(append(prefix, k), child, depth+1)
				if len(out) >= maxFields {
					return
				}
			}
			return
		}
		// Arrays: try to render item if it's scalar and depth limit allows
		if n["type"] == "array" {
			if items, ok := n["items"].(node); ok && (isScalarType(items) || isIntOrString(items) || hasEnum(items)) {
				addField(prefix, items)
			}
			return
		}
		// Otherwise skip (unknown/complex)
	}

	// top-level: iterate properties
	keys := make([]string, 0, len(props))
	for k := range props {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		if child, ok := props[k].(node); ok {
			visit([]string{k}, child, 1)
			if len(out) >= maxFields {
				break
			}
		}
	}
	return out
}

// ---------------- Feature flags ----------------

type factoryFlags struct {
	Workloads bool
	Ingresses bool
	Services  bool
	Secrets   bool
}

// factoryFeatureFlags tries several conventional locations so you can evolve the API
// without breaking the controller. Defaults are false (hidden).
func factoryFeatureFlags(crd *cozyv1alpha1.CozystackResourceDefinition) factoryFlags {
	var f factoryFlags

	f.Workloads = true
	f.Ingresses = true
	f.Services = true
	f.Secrets = true

	return f
}
