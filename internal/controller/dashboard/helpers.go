package dashboard

import (
	"encoding/json"
	"fmt"
	"reflect"
	"sort"
	"strings"

	dashv1alpha1 "github.com/cozystack/cozystack/api/dashboard/v1alpha1"
	cozyv1alpha1 "github.com/cozystack/cozystack/api/v1alpha1"
)

// ---------------- Types used by OpenAPI parsing ----------------

type fieldInfo struct {
	JSONPathSpec string // dotted path under .spec (e.g., "systemDisk.image")
	Label        string // "System Disk / Image" or "systemDisk.image"
	Description  string
}

// ---------------- Public entry: ensure Factory ------------------

// pickGVK tries to read group/version/kind from the CRD. We prefer the "application" section,
// falling back to other likely fields if your schema differs.
func pickGVK(crd *cozyv1alpha1.CozystackResourceDefinition) (group, version, kind string) {
	// Best guess based on your examples:
	if crd.Spec.Application.Kind != "" {
		kind = crd.Spec.Application.Kind
	}

	// For applications, always use apps.cozystack.io group, not the CRD's own group
	group = "apps.cozystack.io"
	version = "v1alpha1"

	// Reasonable fallbacks if any are empty:
	if kind == "" {
		kind = "Resource"
	}
	return
}

// pickPlural prefers a field on the CRD if you have it; otherwise do a simple lowercase + "s".
func pickPlural(kind string, crd *cozyv1alpha1.CozystackResourceDefinition) string {
	// If you have crd.Spec.Application.Plural, prefer it. Example:
	if crd.Spec.Application.Plural != "" {
		return crd.Spec.Application.Plural
	}
	// naive pluralization
	k := strings.ToLower(kind)
	if strings.HasSuffix(k, "s") {
		return k
	}
	return k + "s"
}

// ----------------------- Helpers (OpenAPI → values) -----------------------

// defaultOrZero returns the schema default if present; otherwise a reasonable zero value.
func defaultOrZero(sub map[string]interface{}) interface{} {
	if v, ok := sub["default"]; ok {
		return v
	}
	typ, _ := sub["type"].(string)
	switch typ {
	case "string":
		return ""
	case "boolean":
		return false
	case "array":
		return []interface{}{}
	case "integer", "number":
		return 0
	case "object":
		return map[string]interface{}{}
	default:
		return nil
	}
}

// toIfaceSlice converts []string -> []interface{}.
func toIfaceSlice(ss []string) []interface{} {
	out := make([]interface{}, len(ss))
	for i, s := range ss {
		out[i] = s
	}
	return out
}

// buildPrefillValues converts an OpenAPI schema (JSON string) into a []interface{} "values" list
// suitable for CustomFormsPrefill.spec.values.
// Rules:
//   - For top-level primitive/array fields: emit an entry, using default if present, otherwise zero.
//   - For top-level objects: recursively process nested objects and emit entries for all default values
//     found at any nesting level.
func buildPrefillValues(openAPISchema string) ([]interface{}, error) {
	var root map[string]interface{}
	if err := json.Unmarshal([]byte(openAPISchema), &root); err != nil {
		return nil, fmt.Errorf("cannot parse openAPISchema: %w", err)
	}
	props, _ := root["properties"].(map[string]interface{})
	if props == nil {
		return []interface{}{}, nil
	}

	var values []interface{}
	processSchemaProperties(props, []string{"spec"}, &values, true)
	return values, nil
}

// processSchemaProperties recursively processes OpenAPI schema properties and extracts default values
func processSchemaProperties(props map[string]interface{}, path []string, values *[]interface{}, topLevel bool) {
	for pname, raw := range props {
		sub, _ := raw.(map[string]interface{})
		if sub == nil {
			continue
		}

		typ, _ := sub["type"].(string)
		currentPath := append(path, pname)

		switch typ {
		case "object":
			// Check if this object has a default value
			if objDefault, ok := sub["default"].(map[string]interface{}); ok {
				// Process the default object recursively
				processDefaultObject(objDefault, currentPath, values)
			}

			// Also process child properties for their individual defaults
			if childProps, ok := sub["properties"].(map[string]interface{}); ok {
				processSchemaProperties(childProps, currentPath, values, false)
			}
		default:
			// For primitive types, use default if present, otherwise zero value
			val := defaultOrZero(sub)
			// Only emit zero-value entries when at top level
			if val != nil || topLevel {
				entry := map[string]interface{}{
					"path":  toIfaceSlice(currentPath),
					"value": val,
				}
				*values = append(*values, entry)
			}
		}
	}
}

