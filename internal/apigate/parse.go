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
// each served version's openAPIV3Schema. CRD discovery is content-based (any
// file under the walked roots, regardless of directory name — see
// discoverYAMLFiles), so a single file routinely mixes genuine CRD documents
// with unrendered Helm markup (a `{{- if }}` guard, an `{{ include }}` call)
// in neighboring documents of the same `---`-delimited stream. A document
// that fails to parse as YAML is therefore skipped on its own rather than
// aborting the whole file, so a real CRD sharing a file with templated
// documents is still found.
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
					Served bool   `json:"served"`
					Schema struct {
						OpenAPIV3Schema Schema `json:"openAPIV3Schema"`
					} `json:"schema"`
				} `json:"versions"`
			} `json:"spec"`
		}
		if err := unmarshalYAMLDoc(docBytes, &doc); err != nil {
			continue
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
			// Only served versions are live API surface. Skipping unserved
			// versions keeps the classifier from diffing schemas no client can
			// call, and makes the standard CRD deprecation step (flip
			// served:true -> false) surface as a removed served version.
			if v.Name == "" || !v.Served {
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
//
// The plural class allows hyphens so a future hyphenated resource name is not
// silently dropped. The apps storage map is indexed by a variable
// (appsV1alpha1Storage[resConfig.Application.Plural]), not a string literal, so
// it never matches here — the apps API is covered by the cozyrds instead.
var apiserverStorageRe = regexp.MustCompile(`(\w+Storage)\["([a-z0-9-]+)"\]`)

// unmappedStoragePrefix labels resources whose storage variable prefix is not
// in storageVarGroups. Rather than silently dropping them — which would let a
// contributor add a brand-new aggregated group under a new variable and bypass
// the gate — they are surfaced under this synthetic group so the classifier
// reports them as a new group/resource, prompting the reviewer to map the
// prefix in storageVarGroups.
const unmappedStoragePrefix = "unmapped-apiserver-storage:"

// ParseAPIServerStorages extracts the static Go-backed aggregated resources
// registered in pkg/apiserver/apiserver.go. Known variable prefixes map to
// their real group (storageVarGroups); an unknown prefix is surfaced under a
// synthetic group (unmappedStoragePrefix) so drift cannot silently defeat the
// gate. These resources carry no checked-in schema, so they populate only the
// new-group / new-resource / removal detection. Kind is left empty (not
// recoverable from the registration line alone); reporting falls back to the
// plural.
func ParseAPIServerStorages(origin string, data []byte) []Resource {
	var out []Resource
	seen := map[resourceKey]struct{}{}
	for _, m := range apiserverStorageRe.FindAllStringSubmatch(string(data), -1) {
		prefix, plural := m[1], m[2]
		group, ok := storageVarGroups[prefix]
		if !ok {
			group = unmappedStoragePrefix + prefix
		}
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

// apiServicePlural is the sentinel Plural used for SourceAPIService
// resources. An APIService does not describe an individual resource the way
// a CRD or cozyrd does — it registers a whole (group, version) pair, served
// by whatever aggregated apiserver binary the group's registration points
// at, which may be built entirely outside this repository (no vendored Go
// types, no checked-in CRD). The literal is namespaced with "$" so it can
// never collide with a real plural name.
const apiServicePlural = "$apiservice"

// ParseAPIServices parses a (possibly multi-document) YAML file for
// kind: APIService manifests, surfacing one schema-less Resource per
// first-party (group, version) registration. Like ParseAPIServerStorages,
// these resources carry no checked-in schema, so they participate only in
// new-group / new-resource / removal detection via the shared
// empty-Versions mechanism — diffResource has nothing to iterate, so no
// Breaking finding is ever produced from one of these alone. Documents that
// fail to parse (Helm markup sharing a file with a real APIService, as with
// ParseCRDs) are skipped rather than failing the file.
func ParseAPIServices(origin string, data []byte) []Resource {
	var out []Resource
	for _, docBytes := range splitYAMLDocs(data) {
		if len(strings.TrimSpace(string(docBytes))) == 0 {
			continue
		}
		var doc struct {
			Kind string `json:"kind"`
			Spec struct {
				Group   string `json:"group"`
				Version string `json:"version"`
			} `json:"spec"`
		}
		if err := unmarshalYAMLDoc(docBytes, &doc); err != nil {
			continue
		}
		if doc.Kind != "APIService" || doc.Spec.Group == "" || doc.Spec.Version == "" {
			continue
		}
		out = append(out, Resource{
			Group:    doc.Spec.Group,
			Kind:     "(aggregated apiserver)",
			Plural:   apiServicePlural,
			Source:   SourceAPIService,
			Origin:   origin,
			Versions: map[string]Schema{doc.Spec.Version: {}},
		})
	}
	return out
}

// helmDirectiveLine matches a line that is entirely Go-template/Helm markup
// (`{{- if ... }}`, `{{- end }}`, `{{- /* comment */ -}}`), including its
// trailing newline.
var helmDirectiveLine = regexp.MustCompile(`(?m)^[ \t]*\{\{-?[^\n]*-?\}\}[ \t]*\r?\n?`)

// unmarshalYAMLDoc decodes one `---`-delimited document, tolerating Helm
// directive lines that share a document with otherwise-valid YAML. The
// convention this repo's charts use for a raw CRD/APIService body is to wrap
// the whole manifest in a single `{{- if }}...{{- end }}` block; the `{{- if
// }}` half is naturally its own document (nothing precedes it since it opens
// the block before a `---`), but the closing `{{- end }}` has no `---` before
// it and so lands in the same document as the manifest itself — plain
// yaml.Unmarshal rejects that whole document as invalid. Stripping
// whole-line directives before decoding recovers the manifest; a document
// that still fails after stripping is genuinely not YAML and is left to the
// caller to skip.
func unmarshalYAMLDoc(doc []byte, out any) error {
	if err := yaml.Unmarshal(doc, out); err == nil {
		return nil
	}
	return yaml.Unmarshal(helmDirectiveLine.ReplaceAll(doc, nil), out)
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
