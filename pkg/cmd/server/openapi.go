package server

import (
	"encoding/json"
	"fmt"
	"strings"

	"k8s.io/kube-openapi/pkg/spec3"
	"k8s.io/kube-openapi/pkg/validation/spec"
)

// -----------------------------------------------------------------------------
// shared helpers
// -----------------------------------------------------------------------------

const (
	apiPrefix     = "com.github.cozystack.cozystack.pkg.apis.apps.v1alpha1"
	baseRef       = apiPrefix + ".Application"
	baseListRef   = apiPrefix + ".ApplicationList"
	baseStatusRef = apiPrefix + ".ApplicationStatus"
	smp           = "application/strategic-merge-patch+json"

	// objectMetaRef is the OpenAPI reference for Kubernetes ObjectMeta.
	objectMetaRef = "k8s.io/apimachinery/pkg/apis/meta/v1.ObjectMeta"
	// applicationNamePattern is DNS-1035 regex for Application names.
	applicationNamePattern = `^[a-z]([-a-z0-9]*[a-z0-9])?$`
	// applicationNameMaxLength is the maximum length for Application names.
	applicationNameMaxLength = 40
)

// patchObjectMetaNameValidation adds DNS-1035 validation to metadata.name.
// This ensures UI form validation matches backend validation.
func patchObjectMetaNameValidation(schemas map[string]*spec.Schema) {
	objMeta, ok := schemas[objectMetaRef]
	if !ok || objMeta == nil {
		return
	}

	if name, ok := objMeta.Properties["name"]; ok {
		name.Pattern = applicationNamePattern
		maxLen := int64(applicationNameMaxLength)
		name.MaxLength = &maxLen
		objMeta.Properties["name"] = name
	}
}

// patchObjectMetaNameValidationV2 adds DNS-1035 validation for Swagger v2.
func patchObjectMetaNameValidationV2(defs map[string]spec.Schema) {
	objMeta, ok := defs[objectMetaRef]
	if !ok {
		return
	}

	if name, ok := objMeta.Properties["name"]; ok {
		name.Pattern = applicationNamePattern
		maxLen := int64(applicationNameMaxLength)
		name.MaxLength = &maxLen
		objMeta.Properties["name"] = name
	}
	defs[objectMetaRef] = objMeta
}

// deepCopySchema clones *spec.Schema via JSON-marshal/unmarshal.
func deepCopySchema(in *spec.Schema) *spec.Schema {
	if in == nil {
		return nil
	}
	raw, err := json.Marshal(in)
	if err != nil {
		panic(fmt.Errorf("failed to marshal schema: %w", err))
	}
	var out spec.Schema
	err = json.Unmarshal(raw, &out)
	if err != nil {
		panic(fmt.Errorf("failed to unmarshal schema: %w", err))
	}
	return &out
}

// findSpecContainer returns first object owning ".spec".
func findSpecContainer(s *spec.Schema) *spec.Schema {
	if s == nil {
		return nil
	}
	if len(s.Type) > 0 && s.Type.Contains("object") {
		if _, ok := s.Properties["spec"]; ok {
			return s
		}
	}
	for _, branch := range [][]spec.Schema{s.AllOf, s.OneOf, s.AnyOf} {
		for i := range branch {
			if res := findSpecContainer(&branch[i]); res != nil {
				return res
			}
		}
	}
	return nil
}

// patchSpec injects/overrides ".spec" with user JSON (or schemaless object).
func patchSpec(target *spec.Schema, raw string) error {
	if strings.TrimSpace(raw) == "" {
		if target.Properties == nil {
			target.Properties = map[string]spec.Schema{}
		}
		prop := target.Properties["spec"]
		prop.AdditionalProperties = &spec.SchemaOrBool{Allows: true}
		target.Properties["spec"] = prop
		return nil
	}

	var custom spec.Schema
	if err := json.Unmarshal([]byte(raw), &custom); err != nil {
		return err
	}
	if custom.AdditionalProperties == nil {
		custom.AdditionalProperties = &spec.SchemaOrBool{Allows: true}
	}
	if target.Properties == nil {
		target.Properties = map[string]spec.Schema{}
	}
	target.Properties["spec"] = custom
	return nil
}

/* ────────────────────────────────────────────────────────────────────────── */
/*  DRY helpers                                                             */
/* ────────────────────────────────────────────────────────────────────────── */