// processDefaultObject recursively processes a default object and creates entries for all nested values
func processDefaultObject(obj map[string]interface{}, path []string, values *[]interface{}) {
	for key, value := range obj {
		currentPath := append(path, key)

		// If the value is a map, process it recursively
		if nestedObj, ok := value.(map[string]interface{}); ok {
			processDefaultObject(nestedObj, currentPath, values)
		} else {
			// For primitive values, create an entry
			entry := map[string]interface{}{
				"path":  toIfaceSlice(currentPath),
				"value": value,
			}
			*values = append(*values, entry)
		}
	}
}

// normalizeJSON makes maps/slices JSON-safe for k8s Unstructured:
// - converts all int/int32/... to float64
// - leaves strings, bools, nil as-is
func normalizeJSON(v any) any {
	switch t := v.(type) {
	case map[string]any:
		out := make(map[string]any, len(t))
		for k, val := range t {
			out[k] = normalizeJSON(val)
		}
		return out
	case []any:
		out := make([]any, len(t))
		for i := range t {
			out[i] = normalizeJSON(t[i])
		}
		return out
	case int:
		return float64(t)
	case int8:
		return float64(t)
	case int16:
		return float64(t)
	case int32:
		return float64(t)
	case int64:
		return float64(t)
	case uint, uint8, uint16, uint32, uint64:
		return float64(reflect.ValueOf(t).Convert(reflect.TypeOf(uint64(0))).Uint())
	case float32:
		return float64(t)
	default:
		return v
	}
}

// --- helpers for schema inspection ---

func isScalarType(n map[string]any) bool {
	switch getString(n, "type") {
	case "string", "integer", "number", "boolean":
		return true
	default:
		return false
	}
}

func isIntOrString(n map[string]any) bool {
	// Kubernetes extension: x-kubernetes-int-or-string: true
	if v, ok := n["x-kubernetes-int-or-string"]; ok {
		if b, ok := v.(bool); ok && b {
			return true
		}
	}
	// anyOf: integer|string
	if anyOf, ok := n["anyOf"].([]any); ok {
		hasInt := false
		hasStr := false
		for _, it := range anyOf {
			if m, ok := it.(map[string]any); ok {
				switch getString(m, "type") {
				case "integer":
					hasInt = true
				case "string":
					hasStr = true
				}
			}
		}
		return hasInt && hasStr
	}
	return false
}

func hasEnum(n map[string]any) bool {
	_, ok := n["enum"]
	return ok
}

func getString(n map[string]any, key string) string {
	if v, ok := n[key]; ok {
		if s, ok := v.(string); ok {
			return s
		}
	}
	return ""
}

// shouldExcludeParamPath returns true if any part of the path contains
// backup / bootstrap / password (case-insensitive)
func shouldExcludeParamPath(parts []string) bool {
	for _, p := range parts {
		lp := strings.ToLower(p)
		if strings.Contains(lp, "backup") || strings.Contains(lp, "bootstrap") || strings.Contains(lp, "password") || strings.Contains(lp, "cloudinit") {
			return true
		}
	}
	return false
}

func humanizePath(parts []string) string {
	// "systemDisk.image" -> "System Disk / Image"
	return strings.Join(parts, " / ")
}

// titleFromKindPlural returns a presentable plural label, e.g.:
// kind="VirtualMachine", plural="virtualmachines" => "VirtualMachines"
func titleFromKindPlural(kind, plural string) string {
	return kind + "s"
}

// The hidden lists below mirror the Helm templates you shared.
// Each entry is a path as nested string array, e.g. ["metadata","creationTimestamp"].

