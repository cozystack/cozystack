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

	corev1 "k8s.io/api/core/v1"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
)

const (
	// NamespaceAnnotationPrefix is the prefix for namespace annotations that should be copied to _namespace values
	NamespaceAnnotationPrefix = "namespace.cozystack.io/"
)

// ExtractNamespaceAnnotations extracts namespace.cozystack.io/* annotations from namespace
// and returns them as a map with the prefix removed.
// For example, "namespace.cozystack.io/host" becomes "host" in the returned map.
func ExtractNamespaceAnnotations(ns *corev1.Namespace) map[string]string {
	result := make(map[string]string)
	prefix := NamespaceAnnotationPrefix

	if ns.Annotations == nil {
		return result
	}

	for key, value := range ns.Annotations {
		if strings.HasPrefix(key, prefix) {
			// Remove prefix and add to result
			namespaceKey := strings.TrimPrefix(key, prefix)
			result[namespaceKey] = value
		}
	}

	return result
}

// InjectNamespaceAnnotationsIntoValues injects namespace.cozystack.io/* annotations into _namespace (top-level) in values.
// This function extracts annotations from the namespace and adds them to the _namespace field in the values JSON.
// If namespace is nil or has no matching annotations, values are returned as-is.
func InjectNamespaceAnnotationsIntoValues(values *apiextensionsv1.JSON, ns *corev1.Namespace) (*apiextensionsv1.JSON, error) {
	if ns == nil {
		return values, nil
	}

	// Extract namespace.cozystack.io/* annotations
	namespaceLabels := ExtractNamespaceAnnotations(ns)
	if len(namespaceLabels) == 0 {
		// No namespace annotations, return values as-is
		return values, nil
	}

	// Parse values
	var valuesMap map[string]interface{}
	if values != nil && len(values.Raw) > 0 {
		if err := json.Unmarshal(values.Raw, &valuesMap); err != nil {
			return nil, fmt.Errorf("failed to unmarshal values: %w", err)
		}
	} else {
		valuesMap = make(map[string]interface{})
	}

	// Convert namespaceLabels from map[string]string to map[string]interface{}
	namespaceLabelsMap := make(map[string]interface{})
	for k, v := range namespaceLabels {
		namespaceLabelsMap[k] = v
	}

	// Namespace annotations completely overwrite existing _namespace field (top-level)
	valuesMap["_namespace"] = namespaceLabelsMap

	// Marshal back to JSON
	mergedJSON, err := json.Marshal(valuesMap)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal values with namespace annotations: %w", err)
	}

	return &apiextensionsv1.JSON{Raw: mergedJSON}, nil
}
