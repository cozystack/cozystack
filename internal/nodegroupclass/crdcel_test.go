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

// Package nodegroupclass holds no code: NodeGroupClass is a pure API whose
// rules live in the generated CRD. The tests here compile that CRD the way
// the apiserver does and run objects through it.
package nodegroupclass

import (
	"bytes"
	"context"
	"io"
	"os"
	"testing"

	apiextensions "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	apiextvalidation "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/validation"
	"k8s.io/apiextensions-apiserver/pkg/apiserver/schema"
	schemacel "k8s.io/apiextensions-apiserver/pkg/apiserver/schema/cel"
	apiservervalidation "k8s.io/apiextensions-apiserver/pkg/apiserver/validation"
	"k8s.io/apimachinery/pkg/util/validation/field"
	utilyaml "k8s.io/apimachinery/pkg/util/yaml"
	celconfig "k8s.io/apiserver/pkg/apis/cel"
	"sigs.k8s.io/yaml"
)

const (
	crdPath = "../../packages/system/cozystack-controller/definitions/cozystack.io_nodegroupclasses.yaml"
	// The built-in classes the platform ships. Running them through the CRD
	// proves the platform does not ship objects its own rules refuse.
	builtinPath = "../../packages/system/kubernetes-nodes-rd/templates/nodegroupclasses.yaml"
)

func readCRD(t *testing.T) *apiextensionsv1.CustomResourceDefinition {
	t.Helper()
	raw, err := os.ReadFile(crdPath)
	if err != nil {
		t.Fatalf("read CRD: %v", err)
	}
	var crd apiextensionsv1.CustomResourceDefinition
	if err := yaml.Unmarshal(raw, &crd); err != nil {
		t.Fatalf("unmarshal CRD: %v", err)
	}
	return &crd
}

type admission struct {
	cel        *schemacel.Validator
	structural *schema.Structural
	schema     apiservervalidation.SchemaValidator
}

// rootAdmission compiles the whole-object schema, not only spec: the rule
// that reserves the built-in names reads metadata.name, which CEL sees only
// from the resource root.
func rootAdmission(t *testing.T) *admission {
	t.Helper()
	crd := readCRD(t)
	var props *apiextensionsv1.JSONSchemaProps
	for i := range crd.Spec.Versions {
		v := &crd.Spec.Versions[i]
		if v.Name == "v1alpha1" && v.Schema != nil && v.Schema.OpenAPIV3Schema != nil {
			props = v.Schema.OpenAPIV3Schema
		}
	}
	if props == nil {
		t.Fatal("v1alpha1 schema not found in CRD")
	}
	if len(props.XValidations) == 0 {
		t.Fatal("root schema carries no x-kubernetes-validations; the type-level XValidation marker did not generate")
	}
	var internal apiextensions.JSONSchemaProps
	if err := apiextensionsv1.Convert_v1_JSONSchemaProps_To_apiextensions_JSONSchemaProps(props, &internal, nil); err != nil {
		t.Fatalf("convert schema: %v", err)
	}
	structural, err := schema.NewStructural(&internal)
	if err != nil {
		t.Fatalf("structural schema: %v", err)
	}
	sv, _, err := apiservervalidation.NewSchemaValidator(&internal)
	if err != nil {
		t.Fatalf("schema validator: %v", err)
	}
	return &admission{
		cel:        schemacel.NewValidator(structural, true, celconfig.PerCallLimit),
		structural: structural,
		schema:     sv,
	}
}

// errors returns what admission reports for obj, given oldObj on an update
// and nil on a create.
func (a *admission) errors(obj, oldObj map[string]any) field.ErrorList {
	errs := apiservervalidation.ValidateCustomResource(nil, obj, a.schema)
	var old any
	if oldObj != nil {
		old = oldObj
	}
	celErrs, _ := a.cel.Validate(context.TODO(), nil, a.structural, obj, old, celconfig.RuntimeCELCostBudget)
	return append(errs, celErrs...)
}

func class(name string, spec map[string]any) map[string]any {
	return map[string]any{
		"apiVersion": "cozystack.io/v1alpha1",
		"kind":       "NodeGroupClass",
		"metadata":   map[string]any{"name": name},
		"spec":       spec,
	}
}

func image(ver, ref string) map[string]any {
	img := map[string]any{}
	if ver != "" {
		img["kubernetesVersion"] = ver
	}
	if ref != "" {
		img["reference"] = ref
	}
	return img
}

const ref = "registry.example.com/workers/kairos-worker@sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

