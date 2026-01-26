// Copyright 2024 The Cozystack Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package fields

import (
	"fmt"

	"k8s.io/apimachinery/pkg/fields"
)

// Filter holds field selector filters extracted from a field selector string.
type Filter struct {
	// Name is the value from metadata.name field selector, empty if not specified
	Name string
	// Namespace is the value from metadata.namespace field selector, empty if not specified
	Namespace string
}

// ParseFieldSelector parses a field selector and extracts metadata.name and metadata.namespace values.
// Other field selectors are silently ignored as controller-runtime cache doesn't support them.
// See: https://github.com/kubernetes-sigs/controller-runtime/issues/612
func ParseFieldSelector(fieldSelector fields.Selector) (*Filter, error) {
	if fieldSelector == nil {
		return &Filter{}, nil
	}

	fs, err := fields.ParseSelector(fieldSelector.String())
	if err != nil {
		return nil, fmt.Errorf("invalid field selector: %v", err)
	}

	filter := &Filter{}

	// Check if selector is for metadata.name
	if name, exists := fs.RequiresExactMatch("metadata.name"); exists {
		filter.Name = name
	}

	// Check if selector is for metadata.namespace
	if namespace, exists := fs.RequiresExactMatch("metadata.namespace"); exists {
		filter.Namespace = namespace
	}

	// Note: Other field selectors are silently ignored as controller-runtime cache
	// doesn't support them. See: https://github.com/kubernetes-sigs/controller-runtime/issues/612

	return filter, nil
}

// MatchesName returns true if the filter has no name constraint or if the name matches.
func (f *Filter) MatchesName(name string) bool {
	return f.Name == "" || f.Name == name
}

// MatchesNamespace returns true if the filter has no namespace constraint or if the namespace matches.
func (f *Filter) MatchesNamespace(namespace string) bool {
	return f.Namespace == "" || f.Namespace == namespace
}
