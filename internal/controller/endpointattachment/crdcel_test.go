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

package endpointattachment

import (
	"context"
	"os"
	"testing"

	apiextensions "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	apiextvalidation "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/validation"
	"k8s.io/apiextensions-apiserver/pkg/apiserver/schema"
	schemacel "k8s.io/apiextensions-apiserver/pkg/apiserver/schema/cel"
	"k8s.io/apiextensions-apiserver/pkg/apiserver/schema/listtype"
	apiservervalidation "k8s.io/apiextensions-apiserver/pkg/apiserver/validation"
	"k8s.io/apimachinery/pkg/util/validation/field"
	celconfig "k8s.io/apiserver/pkg/apis/cel"
	"sigs.k8s.io/yaml"
)

// crdPath is the generated CRD the controller ships. The rules under test
// are compiled from the XValidation markers on the API types, so reading
// the generated file is what detects a marker that silently failed to
// generate.
const crdPath = "../../../packages/system/cozystack-controller/definitions/local.sdn.cozystack.io_endpointattachments.yaml"

type admissionCheck struct {
	cel        *schemacel.Validator
	structural *schema.Structural
	schema     apiservervalidation.SchemaValidator
}

// versionSchema returns the v1alpha1 schema of the shipped CRD, from the
// resource root.
func versionSchema(t *testing.T) *apiextensionsv1.JSONSchemaProps {
	t.Helper()
	raw, err := os.ReadFile(crdPath)
	if err != nil {
		t.Fatalf("read CRD: %v", err)
	}
	var crd apiextensionsv1.CustomResourceDefinition
	if err := yaml.Unmarshal(raw, &crd); err != nil {
		t.Fatalf("unmarshal CRD: %v", err)
	}
	for i := range crd.Spec.Versions {
		v := &crd.Spec.Versions[i]
		if v.Name == "v1alpha1" && v.Schema != nil && v.Schema.OpenAPIV3Schema != nil {
			return v.Schema.OpenAPIV3Schema
		}
	}
	t.Fatal("v1alpha1 schema not found in CRD")
	return nil
}

// specValidator compiles the spec subtree alone, which is enough for the
// rules nested inside it.
func specValidator(t *testing.T) *admissionCheck {
	t.Helper()
	p, ok := versionSchema(t).Properties["spec"]
	if !ok {
		t.Fatal("v1alpha1 spec schema not found in CRD")
	}
	if len(p.XValidations) == 0 {
		t.Fatal("spec schema carries no x-kubernetes-validations; the XValidation markers did not generate")
	}
	return compile(t, &p)
}

// rootValidator compiles the whole resource, which is what admission
// validates: a rule nested in spec never runs when spec itself is absent.
func rootValidator(t *testing.T) *admissionCheck {
	t.Helper()
	return compile(t, versionSchema(t))
}

func compile(t *testing.T, props *apiextensionsv1.JSONSchemaProps) *admissionCheck {
	t.Helper()
	var internal apiextensions.JSONSchemaProps
	if err := apiextensionsv1.Convert_v1_JSONSchemaProps_To_apiextensions_JSONSchemaProps(props, &internal, nil); err != nil {
		t.Fatalf("convert schema: %v", err)
	}
	structural, err := schema.NewStructural(&internal)
	if err != nil {
		t.Fatalf("structural schema: %v", err)
	}
	schemaValidator, _, err := apiservervalidation.NewSchemaValidator(&internal)
	if err != nil {
		t.Fatalf("schema validator: %v", err)
	}
	return &admissionCheck{
		cel:        schemacel.NewValidator(structural, true, celconfig.PerCallLimit),
		structural: structural,
		schema:     schemaValidator,
	}
}

// rejects reports whether admission would refuse the object. old is nil for
// a create; on an update the transition rules (oldSelf) run as well.
func (a *admissionCheck) rejects(t *testing.T, obj, old map[string]interface{}) bool {
	t.Helper()
	if errs := apiservervalidation.ValidateCustomResource(nil, obj, a.schema); len(errs) > 0 {
		return true
	}
	// Uniqueness in a listType=set is a structural check of its own, which
	// neither the OpenAPI schema nor CEL performs.
	if errs := listtype.ValidateListSetsAndMaps(nil, a.structural, obj); len(errs) > 0 {
		return true
	}
	var oldObj interface{}
	if old != nil {
		oldObj = old
	}
	errs, _ := a.cel.Validate(context.TODO(), field.NewPath("root"), a.structural, obj, oldObj, celconfig.RuntimeCELCostBudget)
	return len(errs) > 0
}

