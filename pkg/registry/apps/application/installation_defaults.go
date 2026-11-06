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

package application

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"

	appsv1alpha1 "github.com/cozystack/cozystack/pkg/apis/apps/v1alpha1"
	corev1 "k8s.io/api/core/v1"
	internalapiext "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions"
	apiextv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	apiextvalidation "k8s.io/apiextensions-apiserver/pkg/apiserver/validation"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	kubejson "k8s.io/apimachinery/pkg/util/json"
	"k8s.io/apimachinery/pkg/util/validation/field"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

func (r *REST) applyInstallationDefaults(ctx context.Context, app *appsv1alpha1.Application) error {
	if r.defaultsReader == nil {
		return nil
	}
	readCtx, cancel := context.WithTimeout(ctx, r.defaultsReadLimit)
	defer cancel()
	cm := &corev1.ConfigMap{}
	if err := r.defaultsReader.Get(readCtx, client.ObjectKey{Namespace: InstallationDefaultsNamespace, Name: InstallationDefaultsName}, cm); err != nil {
		if apierrors.IsNotFound(err) {
			return nil
		}
		return fmt.Errorf("read application defaults: %w", err)
	}
	defaults, err := parseInstallationDefaults(cm.Data["defaults.json"])
	if err != nil {
		return fmt.Errorf("invalid application defaults: %w", err)
	}
	values, configured := defaults[r.gvr.Resource]
	if !configured {
		return nil
	}
	path := field.NewPath("applicationDefaults").Key(r.gvr.Resource)
	if r.defaultsSchemaErr != nil {
		return fmt.Errorf("%s: cannot validate installation defaults: %w", path, r.defaultsSchemaErr)
	}
	if r.defaultsValidator == nil {
		return fmt.Errorf("%s: application schema is unavailable", path)
	}
	// OpenAPI validation expects native numbers, including inside allOf branches.
	// Keep the original JSON numbers for persistence without a float round-trip.
	validationValues := cloneMap(values)
	if err := kubejson.ConvertMapNumbers(validationValues, 0); err != nil {
		return fmt.Errorf("%s: invalid configured number: %w", path, err)
	}
	if errs := apiextvalidation.ValidateCustomResource(path, validationValues, r.defaultsValidator); len(errs) > 0 {
		return fmt.Errorf("invalid installation defaults: %w", errs.ToAggregate())
	}
	spec := map[string]any{}
	if app.Spec != nil && len(app.Spec.Raw) > 0 {
		if err := decodeDefaultsJSON(app.Spec.Raw, &spec); err != nil {
			return apierrors.NewBadRequest(fmt.Sprintf("decode application spec: %v", err))
		}
	}
	if spec == nil {
		spec = map[string]any{}
	}
	fillInstallationDefaults(spec, values, r.defaultsSchema)
	raw, err := json.Marshal(spec)
	if err != nil {
		return err
	}
	app.Spec = &apiextv1.JSON{Raw: raw}
	return nil
}

func buildInstallationDefaultsValidator(raw string) (apiextvalidation.SchemaCreateValidator, error) {
	var external apiextv1.JSONSchemaProps
	if err := json.Unmarshal([]byte(raw), &external); err != nil {
		return nil, fmt.Errorf("read application schema: %w", err)
	}
	if external.Type != "object" {
		return nil, fmt.Errorf("application schema must describe an object")
	}
	var schema internalapiext.JSONSchemaProps
	if err := apiextv1.Convert_v1_JSONSchemaProps_To_apiextensions_JSONSchemaProps(&external, &schema, nil); err != nil {
		return nil, err
	}
	prepareInstallationDefaultsSchema(&schema, true)
	validator, _, err := apiextvalidation.NewSchemaValidator(&schema)
	return validator, err
}