// cloneKindSchemas: from base schemas, create new schemas for a specific kind.
func cloneKindSchemas(kind string, base, baseStatus, baseList *spec.Schema, v3 bool) (obj, status, list *spec.Schema) {
	obj = deepCopySchema(base)
	status = deepCopySchema(baseStatus)
	list = deepCopySchema(baseList)

	// Ensure we have valid clones
	if obj == nil || status == nil || list == nil {
		return nil, nil, nil
	}

	// GVK-extensions
	setGVK := func(s *spec.Schema, k string) {
		s.Extensions = map[string]interface{}{
			"x-kubernetes-group-version-kind": []interface{}{
				map[string]interface{}{"group": "apps.cozystack.io", "version": "v1alpha1", "kind": k},
			},
		}
	}
	setGVK(obj, kind)
	setGVK(list, kind+"List")

	// fix refs
	refPrefix := "#/components/schemas/" // v3
	if !v3 {
		refPrefix = "#/definitions/"
	}
	statusRef := refPrefix + apiPrefix + "." + kind + "Status"
	itemRef := refPrefix + apiPrefix + "." + kind

	if prop, ok := obj.Properties["status"]; ok {
		prop.Ref = spec.MustCreateRef(statusRef)
		obj.Properties["status"] = prop
	}
	if list.Properties != nil {
		if items := list.Properties["items"]; items.Items != nil && items.Items.Schema != nil {
			items.Items.Schema.Ref = spec.MustCreateRef(itemRef)
			list.Properties["items"] = items
		}
	}
	return
}

// rewriteDocRefs rewrites all $ref in the OpenAPI document
func rewriteDocRefs(doc interface{}) ([]byte, error) {
	raw, err := json.Marshal(doc)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal OpenAPI document: %w", err)
	}
	var any interface{}
	if err := json.Unmarshal(raw, &any); err != nil {
		return nil, err
	}
	walkAndRewriteRefs(any, "")
	return json.Marshal(any)
}

// walkAndRewriteRefs walks arbitrary JSON (map/array) and
//   - when encountering x-kubernetes-group-version-kind, extracts kind,
//     updating the currentKind context;
//   - rewrites all $ref inside the current context from Application* → kind*.
func walkAndRewriteRefs(node interface{}, currentKind string) {
	switch n := node.(type) {
	case map[string]interface{}:
		if gvk, ok := n["x-kubernetes-group-version-kind"]; ok {
			switch g := gvk.(type) {
			case map[string]interface{}:
				if k, ok := g["kind"].(string); ok {
					currentKind = k
				}
			case []interface{}:
				if len(g) > 0 {
					if mm, ok := g[0].(map[string]interface{}); ok {
						if k, ok := mm["kind"].(string); ok {
							currentKind = k
						}
					}
				}
			}
		}
		for k, v := range n {
			if k == "$ref" && currentKind != "" {
				if s, ok := v.(string); ok {
					n[k] = rewriteRefForKind(s, currentKind)
					continue
				}
			}
			walkAndRewriteRefs(v, currentKind)
		}
	case []interface{}:
		for _, v := range n {
			walkAndRewriteRefs(v, currentKind)
		}
	}
}

// rewriteRefForKind rewrites a reference to a specific kind.
func rewriteRefForKind(old, kind string) string {
	var base string
	switch {
	case strings.HasPrefix(old, "#/components/schemas/"):
		base = "#/components/schemas/"
	case strings.HasPrefix(old, "#/definitions/"):
		base = "#/definitions/"
	default:
		return old
	}
	switch {
	case strings.HasSuffix(old, ".Application"):
		return base + apiPrefix + "." + kind
	case strings.HasSuffix(old, ".ApplicationList"):
		return base + apiPrefix + "." + kind + "List"
	case strings.HasSuffix(old, ".ApplicationStatus"):
		return base + apiPrefix + "." + kind + "Status"
	default:
		return old
	}
}

// -----------------------------------------------------------------------------
// OpenAPI **v3** post-processor
// -----------------------------------------------------------------------------
func buildPostProcessV3(kindSchemas map[string]string) func(*spec3.OpenAPI) (*spec3.OpenAPI, error) {
	return func(doc *spec3.OpenAPI) (*spec3.OpenAPI, error) {

		if doc.Components == nil {
			doc.Components = &spec3.Components{}
		}
		if doc.Components.Schemas == nil {
			doc.Components.Schemas = map[string]*spec.Schema{}
		}

		// Get base schemas
		base, ok1 := doc.Components.Schemas[baseRef]
		list, ok2 := doc.Components.Schemas[baseListRef]
		stat, ok3 := doc.Components.Schemas[baseStatusRef]
		if !(ok1 && ok2 && ok3) && len(kindSchemas) > 0 {
			return doc, fmt.Errorf("base Application* schemas not found")
		}

		// Clone base schemas for each kind
		for kind, raw := range kindSchemas {
			ref := apiPrefix + "." + kind
			statusRef := ref + "Status"
			listRef := ref + "List"

			obj, status, l := cloneKindSchemas(kind, base, stat, list /*v3=*/, true)
			doc.Components.Schemas[ref] = obj
			doc.Components.Schemas[statusRef] = status
			doc.Components.Schemas[listRef] = l

			// patch .spec
			container := findSpecContainer(obj)
			if container == nil {
				container = obj
			}
			if err := patchSpec(container, raw); err != nil {
				return nil, fmt.Errorf("kind %s: %w", kind, err)
			}
		}

		// Delete base schemas
		delete(doc.Components.Schemas, baseRef)
		delete(doc.Components.Schemas, baseListRef)
		delete(doc.Components.Schemas, baseStatusRef)

		// Disable strategic-merge-patch+json
		for p, pi := range doc.Paths.Paths {
			if pi != nil && pi.Patch != nil && pi.Patch.RequestBody != nil {
				delete(pi.Patch.RequestBody.Content, smp)
				doc.Paths.Paths[p] = pi
			}
		}

		// Add name validation to ObjectMeta for UI form validation
		patchObjectMetaNameValidation(doc.Components.Schemas)

		// Rewrite all $ref in the document
		out, err := rewriteDocRefs(doc)
		if err != nil {
			return nil, err
		}
		return doc, json.Unmarshal(out, doc)
	}
}

