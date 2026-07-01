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

package apigate

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"

	"sigs.k8s.io/yaml"
)

// appsGroup is the fixed API group served by the cozyrd ApplicationDefinitions.
const appsGroup = "apps.cozystack.io"

// ParseCozyRD parses an ApplicationDefinition (cozyrd) manifest into a single
// apps.cozystack.io Resource. The apps group is served at v1alpha1 only, and
// its per-resource schema lives in spec.application.openAPISchema as an
// embedded JSON string.
func ParseCozyRD(origin string, data []byte) (Resource, bool, error) {
	var doc struct {
		Kind string `json:"kind"`
		Spec struct {
			Application struct {
				Kind          string `json:"kind"`
				Plural        string `json:"plural"`
				OpenAPISchema string `json:"openAPISchema"`
			} `json:"application"`
		} `json:"spec"`
	}
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return Resource{}, false, fmt.Errorf("%s: %w", origin, err)
	}
	if doc.Kind != "ApplicationDefinition" {
		return Resource{}, false, nil
	}
	app := doc.Spec.Application
	if app.Plural == "" {
		return Resource{}, false, fmt.Errorf("%s: ApplicationDefinition has empty spec.application.plural", origin)
	}
	res := Resource{
		Group:    appsGroup,
		Kind:     app.Kind,
		Plural:   app.Plural,
		Source:   SourceCozyRD,
		Origin:   origin,
		Versions: map[string]Schema{},
	}
	if strings.TrimSpace(app.OpenAPISchema) != "" {
		var sch Schema
		if err := json.Unmarshal([]byte(app.OpenAPISchema), &sch); err != nil {
			return Resource{}, false, fmt.Errorf("%s: spec.application.openAPISchema is not valid JSON: %w", origin, err)
		}
		res.Versions["v1alpha1"] = sch
	}
	return res, true, nil
}

// ParseCRDs parses a (possibly multi-document) CustomResourceDefinition YAML
// file into one Resource per CRD, keyed by spec.group + spec.names.plural with
// each served version's openAPIV3Schema.
func ParseCRDs(origin string, data []byte) ([]Resource, error) {
	var out []Resource
	for i, docBytes := range splitYAMLDocs(data) {
		if len(strings.TrimSpace(string(docBytes))) == 0 {
			continue
		}
		var doc struct {
			Kind string `json:"kind"`
			Spec struct {
				Group string `json:"group"`
				Names struct {
					Kind   string `json:"kind"`
					Plural string `json:"plural"`
				} `json:"names"`
				Versions []struct {
					Name   string `json:"name"`
					Schema struct {
						OpenAPIV3Schema Schema `json:"openAPIV3Schema"`
					} `json:"schema"`
				} `json:"versions"`
			} `json:"spec"`
		}
		if err := yaml.Unmarshal(docBytes, &doc); err != nil {
			return nil, fmt.Errorf("%s (document %d): %w", origin, i, err)
		}
		if doc.Kind != "CustomResourceDefinition" {
			continue
		}
		if doc.Spec.Group == "" || doc.Spec.Names.Plural == "" {
			return nil, fmt.Errorf("%s (document %d): CRD missing spec.group or spec.names.plural", origin, i)
		}
		res := Resource{
			Group:    doc.Spec.Group,
			Kind:     doc.Spec.Names.Kind,
			Plural:   doc.Spec.Names.Plural,
			Source:   SourceCRD,
			Origin:   origin,
			Versions: map[string]Schema{},
		}
		for _, v := range doc.Spec.Versions {
			if v.Name == "" {
				continue
			}
			res.Versions[v.Name] = v.Schema.OpenAPIV3Schema
		}
		out = append(out, res)
	}
	return out, nil
}

// storageVarGroups maps the storage-map variable prefixes used in
// pkg/apiserver/apiserver.go to their API group. The apps map is intentionally
// omitted: apps.cozystack.io resources are discovered from the cozyrds, which
// additionally carry their schema. A new entry here (or a new variable prefix
// in the apiserver) is the signal to extend this table.
var storageVarGroups = map[string]string{
	"coreV1alpha1Storage": "core.cozystack.io",
	"sdnV1alpha1Storage":  "sdn.cozystack.io",
}

// apiserverStorageRe matches `<var>["<plural>"]` storage registrations, e.g.
//
//	coreV1alpha1Storage["tenantsecrets"] = ...
var apiserverStorageRe = regexp.MustCompile(`(\w+Storage)\["([a-z0-9]+)"\]`)

// ParseAPIServerStorages extracts the static Go-backed aggregated resources
// registered in pkg/apiserver/apiserver.go for the groups in storageVarGroups.
// These resources carry no checked-in schema, so they populate only the
// new-group / new-resource detection. Kind is left empty (not recoverable from
// the registration line alone); reporting falls back to the plural.
func ParseAPIServerStorages(origin string, data []byte) []Resource {
	var out []Resource
	seen := map[resourceKey]struct{}{}
	for _, m := range apiserverStorageRe.FindAllStringSubmatch(string(data), -1) {
		group, ok := storageVarGroups[m[1]]
		if !ok {
			continue
		}
		plural := m[2]
		k := resourceKey{Group: group, Plural: plural}
		if _, dup := seen[k]; dup {
			continue
		}
		seen[k] = struct{}{}
		out = append(out, Resource{
			Group:    group,
			Plural:   plural,
			Source:   SourceAPIServer,
			Origin:   origin,
			Versions: map[string]Schema{},
		})
	}
	return out
}

// splitYAMLDocs splits a YAML stream into individual document byte slices on
// `---` separators. It intentionally mirrors the simple splitting Helm and
// kubectl use; CRD manifests in this repo do not embed `---` inside block
// scalars, so line-based splitting is safe.
func splitYAMLDocs(data []byte) [][]byte {
	parts := regexp.MustCompile(`(?m)^---\s*$`).Split(string(data), -1)
	out := make([][]byte, 0, len(parts))
	for _, p := range parts {
		out = append(out, []byte(p))
	}
	return out
}