func spec(lb map[string]interface{}) map[string]interface{} {
	s := map[string]interface{}{
		"applicationRef": map[string]interface{}{"group": "apps.cozystack.io", "kind": "Postgres", "name": "mydb"},
		"endpoint":       map[string]interface{}{"serviceName": "postgres-mydb-ro"},
	}
	if lb != nil {
		s["loadBalancer"] = lb
	}
	return s
}

func TestCreateAdmission(t *testing.T) {
	a := specValidator(t)
	tests := []struct {
		name       string
		spec       map[string]interface{}
		wantReject bool
	}{
		{name: "minimal member mints from the default class", spec: spec(map[string]interface{}{})},
		{name: "className alone", spec: spec(map[string]interface{}{"className": "public"})},
		{name: "claimName alone", spec: spec(map[string]interface{}{"claimName": "held"})},
		{name: "className and claimName together", spec: spec(map[string]interface{}{"className": "public", "claimName": "held"}), wantReject: true},
		{name: "no mechanism member", spec: spec(nil), wantReject: true},
		{name: "PortList with ports", spec: spec(map[string]interface{}{"method": "PortList", "ports": []interface{}{int64(22)}})},
		{name: "PortList without ports", spec: spec(map[string]interface{}{"method": "PortList"}), wantReject: true},
		{name: "PortList with one port", spec: spec(map[string]interface{}{"method": "PortList", "ports": []interface{}{int64(22)}})},
		{name: "PortList with a duplicate port", spec: spec(map[string]interface{}{"method": "PortList", "ports": []interface{}{int64(22), int64(22)}}), wantReject: true},
		{name: "WholeIP without ports", spec: spec(map[string]interface{}{"method": "WholeIP"})},
		{name: "allowICMP with PortList", spec: spec(map[string]interface{}{"method": "PortList", "ports": []interface{}{int64(22)}, "allowICMP": true})},
		{name: "allowICMP with WholeIP", spec: spec(map[string]interface{}{"method": "WholeIP", "allowICMP": true}), wantReject: true},
		{name: "allowICMP without method", spec: spec(map[string]interface{}{"allowICMP": false}), wantReject: true},
		{name: "unknown method", spec: spec(map[string]interface{}{"method": "Magic"}), wantReject: true},
		{name: "unknown family", spec: spec(map[string]interface{}{"family": "IPv5"}), wantReject: true},
		{name: "port out of range", spec: spec(map[string]interface{}{"ports": []interface{}{int64(70000)}}), wantReject: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := a.rejects(t, tt.spec, nil); got != tt.wantReject {
				t.Fatalf("rejects=%v, want %v", got, tt.wantReject)
			}
		})
	}
}

// TestUpdateAdmission pins the immutability split: the fields that select
// the address (applicationRef, className, claimName, family, the member
// itself) are frozen, while the render knobs (ports, method, allowICMP and
// the endpoint) stay mutable.
func TestUpdateAdmission(t *testing.T) {
	a := specValidator(t)
	base := spec(map[string]interface{}{"className": "public", "family": "IPv4", "ports": []interface{}{int64(5432)}})
	mutate := func(f func(s map[string]interface{})) map[string]interface{} {
		s := spec(map[string]interface{}{"className": "public", "family": "IPv4", "ports": []interface{}{int64(5432)}})
		f(s)
		return s
	}
	lb := func(s map[string]interface{}) map[string]interface{} {
		return s["loadBalancer"].(map[string]interface{})
	}
	tests := []struct {
		name       string
		new        map[string]interface{}
		wantReject bool
	}{
		{name: "unchanged", new: mutate(func(map[string]interface{}) {})},
		{name: "applicationRef name changes", new: mutate(func(s map[string]interface{}) {
			s["applicationRef"].(map[string]interface{})["name"] = "other"
		}), wantReject: true},
		{name: "className changes", new: mutate(func(s map[string]interface{}) { lb(s)["className"] = "partner" }), wantReject: true},
		{name: "className dropped", new: mutate(func(s map[string]interface{}) { delete(lb(s), "className") }), wantReject: true},
		{name: "family changes", new: mutate(func(s map[string]interface{}) { lb(s)["family"] = "IPv6" }), wantReject: true},
		{name: "claimName added beside className", new: mutate(func(s map[string]interface{}) { lb(s)["claimName"] = "held" }), wantReject: true},
		{name: "ports change", new: mutate(func(s map[string]interface{}) { lb(s)["ports"] = []interface{}{int64(5432), int64(9187)} })},
		{name: "ports dropped", new: mutate(func(s map[string]interface{}) { delete(lb(s), "ports") })},
		{name: "endpoint retargets", new: mutate(func(s map[string]interface{}) {
			s["endpoint"].(map[string]interface{})["serviceName"] = "postgres-mydb-rw"
		})},
		{name: "member removed", new: mutate(func(s map[string]interface{}) { delete(s, "loadBalancer") }), wantReject: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := a.rejects(t, tt.new, base); got != tt.wantReject {
				t.Fatalf("rejects=%v, want %v", got, tt.wantReject)
			}
		})
	}
	t.Run("claimName to className switch", func(t *testing.T) {
		old := spec(map[string]interface{}{"claimName": "held"})
		if !a.rejects(t, spec(map[string]interface{}{"className": "public"}), old) {
			t.Fatal("switching from a referenced claim to a minted one must be refused")
		}
	})
}

