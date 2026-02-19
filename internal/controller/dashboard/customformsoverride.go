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
)

// ensureCustomFormsOverride creates or updates a CustomFormsOverride resource for the given CRD
func (m *Manager) ensureCustomFormsOverride(ctx context.Context, crd *cozyv1alpha1.ApplicationDefinition) error {
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

	// Parse OpenAPI schema once for reuse
	l := log.FromContext(ctx)
	openAPIProps := parseOpenAPIProperties(crd.Spec.Application.OpenAPISchema)

	// Build schema with multilineString for string fields without enum
	schema, err := buildMultilineStringSchema(crd.Spec.Application.OpenAPISchema)
	if err != nil {
		// If schema parsing fails, log the error and use an empty schema
		l.Error(err, "failed to build multiline string schema, using empty schema", "crd", crd.Name)
		schema = map[string]any{}
	}

	// Override specific fields with API-backed dropdowns (listInput type)
	applyListInputOverrides(schema, kind, openAPIProps)

	spec := map[string]any{
		"customizationId": customizationID,
		"hidden":          hidden,
		"sort":            sort,
		"schema":          schema,
		"strategy":        "merge",
	}

	_, err = controllerutil.CreateOrUpdate(ctx, m.Client, obj, func() error {
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

// ensureCFOMapping updates the CFOMapping resource to include a mapping for the given CRD
func (m *Manager) ensureCFOMapping(ctx context.Context, crd *cozyv1alpha1.ApplicationDefinition) error {
	g, v, kind := pickGVK(crd)
	plural := pickPlural(kind, crd)

	resourcePath := fmt.Sprintf("/%s/%s/%s", g, v, plural)
	customizationID := fmt.Sprintf("default-%s", resourcePath)

	obj := &dashv1alpha1.CFOMapping{}
	obj.SetName("cfomapping")

	_, err := controllerutil.CreateOrUpdate(ctx, m.Client, obj, func() error {
		// Parse existing mappings
		mappings := make(map[string]string)
		if obj.Spec.JSON.Raw != nil {
			var spec map[string]any
			if err := json.Unmarshal(obj.Spec.JSON.Raw, &spec); err == nil {
				if m, ok := spec["mappings"].(map[string]any); ok {
					for k, val := range m {
						if s, ok := val.(string); ok {
							mappings[k] = s
						}
					}
				}
			}
		}

		// Add/update the mapping for this CRD
		mappings[resourcePath] = customizationID

		specData := map[string]any{
			"mappings": mappings,
		}
		b, err := json.Marshal(specData)
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

// buildMultilineStringSchema parses OpenAPI schema and creates schema with multilineString
// for all string fields inside spec that don't have enum
func buildMultilineStringSchema(openAPISchema string) (map[string]any, error) {
	if openAPISchema == "" {
		return map[string]any{}, nil
	}

	var root map[string]any
	if err := json.Unmarshal([]byte(openAPISchema), &root); err != nil {
		return nil, fmt.Errorf("cannot parse openAPISchema: %w", err)
	}

	props, _ := root["properties"].(map[string]any)
	if props == nil {
		return map[string]any{}, nil
	}

	schema := map[string]any{
		"properties": map[string]any{},
	}

	// Check if there's a spec property
	specProp, ok := props["spec"].(map[string]any)
	if !ok {
		return map[string]any{}, nil
	}

	specProps, ok := specProp["properties"].(map[string]any)
	if !ok {
		return map[string]any{}, nil
	}

	// Create spec.properties structure in schema
	schemaProps := schema["properties"].(map[string]any)
	specSchema := map[string]any{
		"properties": map[string]any{},
	}
	schemaProps["spec"] = specSchema

	// Process spec properties recursively
	processSpecProperties(specProps, specSchema["properties"].(map[string]any))

	return schema, nil
}

// applyListInputOverrides injects listInput type overrides into the schema
// for fields that should be rendered as API-backed dropdowns in the dashboard.
// openAPIProps are the parsed top-level properties from the OpenAPI schema.
func applyListInputOverrides(schema map[string]any, kind string, openAPIProps map[string]any) {
	switch kind {
	case "VMInstance":
		specProps := ensureSchemaPath(schema, "spec")
		field := map[string]any{
			"type": "listInput",
			"customProps": map[string]any{
				"valueUri":    "/api/clusters/{cluster}/k8s/apis/instancetype.kubevirt.io/v1beta1/virtualmachineclusterinstancetypes",
				"keysToValue": []any{"metadata", "name"},
				"keysToLabel": []any{"metadata", "name"},
			},
		}
		if prop, _ := openAPIProps["instanceType"].(map[string]any); prop != nil {
			if def := prop["default"]; def != nil {
				field["default"] = def
			}
		}
		specProps["instanceType"] = field

		// Override disks[].name to be an API-backed dropdown listing VMDisk resources
		disksItemProps := ensureArrayItemProps(specProps, "disks")
		disksItemProps["name"] = map[string]any{
			"type": "listInput",
			"customProps": map[string]any{
				"valueUri":    "/api/clusters/{cluster}/k8s/apis/apps.cozystack.io/v1alpha1/namespaces/{namespace}/vmdisks",
				"keysToValue": []any{"metadata", "name"},
				"keysToLabel": []any{"metadata", "name"},
			},
		}
	}
}

// ensureArrayItemProps ensures that parentProps[fieldName].items.properties exists
// and returns the items properties map. Used for overriding fields inside array items.
func ensureArrayItemProps(parentProps map[string]any, fieldName string) map[string]any {
	field, ok := parentProps[fieldName].(map[string]any)
	if !ok {
		field = map[string]any{}
		parentProps[fieldName] = field
	}
	items, ok := field["items"].(map[string]any)
	if !ok {
		items = map[string]any{}
		field["items"] = items
	}
	props, ok := items["properties"].(map[string]any)
	if !ok {
		props = map[string]any{}
		items["properties"] = props
	}
	return props
}

// parseOpenAPIProperties parses the top-level properties from an OpenAPI schema JSON string.
func parseOpenAPIProperties(openAPISchema string) map[string]any {
	if openAPISchema == "" {
		return nil
	}
	var root map[string]any
	if err := json.Unmarshal([]byte(openAPISchema), &root); err != nil {
		return nil
	}
	props, _ := root["properties"].(map[string]any)
	return props
}

// ensureSchemaPath ensures the nested properties structure exists in a schema
// and returns the innermost properties map.
// e.g. ensureSchemaPath(schema, "spec") returns schema["properties"]["spec"]["properties"]
func ensureSchemaPath(schema map[string]any, segments ...string) map[string]any {
	current := schema
	for _, seg := range segments {
		props, ok := current["properties"].(map[string]any)
		if !ok {
			props = map[string]any{}
			current["properties"] = props
		}
		child, ok := props[seg].(map[string]any)
		if !ok {
			child = map[string]any{}
			props[seg] = child
		}
		current = child
	}
	props, ok := current["properties"].(map[string]any)
	if !ok {
		props = map[string]any{}
		current["properties"] = props
	}
	return props
}

// processSpecProperties recursively processes spec properties and adds multilineString type
// for string fields without enum
func processSpecProperties(props map[string]any, schemaProps map[string]any) {
	for pname, raw := range props {
		sub, ok := raw.(map[string]any)
		if !ok {
			continue
		}

		typ, _ := sub["type"].(string)

		switch typ {
		case "string":
			// Check if this string field has enum
			if !hasEnum(sub) {
				// Add multilineString type for this field
				if schemaProps[pname] == nil {
					schemaProps[pname] = map[string]any{}
				}
				fieldSchema := schemaProps[pname].(map[string]any)
				fieldSchema["type"] = "multilineString"
			}
		case "object":
			// Recursively process nested objects
			if childProps, ok := sub["properties"].(map[string]any); ok {
				fieldSchema, ok := schemaProps[pname].(map[string]any)
				if !ok {
					fieldSchema = map[string]any{}
					schemaProps[pname] = fieldSchema
				}
				nestedSchemaProps, ok := fieldSchema["properties"].(map[string]any)
				if !ok {
					nestedSchemaProps = map[string]any{}
					fieldSchema["properties"] = nestedSchemaProps
				}
				processSpecProperties(childProps, nestedSchemaProps)
			}
		case "array":
			// Check if array items are objects with properties
			if items, ok := sub["items"].(map[string]any); ok {
				if itemProps, ok := items["properties"].(map[string]any); ok {
					// Create array item schema
					fieldSchema, ok := schemaProps[pname].(map[string]any)
					if !ok {
						fieldSchema = map[string]any{}
						schemaProps[pname] = fieldSchema
					}
					itemSchema, ok := fieldSchema["items"].(map[string]any)
					if !ok {
						itemSchema = map[string]any{}
						fieldSchema["items"] = itemSchema
					}
					itemSchemaProps, ok := itemSchema["properties"].(map[string]any)
					if !ok {
						itemSchemaProps = map[string]any{}
						itemSchema["properties"] = itemSchemaProps
					}
					processSpecProperties(itemProps, itemSchemaProps)
				}
			}
		}
	}
}
