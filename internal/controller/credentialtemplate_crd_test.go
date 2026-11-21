// SPDX-License-Identifier: Apache-2.0

package controller

import (
	"context"
	"os"
	"testing"

	apiextensions "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	"k8s.io/apiextensions-apiserver/pkg/apiserver/schema"
	schemacel "k8s.io/apiextensions-apiserver/pkg/apiserver/schema/cel"
	"k8s.io/apimachinery/pkg/util/validation/field"
	celconfig "k8s.io/apiserver/pkg/apis/cel"
	"sigs.k8s.io/yaml"
)

// credentialTemplateCRD is the generated CRD cozystack-controller ships.
// Reading it rather than restating the rule catches a marker that did not
// generate.
const credentialTemplateCRD = "../../packages/system/cozystack-controller/definitions/cozystack.io_credentialtemplates.yaml"

// An engine-issued template, one that names a BucketAccess, stores the keys
// as they are issued, so its format must be plaintext; a template without a
// BucketAccess may use any format.
func TestCredentialTemplateCRDTiesBucketAccessToPlaintext(t *testing.T) {
	raw, err := os.ReadFile(credentialTemplateCRD)
	if err != nil {
		t.Fatal(err)
	}
	var crd apiextensionsv1.CustomResourceDefinition
	if err := yaml.Unmarshal(raw, &crd); err != nil {
		t.Fatal(err)
	}
	var spec *apiextensionsv1.JSONSchemaProps
	for i := range crd.Spec.Versions {
		if v := crd.Spec.Versions[i]; v.Name == "v1alpha1" && v.Schema != nil && v.Schema.OpenAPIV3Schema != nil {
			if p, ok := v.Schema.OpenAPIV3Schema.Properties["spec"]; ok {
				spec = &p
			}
		}
	}
	if spec == nil || len(spec.XValidations) == 0 {
		t.Fatal("v1alpha1 spec schema with x-kubernetes-validations not found in the CRD")
	}
	var internal apiextensions.JSONSchemaProps
	if err := apiextensionsv1.Convert_v1_JSONSchemaProps_To_apiextensions_JSONSchemaProps(spec, &internal, nil); err != nil {
		t.Fatal(err)
	}
	structural, err := schema.NewStructural(&internal)
	if err != nil {
		t.Fatal(err)
	}
	validator := schemacel.NewValidator(structural, true, celconfig.PerCallLimit)

	access := map[string]any{"name": "bucket-photos-web", "bucketClaimName": "bucket-photos", "bucketAccessClassName": "seaweedfs"}
	for _, tc := range []struct {
		name   string
		spec   map[string]any
		accept bool
	}{
		{"bucketAccess with plaintext", map[string]any{"username": "web", "format": map[string]any{"name": "plaintext"}, "bucketAccess": access}, true},
		{"bucketAccess with another format", map[string]any{"username": "web", "format": map[string]any{"name": "scram-sha-256"}, "bucketAccess": access}, false},
		{"bucketAccess with a format named cosi", map[string]any{"username": "web", "format": map[string]any{"name": "cosi"}, "bucketAccess": access}, false},
		{"plaintext without bucketAccess", map[string]any{"username": "web", "format": map[string]any{"name": "plaintext"}}, true},
		{"another format without bucketAccess", map[string]any{"username": "web", "format": map[string]any{"name": "sha256"}}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			errs, _ := validator.Validate(context.TODO(), field.NewPath("spec"), structural, tc.spec, nil, celconfig.RuntimeCELCostBudget)
			if accepted := len(errs) == 0; accepted != tc.accept {
				t.Errorf("accepted = %v, want %v: %v", accepted, tc.accept, errs)
			}
		})
	}
}
