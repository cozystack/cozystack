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

// Command openapi-gen assembles the OpenAPI v3 spec for apps.cozystack.io from
// ApplicationDefinition YAML files in the repository.
package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	cozyv1alpha1 "github.com/cozystack/cozystack/api/v1alpha1"
	sampleopenapi "github.com/cozystack/cozystack/pkg/generated/openapi"
	"k8s.io/kube-openapi/pkg/validation/spec"
	"sigs.k8s.io/yaml"
)

const apiPrefix = "com.github.cozystack.cozystack.pkg.apis.apps.v1alpha1"

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
}

func run() error {
	// Find all ApplicationDefinition YAML files
	pattern := "packages/system/*-rd/cozyrds/*.yaml"
	matches, err := filepath.Glob(pattern)
	if err != nil {
		return fmt.Errorf("glob %q: %w", pattern, err)
	}
	if len(matches) == 0 {
		return fmt.Errorf("no files matched %q — run from repo root", pattern)
	}

	// Parse ApplicationDefinitions and build kindSchemas map
	kindSchemas := map[string]string{}
	for _, path := range matches {
		data, err := os.ReadFile(path)
		if err != nil {
			return fmt.Errorf("read %s: %w", path, err)
		}
		var appDef cozyv1alpha1.ApplicationDefinition
		if err := yaml.Unmarshal(data, &appDef); err != nil {
			return fmt.Errorf("parse %s: %w", path, err)
		}
		kind := appDef.Spec.Application.Kind
		schema := appDef.Spec.Application.OpenAPISchema
		if kind == "" || schema == "" {
			continue
		}
		kindSchemas[kind] = schema
	}

	if len(kindSchemas) == 0 {
		return fmt.Errorf("no ApplicationDefinitions with kind+schema found")
	}

	// Get base OpenAPI definitions (Application, ApplicationList, ApplicationStatus + k8s types)
	defs := sampleopenapi.GetOpenAPIDefinitions(func(path string) spec.Ref {
		name := sanitizeName(path)
		return spec.MustCreateRef("#/components/schemas/" + name)
	})

	// Build schemas map
	schemas := map[string]*spec.Schema{}
	for path, def := range defs {
		name := sanitizeName(path)
		s := def.Schema
		schemas[name] = &s
	}

	baseRef := apiPrefix + ".Application"
	baseListRef := apiPrefix + ".ApplicationList"
	baseStatusRef := apiPrefix + ".ApplicationStatus"

	base, ok1 := schemas[baseRef]
	baseList, ok2 := schemas[baseListRef]
	baseStat, ok3 := schemas[baseStatusRef]
	if !ok1 || !ok2 || !ok3 {
		return fmt.Errorf("base Application schemas not found in GetOpenAPIDefinitions output")
	}

	// For each kind, clone base schemas and inject per-kind spec
	for kind, rawSchema := range kindSchemas {
		ref := apiPrefix + "." + kind
		statusRef := ref + "Status"
		listRef := ref + "List"

		obj := deepCopySchema(base)
		status := deepCopySchema(baseStat)
		list := deepCopySchema(baseList)

		// Set x-kubernetes-group-version-kind
		obj.Extensions = map[string]interface{}{
			"x-kubernetes-group-version-kind": []interface{}{
				map[string]interface{}{"group": "apps.cozystack.io", "version": "v1alpha1", "kind": kind},
			},
		}
		list.Extensions = map[string]interface{}{
			"x-kubernetes-group-version-kind": []interface{}{
				map[string]interface{}{"group": "apps.cozystack.io", "version": "v1alpha1", "kind": kind + "List"},
			},
		}

		// Fix refs inside obj and list
		if prop, ok := obj.Properties["status"]; ok {
			prop.Ref = spec.MustCreateRef("#/components/schemas/" + statusRef)
			obj.Properties["status"] = prop
		}
		if list.Properties != nil {
			if items := list.Properties["items"]; items.Items != nil && items.Items.Schema != nil {
				items.Items.Schema.Ref = spec.MustCreateRef("#/components/schemas/" + ref)
				list.Properties["items"] = items
			}
		}

		// Inject spec schema
		if err := patchSpec(obj, rawSchema); err != nil {
			fmt.Fprintf(os.Stderr, "warning: kind %s spec patch failed: %v\n", kind, err)
		}

		schemas[ref] = obj
		schemas[statusRef] = status
		schemas[listRef] = list
	}

	// Remove base Application schemas
	delete(schemas, baseRef)
	delete(schemas, baseListRef)
	delete(schemas, baseStatusRef)

	// Get version from environment
	version := os.Getenv("VERSION")
	if version == "" {
		version = "dev"
	}

	// Sort schema names for deterministic output
	schemaNames := make([]string, 0, len(schemas))
	for name := range schemas {
		schemaNames = append(schemaNames, name)
	}
	sort.Strings(schemaNames)
	orderedSchemas := make(map[string]*spec.Schema, len(schemas))
	for _, name := range schemaNames {
		orderedSchemas[name] = schemas[name]
	}

	doc := map[string]interface{}{
		"openapi": "3.0.0",
		"info": map[string]interface{}{
			"title":   "Cozystack apps.cozystack.io API",
			"version": version,
		},
		"paths": map[string]interface{}{},
		"components": map[string]interface{}{
			"schemas": orderedSchemas,
		},
	}

	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(doc)
}

// sanitizeName converts a Go type path to an OpenAPI component name.
// e.g. "github.com/cozystack/cozystack/pkg/apis/apps/v1alpha1.Application"
// → "com.github.cozystack.cozystack.pkg.apis.apps.v1alpha1.Application"
func sanitizeName(path string) string {
	// Split on last "." to separate package path from type name
	lastDot := strings.LastIndex(path, ".")
	if lastDot < 0 {
		return strings.ReplaceAll(path, "/", ".")
	}
	pkgPath := path[:lastDot]
	typeName := path[lastDot+1:]
	// Reverse the domain component (github.com → com.github)
	parts := strings.Split(pkgPath, "/")
	if len(parts) > 0 && strings.Contains(parts[0], ".") {
		domainParts := strings.Split(parts[0], ".")
		for i, j := 0, len(domainParts)-1; i < j; i, j = i+1, j-1 {
			domainParts[i], domainParts[j] = domainParts[j], domainParts[i]
		}
		parts[0] = strings.Join(domainParts, ".")
	}
	return strings.Join(parts, ".") + "." + typeName
}

func deepCopySchema(in *spec.Schema) *spec.Schema {
	if in == nil {
		return nil
	}
	raw, err := json.Marshal(in)
	if err != nil {
		panic(fmt.Errorf("marshal schema: %w", err))
	}
	var out spec.Schema
	if err := json.Unmarshal(raw, &out); err != nil {
		panic(fmt.Errorf("unmarshal schema: %w", err))
	}
	return &out
}

func patchSpec(target *spec.Schema, raw string) error {
	raw = strings.TrimSpace(raw)
	if raw == "" {
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
