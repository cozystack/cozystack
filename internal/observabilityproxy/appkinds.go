/*
Copyright 2026 The Cozystack Authors.

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

package observabilityproxy

import (
	"errors"
	"fmt"
	"slices"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/tools/cache"
)

// ApplicationDefinitionsResource is where the plural of every application
// kind is declared (api/v1alpha1/applicationdefinitions_types.go).
var ApplicationDefinitionsResource = schema.GroupVersionResource{Group: "cozystack.io", Version: "v1alpha1", Resource: "applicationdefinitions"}

// ErrKindsNotSynced means the ApplicationDefinitions cache has not been
// filled yet, so an unknown plural cannot be told from a missing one.
var ErrKindsNotSynced = errors.New("application definitions not synced")

const pluralIndex = "plural"

// KindLookup maps an application plural, as it appears in paths and RBAC,
// to the kind the lineage labels carry. An unknown plural is "", nil.
type KindLookup interface {
	Kind(plural string) (string, error)
}

// ApplicationPluralIndexers indexes ApplicationDefinitions by
// spec.application.plural; the informer feeding ApplicationKinds needs it.
func ApplicationPluralIndexers() cache.Indexers {
	return cache.Indexers{pluralIndex: func(obj any) ([]string, error) {
		u, ok := obj.(*unstructured.Unstructured)
		if !ok {
			return nil, nil
		}
		plural, _, _ := unstructured.NestedString(u.Object, "spec", "application", "plural")
		if plural == "" {
			return nil, nil
		}
		return []string{plural}, nil
	}}
}

// ApplicationKinds is the one place the proxy maps a plural to its kind;
// both application roots use it.
type ApplicationKinds struct {
	indexer   cache.Indexer
	hasSynced func() bool
}

func NewApplicationKinds(indexer cache.Indexer, hasSynced func() bool) *ApplicationKinds {
	return &ApplicationKinds{indexer: indexer, hasSynced: hasSynced}
}

func (k *ApplicationKinds) Kind(plural string) (string, error) {
	if !k.hasSynced() {
		return "", ErrKindsNotSynced
	}
	objs, err := k.indexer.ByIndex(pluralIndex, plural)
	if err != nil {
		return "", err
	}
	var kinds []string
	for _, obj := range objs {
		kind, _, _ := unstructured.NestedString(obj.(*unstructured.Unstructured).Object, "spec", "application", "kind")
		if kind != "" && !slices.Contains(kinds, kind) {
			kinds = append(kinds, kind)
		}
	}
	switch len(kinds) {
	case 0:
		return "", nil
	case 1:
		return kinds[0], nil
	}
	// Picking one would scope the request to whichever definition the
	// cache happens to list first.
	slices.Sort(kinds)
	return "", fmt.Errorf("plural %q is declared for kinds %v", plural, kinds)
}