// resource wraps a spec the way admission sees the whole object; a nil spec
// leaves the field out.
func resource(spec map[string]interface{}) map[string]interface{} {
	obj := map[string]interface{}{
		"apiVersion": "local.sdn.cozystack.io/v1alpha1",
		"kind":       "EndpointAttachment",
		"metadata":   map[string]interface{}{"name": "mydb-replicas", "namespace": "tenant-a"},
	}
	if spec != nil {
		obj["spec"] = spec
	}
	return obj
}

// TestWholeResourceAdmission validates from the resource root. Removing spec
// would skip every rule nested inside it, and restoring it would run them
// with no old value, so spec must be refused as absent on create and on
// update, while the render knobs inside it stay mutable.
func TestWholeResourceAdmission(t *testing.T) {
	a := rootValidator(t)
	held := spec(map[string]interface{}{"claimName": "held", "ports": []interface{}{int64(5432)}})
	tests := []struct {
		name       string
		obj, old   map[string]interface{}
		wantReject bool
	}{
		{name: "create with spec", obj: resource(held)},
		{name: "create without spec", obj: resource(nil), wantReject: true},
		{name: "update removing spec", obj: resource(nil), old: resource(held), wantReject: true},
		{name: "update switching claimName", obj: resource(spec(map[string]interface{}{"claimName": "other", "ports": []interface{}{int64(5432)}})), old: resource(held), wantReject: true},
		{name: "update switching claimName to className", obj: resource(spec(map[string]interface{}{"className": "public", "ports": []interface{}{int64(5432)}})), old: resource(held), wantReject: true},
		{name: "update changing ports", obj: resource(spec(map[string]interface{}{"claimName": "held", "ports": []interface{}{int64(5432), int64(9187)}})), old: resource(held)},
		{name: "update retargeting the endpoint", obj: func() map[string]interface{} {
			s := spec(map[string]interface{}{"claimName": "held", "ports": []interface{}{int64(5432)}})
			s["endpoint"] = map[string]interface{}{"serviceName": "postgres-mydb-rw"}
			return resource(s)
		}(), old: resource(held)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := a.rejects(t, tt.obj, tt.old); got != tt.wantReject {
				t.Fatalf("rejects=%v, want %v", got, tt.wantReject)
			}
		})
	}
}

func TestCRDPassesInstallTimeValidation(t *testing.T) {
	raw, err := os.ReadFile(crdPath)
	if err != nil {
		t.Fatalf("read CRD: %v", err)
	}
	var v1crd apiextensionsv1.CustomResourceDefinition
	if err := yaml.Unmarshal(raw, &v1crd); err != nil {
		t.Fatalf("unmarshal CRD: %v", err)
	}
	v1crd.Status.StoredVersions = []string{"v1alpha1"}
	var internal apiextensions.CustomResourceDefinition
	if err := apiextensionsv1.Convert_v1_CustomResourceDefinition_To_apiextensions_CustomResourceDefinition(&v1crd, &internal, nil); err != nil {
		t.Fatalf("convert CRD: %v", err)
	}
	for _, e := range apiextvalidation.ValidateCustomResourceDefinition(context.TODO(), &internal) {
		t.Errorf("apiserver would reject this CRD: %v", e)
	}
}