// hasIntAndStringAnyOf returns true if anyOf is exactly a combination of string and integer.
func hasIntAndStringAnyOf(anyOf []spec.Schema) bool {
	seen := map[string]bool{}
	for i := range anyOf {
		for _, t := range anyOf[i].Type {
			seen[t] = true
		}
	}
	return seen["string"] && seen["integer"] && len(seen) <= 2
}

// sanitizeForV2 removes unsupported constructs for Swagger v2 and normalizes common patterns.
func sanitizeForV2(s *spec.Schema) {
	if s == nil {
		return
	}

	if len(s.AnyOf) > 0 {
		if hasIntAndStringAnyOf(s.AnyOf) {
			s.Type = spec.StringOrArray{"string"}
			if s.Extensions == nil {
				s.Extensions = map[string]interface{}{}
			}
			s.Extensions["x-kubernetes-int-or-string"] = true
		}
		s.AnyOf = nil
	}

	if len(s.OneOf) > 0 {
		s.OneOf = nil
	}

	if s.AdditionalProperties != nil {
		ap := s.AdditionalProperties
		if ap.Schema != nil {
			sanitizeForV2(ap.Schema)
		}
	}

	for k := range s.Properties {
		prop := s.Properties[k]
		sanitizeForV2(&prop)
		s.Properties[k] = prop
	}

	if s.Items != nil {
		if s.Items.Schema != nil {
			sanitizeForV2(s.Items.Schema)
		}
		for i := range s.Items.Schemas {
			sanitizeForV2(&s.Items.Schemas[i])
		}
	}

	for i := range s.AllOf {
		sanitizeForV2(&s.AllOf[i])
	}
}

// -----------------------------------------------------------------------------
// OpenAPI **v2** (swagger) post-processor
// -----------------------------------------------------------------------------
func buildPostProcessV2(kindSchemas map[string]string) func(*spec.Swagger) (*spec.Swagger, error) {
	return func(sw *spec.Swagger) (*spec.Swagger, error) {
		defs := sw.Definitions
		base, ok1 := defs[baseRef]
		list, ok2 := defs[baseListRef]
		stat, ok3 := defs[baseStatusRef]
		if !(ok1 && ok2 && ok3) && len(kindSchemas) > 0 {
			return sw, fmt.Errorf("base Application* schemas not found")
		}

		// Add name validation to ObjectMeta for UI form validation
		patchObjectMetaNameValidationV2(defs)

		for kind, raw := range kindSchemas {
			ref := apiPrefix + "." + kind
			statusRef := ref + "Status"
			listRef := ref + "List"

			obj, status, l := cloneKindSchemas(kind, &base, &stat, &list, false)

			if err := patchSpec(obj, raw); err != nil {
				return nil, fmt.Errorf("kind %s: %w", kind, err)
			}

			defs[ref] = *obj
			defs[statusRef] = *status
			defs[listRef] = *l
		}

		delete(defs, baseRef)
		delete(defs, baseListRef)
		delete(defs, baseStatusRef)

		for p, op := range sw.Paths.Paths {
			if op.Patch != nil && len(op.Patch.Consumes) > 0 {
				var out []string
				for _, c := range op.Patch.Consumes {
					if c != smp {
						out = append(out, c)
					}
				}
				op.Patch.Consumes = out
				sw.Paths.Paths[p] = op
			}
		}

		out, err := rewriteDocRefs(sw)
		if err != nil {
			return nil, err
		}
		if err := json.Unmarshal(out, sw); err != nil {
			return nil, err
		}

		for name := range sw.Definitions {
			s := sw.Definitions[name]
			sanitizeForV2(&s)
			sw.Definitions[name] = s
		}

		return sw, nil
	}
}