func TestCreate(t *testing.T) {
	a := rootAdmission(t)
	tests := []struct {
		name   string
		obj    map[string]any
		reject bool
	}{
		{"talos built-in, canonical", class("talos", map[string]any{"os": "talos"}), false},
		{"kairos built-in, canonical", class("kairos", map[string]any{"os": "kairos"}), false},
		{"talos name with kairos os", class("talos", map[string]any{"os": "kairos"}), true},
		{"kairos name with talos os", class("kairos", map[string]any{"os": "talos"}), true},
		{"kairos name with an image", class("kairos", map[string]any{"os": "kairos", "image": image("v1.35.6", ref)}), true},
		{"hoster talos class", class("talos-custom", map[string]any{"os": "talos"}), false},
		{"hoster kairos class without image", class("kairos-default", map[string]any{"os": "kairos"}), false},
		{"hoster kairos class with image", class("kairos-example-v1.35.6-r1", map[string]any{"os": "kairos", "image": image("v1.35.6", ref)}), false},
		{"image on a talos class", class("talos-img", map[string]any{"os": "talos", "image": image("v1.35.6", ref)}), true},
		{"image without kubernetesVersion", class("k", map[string]any{"os": "kairos", "image": image("", ref)}), true},
		{"image without reference", class("k", map[string]any{"os": "kairos", "image": image("v1.35.6", "")}), true},
		{"minor-only kubernetesVersion", class("k", map[string]any{"os": "kairos", "image": image("v1.35", ref)}), true},
		{"reference with a scheme", class("k", map[string]any{"os": "kairos", "image": image("v1.35.6", "docker://"+ref)}), true},
		{"unknown os", class("k", map[string]any{"os": "ubuntu"}), true},
		{"no os", class("k", map[string]any{}), true},
		{"no spec", map[string]any{"apiVersion": "cozystack.io/v1alpha1", "kind": "NodeGroupClass", "metadata": map[string]any{"name": "k"}}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			errs := a.errors(tt.obj, nil)
			if got := len(errs) > 0; got != tt.reject {
				t.Errorf("rejected=%v, want %v; errors: %v", got, tt.reject, errs)
			}
		})
	}
}

func TestSpecIsImmutable(t *testing.T) {
	a := rootAdmission(t)
	tests := []struct {
		name     string
		old, new map[string]any
		reject   bool
	}{
		{"built-in talos unchanged", class("talos", map[string]any{"os": "talos"}), class("talos", map[string]any{"os": "talos"}), false},
		{"built-in talos to kairos", class("talos", map[string]any{"os": "talos"}), class("talos", map[string]any{"os": "kairos"}), true},
		{"hoster class os", class("c", map[string]any{"os": "talos"}), class("c", map[string]any{"os": "kairos"}), true},
		{"hoster class version", class("c", map[string]any{"os": "kairos", "image": image("v1.35.6", ref)}), class("c", map[string]any{"os": "kairos", "image": image("v1.35.7", ref)}), true},
		{"hoster class reference", class("c", map[string]any{"os": "kairos", "image": image("v1.35.6", ref)}), class("c", map[string]any{"os": "kairos", "image": image("v1.35.6", "registry.example.com/other:v1")}), true},
		{"hoster class adds image", class("c", map[string]any{"os": "kairos"}), class("c", map[string]any{"os": "kairos", "image": image("v1.35.6", ref)}), true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			errs := a.errors(tt.new, tt.old)
			if got := len(errs) > 0; got != tt.reject {
				t.Errorf("rejected=%v, want %v; errors: %v", got, tt.reject, errs)
			}
		})
	}
}

func TestBuiltinClassesPassAdmission(t *testing.T) {
	a := rootAdmission(t)
	raw, err := os.ReadFile(builtinPath)
	if err != nil {
		t.Fatalf("read built-in classes: %v", err)
	}
	dec := utilyaml.NewYAMLOrJSONDecoder(bytes.NewReader(raw), 4096)
	seen := map[string]bool{}
	for {
		var obj map[string]any
		if err := dec.Decode(&obj); err != nil {
			if err == io.EOF {
				break
			}
			t.Fatalf("decode: %v", err)
		}
		if obj == nil {
			continue
		}
		name, _ := obj["metadata"].(map[string]any)["name"].(string)
		seen[name] = true
		if errs := a.errors(obj, nil); len(errs) > 0 {
			t.Errorf("built-in class %q is refused by the CRD: %v", name, errs)
		}
	}
	for _, want := range []string{"talos", "kairos"} {
		if !seen[want] {
			t.Errorf("built-in class %q is not shipped", want)
		}
	}
}

func TestCRDPassesInstallTimeValidation(t *testing.T) {
	crd := readCRD(t)
	if crd.Spec.Scope != apiextensionsv1.ClusterScoped {
		t.Errorf("scope is %q, want Cluster", crd.Spec.Scope)
	}
	crd.Status.StoredVersions = []string{"v1alpha1"}
	var internal apiextensions.CustomResourceDefinition
	if err := apiextensionsv1.Convert_v1_CustomResourceDefinition_To_apiextensions_CustomResourceDefinition(crd, &internal, nil); err != nil {
		t.Fatalf("convert CRD: %v", err)
	}
	for _, e := range apiextvalidation.ValidateCustomResourceDefinition(context.TODO(), &internal) {
		t.Errorf("apiserver would reject this CRD: %v", e)
	}
}
