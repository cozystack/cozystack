/*
Copyright 2025 The Cozystack Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package cozylib

import (
	"encoding/json"
	"fmt"
	"strings"

	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
)

// DeepMergeMaps performs a deep merge of two maps.
// Values from override map take precedence, but nested maps are merged recursively.
func DeepMergeMaps(base, override map[string]interface{}) map[string]interface{} {
	result := make(map[string]interface{})

	// Copy base map
	for k, v := range base {
		result[k] = v
	}

	// Merge override map
	for k, v := range override {
		if baseVal, exists := result[k]; exists {
			// If both are maps, recursively merge
			if baseMap, ok := baseVal.(map[string]interface{}); ok {
				if overrideMap, ok := v.(map[string]interface{}); ok {
					result[k] = DeepMergeMaps(baseMap, overrideMap)
					continue
				}
			}
		}
		// Override takes precedence for non-map values or new keys
		result[k] = v
	}

	return result
}

// MergeValues merges two JSON values with deep merge.
// baseValues are merged first, then overrideValues (overrideValues take precedence).
func MergeValues(baseValues, overrideValues *apiextensionsv1.JSON) (*apiextensionsv1.JSON, error) {
	var baseMap, overrideMap map[string]interface{}

	if baseValues != nil && len(baseValues.Raw) > 0 {
		if err := json.Unmarshal(baseValues.Raw, &baseMap); err != nil {
			return nil, fmt.Errorf("failed to unmarshal base values: %w", err)
		}
	} else {
		baseMap = make(map[string]interface{})
	}

	if overrideValues != nil && len(overrideValues.Raw) > 0 {
		if err := json.Unmarshal(overrideValues.Raw, &overrideMap); err != nil {
			return nil, fmt.Errorf("failed to unmarshal override values: %w", err)
		}
	} else {
		overrideMap = make(map[string]interface{})
	}

	// Deep merge: baseValues first, then overrideValues (overrideValues override)
	merged := DeepMergeMaps(baseMap, overrideMap)

	mergedJSON, err := json.Marshal(merged)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal merged values: %w", err)
	}

	return &apiextensionsv1.JSON{Raw: mergedJSON}, nil
}

// MergeValuesWithCRDPriority merges CRD values with existing values.
// Existing values have priority (user values override defaults), but _cozystack and _namespace
// from CRD completely overwrite existing values.
func MergeValuesWithCRDPriority(crdValues, existingValues *apiextensionsv1.JSON) (*apiextensionsv1.JSON, error) {
	// If CRD has no values, preserve existing
	if crdValues == nil || len(crdValues.Raw) == 0 {
		return existingValues, nil
	}

	// If existing has no values, use CRD values
	if existingValues == nil || len(existingValues.Raw) == 0 {
		return crdValues, nil
	}

	var crdMap, existingMap map[string]interface{}

	// Parse CRD values (defaults)
	if err := json.Unmarshal(crdValues.Raw, &crdMap); err != nil {
		return nil, fmt.Errorf("failed to unmarshal CRD values: %w", err)
	}

	// Parse existing HelmRelease values
	if err := json.Unmarshal(existingValues.Raw, &existingMap); err != nil {
		return nil, fmt.Errorf("failed to unmarshal existing values: %w", err)
	}

	// Start with existing values as base (user values take priority)
	// Then merge CRD values on top, but _cozystack and _namespace from CRD completely overwrite
	merged := DeepMergeMaps(existingMap, crdMap)

	// Explicitly handle "_cozystack" field: CRD values completely overwrite existing
	// This ensures _cozystack field from CRD is always used, even if user modified it
	if crdCozystack, exists := crdMap["_cozystack"]; exists {
		merged["_cozystack"] = crdCozystack
	}

	// Explicitly handle "_namespace" field: CRD values completely overwrite existing
	// This ensures _namespace field from CRD is always used, even if user modified it
	if crdNamespace, exists := crdMap["_namespace"]; exists {
		merged["_namespace"] = crdNamespace
	}

	mergedJSON, err := json.Marshal(merged)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal merged values: %w", err)
	}

	return &apiextensionsv1.JSON{Raw: mergedJSON}, nil
}

// RemoveUnderscoreFields recursively removes all fields starting with "_" from values.
// This is used to hide internal fields from API responses.
func RemoveUnderscoreFields(values *apiextensionsv1.JSON) (*apiextensionsv1.JSON, error) {
	if values == nil || len(values.Raw) == 0 {
		return values, nil
	}

	var valuesMap map[string]interface{}
	if err := json.Unmarshal(values.Raw, &valuesMap); err != nil {
		return nil, fmt.Errorf("failed to unmarshal values: %w", err)
	}

	removeUnderscoreFieldsRecursive(valuesMap)

	// Always return at least an empty JSON object, never nil
	if len(valuesMap) == 0 {
		return &apiextensionsv1.JSON{Raw: []byte("{}")}, nil
	}

	cleanedJSON, err := json.Marshal(valuesMap)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal cleaned values: %w", err)
	}

	return &apiextensionsv1.JSON{Raw: cleanedJSON}, nil
}

// removeUnderscoreFieldsRecursive recursively removes all fields starting with "_" from a map
func removeUnderscoreFieldsRecursive(m map[string]interface{}) {
	if m == nil {
		return
	}
	// Collect keys to delete (we can't delete while iterating)
	keysToDelete := make([]string, 0)
	for k, v := range m {
		if strings.HasPrefix(k, "_") {
			keysToDelete = append(keysToDelete, k)
		} else if nestedMap, ok := v.(map[string]interface{}); ok {
			// Recursively process nested maps
			removeUnderscoreFieldsRecursive(nestedMap)
		} else if nestedArray, ok := v.([]interface{}); ok {
			// Process arrays that might contain maps
			for _, item := range nestedArray {
				if itemMap, ok := item.(map[string]interface{}); ok {
					removeUnderscoreFieldsRecursive(itemMap)
				}
			}
		}
	}

	// Delete collected keys
	for _, k := range keysToDelete {
		delete(m, k)
	}
}

// RemoveUnderscoreFieldsFromMap recursively removes all fields starting with "_" from a map.
// This is a variant that works directly with map[string]any (used in defaulting).
func RemoveUnderscoreFieldsFromMap(m map[string]any) {
	if m == nil {
		return
	}
	// Collect keys to delete (we can't delete while iterating)
	keysToDelete := make([]string, 0)
	for k, v := range m {
		if strings.HasPrefix(k, "_") {
			keysToDelete = append(keysToDelete, k)
		} else if nestedMap, ok := v.(map[string]any); ok {
			// Recursively process nested maps
			RemoveUnderscoreFieldsFromMap(nestedMap)
		} else if nestedArray, ok := v.([]any); ok {
			// Process arrays that might contain maps
			for _, item := range nestedArray {
				if itemMap, ok := item.(map[string]any); ok {
					RemoveUnderscoreFieldsFromMap(itemMap)
				}
			}
		}
	}

	// Delete collected keys
	for _, k := range keysToDelete {
		delete(m, k)
	}
}

// CheckUnderscoreFields checks if any field starting with "_" exists in user values and returns an error if it does.
// This prevents users from setting internal fields.
func CheckUnderscoreFields(values *apiextensionsv1.JSON) error {
	if values == nil || len(values.Raw) == 0 {
		return nil
	}

	var valuesMap map[string]interface{}
	if err := json.Unmarshal(values.Raw, &valuesMap); err != nil {
		return fmt.Errorf("failed to unmarshal values: %w", err)
	}

	if hasUnderscoreFields(valuesMap) {
		return fmt.Errorf("fields starting with '_' are not allowed in user values")
	}

	return nil
}

// hasUnderscoreFields recursively checks if any field starting with "_" exists in a map
func hasUnderscoreFields(m map[string]interface{}) bool {
	if m == nil {
		return false
	}
	for k, v := range m {
		if strings.HasPrefix(k, "_") {
			return true
		}
		if nestedMap, ok := v.(map[string]interface{}); ok {
			if hasUnderscoreFields(nestedMap) {
				return true
			}
		} else if nestedArray, ok := v.([]interface{}); ok {
			for _, item := range nestedArray {
				if itemMap, ok := item.(map[string]interface{}); ok {
					if hasUnderscoreFields(itemMap) {
						return true
					}
				}
			}
		}
	}
	return false
}
