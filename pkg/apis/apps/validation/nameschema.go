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

package validation

import (
	"bytes"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"

	appsv1alpha1 "github.com/cozystack/cozystack/pkg/apis/apps/v1alpha1"
)

// NameSchema is an application's own declaration of what its name may look
// like. A chart's naming budget follows from the sub-resources the chart
// renders and the names they take — facts that live in the chart and nowhere
// else — so the budget travels with the chart's schema instead of being a
// branch on Kind in the API server.
type NameSchema struct {
	Type        string `json:"type,omitempty"`
	Description string `json:"description,omitempty"`
	MinLength   *int64 `json:"minLength,omitempty"`
	MaxLength   *int64 `json:"maxLength,omitempty"`
	Pattern     string `json:"pattern,omitempty"`

	compiledPattern *regexp.Regexp
}

// MatchesPattern reports whether name satisfies the declared pattern, if any.
// The pattern is unanchored, as JSON Schema specifies: a declaration that means
// the whole name writes ^...$.
func (s *NameSchema) MatchesPattern(name string) bool {
	return s.compiledPattern == nil || s.compiledPattern.MatchString(name)
}

// nameSchemaKeywords are the keywords a declaration may carry that the API
// server interprets.
var nameSchemaKeywords = map[string]bool{
	"type": true, "description": true, "minLength": true, "maxLength": true, "pattern": true,
}

// jsonSchemaAnnotations constrain nothing, so a declaration carrying one is
// enforced as if it did not. Rejecting them would turn a generator adding a
// title or an example into an outage for every kind it regenerates.
var jsonSchemaAnnotations = map[string]bool{
	"title": true, "default": true, "examples": true, "deprecated": true,
	"readOnly": true, "writeOnly": true, "$comment": true,
}

// ParseNameSchema extracts the x-cozystack-name declaration from an
// ApplicationDefinition's openAPISchema. Returns (nil, nil) when the schema
// declares nothing about the name, which includes a schema that is not a JSON
// object at all: there is no declaration in it to enforce, and the spec
// schema's own loader is where that failure is reported.
func ParseNameSchema(raw string) (*NameSchema, error) {
	var doc map[string]json.RawMessage
	if err := json.Unmarshal([]byte(raw), &doc); err != nil {
		return nil, nil
	}
	declared, ok := doc[appsv1alpha1.NameSchemaExtension]
	if !ok {
		return nil, nil
	}
	return ParseNameDeclaration(declared)
}

// ParseNameDeclaration parses the value of an x-cozystack-name key. The
// openAPISchema is untrusted input — definitions from other repositories are
// written by hand — so a constraint keyword the server does not enforce, a
// wrong type, a null, or lengths no name can satisfy are an error rather than
// a constraint silently dropped. A pattern is only checked to compile as RE2;
// whether it matches anything is the chart's to get right.
func ParseNameDeclaration(declared []byte) (*NameSchema, error) {
	// encoding/json decodes null as "leave unset", which would turn a null
	// declaration into an empty one and a null keyword into a dropped
	// constraint.
	var keywords map[string]json.RawMessage
	if err := json.Unmarshal(declared, &keywords); err != nil {
		return nil, fmt.Errorf("decode: %w", err)
	}
	if keywords == nil {
		return nil, fmt.Errorf("declaration is null")
	}
	for k, v := range keywords {
		switch {
		case jsonSchemaAnnotations[k], strings.HasPrefix(k, "x-"):
			continue
		case !nameSchemaKeywords[k]:
			return nil, fmt.Errorf("unsupported keyword %q", k)
		case string(bytes.TrimSpace(v)) == "null":
			return nil, fmt.Errorf("%s is null", k)
		}
	}

	var ns NameSchema
	if err := json.Unmarshal(declared, &ns); err != nil {
		return nil, fmt.Errorf("decode: %w", err)
	}
	switch {
	case ns.Type != "" && ns.Type != "string":
		return nil, fmt.Errorf("type %q: a name is a string", ns.Type)
	case ns.MaxLength != nil && *ns.MaxLength < 1:
		return nil, fmt.Errorf("maxLength %d admits no name", *ns.MaxLength)
	case ns.MinLength != nil && *ns.MinLength < 0:
		return nil, fmt.Errorf("minLength %d is negative", *ns.MinLength)
	case ns.MinLength != nil && ns.MaxLength != nil && *ns.MinLength > *ns.MaxLength:
		return nil, fmt.Errorf("minLength %d exceeds maxLength %d", *ns.MinLength, *ns.MaxLength)
	}
	if ns.Pattern != "" {
		re, err := regexp.Compile(ns.Pattern)
		if err != nil {
			return nil, fmt.Errorf("pattern %q does not compile as RE2: %w", ns.Pattern, err)
		}
		ns.compiledPattern = re
	}
	return &ns, nil
}