func prepareInstallationDefaultsSchema(schema *internalapiext.JSONSchemaProps, partial bool) {
	// Objects can be completed by user fields; replacement arrays cannot. What an
	// object must contain cannot be judged on a partial one, so required and the
	// lower bound on its properties are dropped, and so is its oneOf: with required
	// removed every branch that tells alternatives apart by presence matches. A
	// scalar's oneOf constrains its value and stays.
	if partial {
		schema.Required = nil
		schema.MinProperties = nil
		if schema.Type == "object" {
			schema.OneOf = nil
		}
	}
	// Omitted additionalProperties is open in JSON Schema. Installation
	// configuration requires an explicit declaration before accepting extra keys.
	if schema.Type == "object" && schema.AdditionalProperties == nil &&
		(schema.XPreserveUnknownFields == nil || !*schema.XPreserveUnknownFields) {
		schema.AdditionalProperties = &internalapiext.JSONSchemaPropsOrBool{Allows: false}
	}
	for key, child := range schema.Properties {
		prepareInstallationDefaultsSchema(&child, partial)
		schema.Properties[key] = child
	}
	if schema.AdditionalProperties != nil && schema.AdditionalProperties.Schema != nil {
		prepareInstallationDefaultsSchema(schema.AdditionalProperties.Schema, partial)
	}
	if schema.Items != nil {
		if schema.Items.Schema != nil {
			prepareInstallationDefaultsSchema(schema.Items.Schema, false)
		}
		for i := range schema.Items.JSONSchemas {
			prepareInstallationDefaultsSchema(&schema.Items.JSONSchemas[i], false)
		}
	}
	for _, branches := range [][]internalapiext.JSONSchemaProps{schema.AllOf, schema.AnyOf, schema.OneOf} {
		for i := range branches {
			prepareInstallationDefaultsSchema(&branches[i], partial)
		}
	}
}

func parseInstallationDefaults(raw string) (map[string]map[string]any, error) {
	var defaults map[string]map[string]any
	if err := decodeDefaultsJSON([]byte(raw), &defaults); err != nil {
		return nil, err
	}
	if defaults == nil {
		return nil, fmt.Errorf("defaults.json must be an object keyed by application plural")
	}
	for plural, values := range defaults {
		if plural == "" || strings.HasPrefix(plural, "_") || values == nil {
			return nil, fmt.Errorf("%q must name an application plural with an object value", plural)
		}
		for key := range values {
			if strings.HasPrefix(key, "_") {
				return nil, fmt.Errorf("%s: field %q is reserved", plural, key)
			}
		}
	}
	return defaults, nil
}

func decodeDefaultsJSON(raw []byte, target any) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	if decoder.Decode(new(any)) != io.EOF {
		return fmt.Errorf("expected a single JSON object")
	}
	return nil
}

// fillInstallationDefaults copies missing fields from defaults into spec. It
// recurses only into objects whose schema declares properties; dictionaries and
// free-form objects are replaced as a whole, like arrays, so a user's map is
// never completed with entries nobody wrote.
func fillInstallationDefaults(spec, defaults map[string]any, schema *apiextv1.JSONSchemaProps) {
	for key, value := range defaults {
		current, present := spec[key]
		if !present {
			spec[key] = clone(value)
			continue
		}
		obj, isObject := current.(map[string]any)
		fallback, isFallbackObject := value.(map[string]any)
		if !isObject || len(obj) == 0 || !isFallbackObject {
			continue
		}
		if child, declared := declaredProperty(schema, key); declared {
			fillInstallationDefaults(obj, fallback, child)
		}
	}
}

func declaredProperty(schema *apiextv1.JSONSchemaProps, key string) (*apiextv1.JSONSchemaProps, bool) {
	if schema == nil {
		return nil, false
	}
	child, ok := schema.Properties[key]
	if !ok || len(child.Properties) == 0 {
		return nil, false
	}
	// An object that also takes arbitrary keys is a map, whatever properties it names.
	if open := child.AdditionalProperties; open != nil && (open.Allows || open.Schema != nil) {
		return nil, false
	}
	if child.XPreserveUnknownFields != nil && *child.XPreserveUnknownFields {
		return nil, false
	}
	return &child, true
}

func parseDefaultsSchema(raw string) *apiextv1.JSONSchemaProps {
	var schema apiextv1.JSONSchemaProps
	if err := json.Unmarshal([]byte(raw), &schema); err != nil {
		return nil
	}
	return &schema
}