func hiddenMetadataSystem() []any {
	return []any{
		[]any{"metadata", "annotations"},
		[]any{"metadata", "labels"},
		[]any{"metadata", "namespace"},
		[]any{"metadata", "creationTimestamp"},
		[]any{"metadata", "deletionGracePeriodSeconds"},
		[]any{"metadata", "deletionTimestamp"},
		[]any{"metadata", "finalizers"},
		[]any{"metadata", "generateName"},
		[]any{"metadata", "generation"},
		[]any{"metadata", "managedFields"},
		[]any{"metadata", "ownerReferences"},
		[]any{"metadata", "resourceVersion"},
		[]any{"metadata", "selfLink"},
		[]any{"metadata", "uid"},
	}
}

func hiddenMetadataAPI() []any {
	return []any{
		[]any{"kind"},
		[]any{"apiVersion"},
		[]any{"appVersion"},
	}
}

func hiddenStatus() []any {
	return []any{
		[]any{"status"},
	}
}

// compareArbitrarySpecs compares two ArbitrarySpec objects by comparing their JSON content
func compareArbitrarySpecs(spec1, spec2 dashv1alpha1.ArbitrarySpec) bool {
	// If both are empty, they're equal
	if len(spec1.JSON.Raw) == 0 && len(spec2.JSON.Raw) == 0 {
		return true
	}

	// If one is empty and the other is not, they're different
	if len(spec1.JSON.Raw) == 0 || len(spec2.JSON.Raw) == 0 {
		return false
	}

	// Parse and normalize both specs
	norm1, err := normalizeJSONForComparison(spec1.JSON.Raw)
	if err != nil {
		return false
	}
	norm2, err := normalizeJSONForComparison(spec2.JSON.Raw)
	if err != nil {
		return false
	}

	// Compare normalized JSON
	equal := string(norm1) == string(norm2)

	return equal
}

// normalizeJSONForComparison normalizes JSON by sorting arrays and objects
func normalizeJSONForComparison(data []byte) ([]byte, error) {
	var obj interface{}
	if err := json.Unmarshal(data, &obj); err != nil {
		return nil, err
	}

	// Recursively normalize the object
	normalized := normalizeObject(obj)

	// Re-marshal to get normalized JSON
	return json.Marshal(normalized)
}

// normalizeObject recursively normalizes objects and arrays
func normalizeObject(obj interface{}) interface{} {
	switch v := obj.(type) {
	case map[string]interface{}:
		// For maps, we don't need to sort keys as json.Marshal handles that
		result := make(map[string]interface{})
		for k, val := range v {
			result[k] = normalizeObject(val)
		}
		return result
	case []interface{}:
		// For arrays, we need to sort them if they contain objects with comparable fields
		if len(v) == 0 {
			return v
		}

		// Check if this is an array of objects that can be sorted
		if canSortArray(v) {
			// Sort the array
			sorted := make([]interface{}, len(v))
			copy(sorted, v)
			sortArray(sorted)
			return sorted
		}

		// If we can't sort, just normalize each element
		result := make([]interface{}, len(v))
		for i, val := range v {
			result[i] = normalizeObject(val)
		}
		return result
	default:
		return v
	}
}

// canSortArray checks if an array can be sorted (contains objects with comparable fields)
func canSortArray(arr []interface{}) bool {
	if len(arr) == 0 {
		return false
	}

	// Check if all elements are objects
	for _, item := range arr {
		if _, ok := item.(map[string]interface{}); !ok {
			return false
		}
	}

	// Check if objects have comparable fields (like "path" for CustomFormsPrefill values)
	firstObj, ok := arr[0].(map[string]interface{})
	if !ok {
		return false
	}

	// Look for "path" field which is used in CustomFormsPrefill values
	if _, hasPath := firstObj["path"]; hasPath {
		return true
	}

	return false
}

// sortArray sorts an array of objects by their "path" field
func sortArray(arr []interface{}) {
	sort.Slice(arr, func(i, j int) bool {
		objI, okI := arr[i].(map[string]interface{})
		objJ, okJ := arr[j].(map[string]interface{})

		if !okI || !okJ {
			return false
		}

		pathI, hasPathI := objI["path"]
		pathJ, hasPathJ := objJ["path"]

		if !hasPathI || !hasPathJ {
			return false
		}

		// Convert paths to strings for comparison
		pathIStr := fmt.Sprintf("%v", pathI)
		pathJStr := fmt.Sprintf("%v", pathJ)

		return pathIStr < pathJStr
	})
}
