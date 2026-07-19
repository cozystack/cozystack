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

package tenantgateway

import (
	"context"
	"fmt"
	"os"
	"testing"

	apiextensions "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	"k8s.io/apiextensions-apiserver/pkg/apiserver/schema"
	schemacel "k8s.io/apiextensions-apiserver/pkg/apiserver/schema/cel"
	apiservervalidation "k8s.io/apiextensions-apiserver/pkg/apiserver/validation"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/validation/field"
	celconfig "k8s.io/apiserver/pkg/apis/cel"
	"sigs.k8s.io/yaml"

	gatewayv1alpha1 "github.com/cozystack/cozystack/api/gateway/v1alpha1"
)

// crdPath is the generated CRD the controller ships. The CEL rules under
// test are compiled from the +kubebuilder:validation:XValidation markers
// on TenantGatewaySpec, so reading the generated file (rather than
// hand-writing the rules here) is what makes this test detect a marker
// that silently failed to generate.
const crdPath = "../../../packages/system/cozystack-controller/definitions/gateway.cozystack.io_tenantgateways.yaml"

// admissionCheck bundles the two validators the apiserver applies to a
// spec: the structural schema (types, pattern, minimum/maximum,
// maxItems) and the compiled CEL rules. Both must be run to answer "is
// this write accepted" — the constraints under test are split across
// the two, and checking only one silently ignores half of them.
type admissionCheck struct {
	cel        *schemacel.Validator
	structural *schema.Structural
	schema     apiservervalidation.SchemaValidator
}

// specValidator compiles the spec schema exactly as the apiserver
// would. A CEL rule that fails to compile, or whose estimated cost
// exceeds the per-CRD budget, surfaces here — which matters because a
// CRD the apiserver refuses to install takes the whole platform's
// gateway API down, and nothing else in this suite would notice.
func specValidator(t *testing.T) *admissionCheck {
	t.Helper()

	raw, err := os.ReadFile(crdPath)
	if err != nil {
		t.Fatalf("read CRD: %v", err)
	}
	var crd apiextensionsv1.CustomResourceDefinition
	if err := yaml.Unmarshal(raw, &crd); err != nil {
		t.Fatalf("unmarshal CRD: %v", err)
	}

	var specProps *apiextensionsv1.JSONSchemaProps
	for i := range crd.Spec.Versions {
		v := &crd.Spec.Versions[i]
		if v.Name != "v1alpha1" || v.Schema == nil || v.Schema.OpenAPIV3Schema == nil {
			continue
		}
		if p, ok := v.Schema.OpenAPIV3Schema.Properties["spec"]; ok {
			specProps = &p
		}
	}
	if specProps == nil {
		t.Fatal("v1alpha1 spec schema not found in CRD")
	}
	if len(specProps.XValidations) == 0 {
		t.Fatal("spec schema carries no x-kubernetes-validations; the XValidation markers did not generate")
	}

	var internal apiextensions.JSONSchemaProps
	if err := apiextensionsv1.Convert_v1_JSONSchemaProps_To_apiextensions_JSONSchemaProps(specProps, &internal, nil); err != nil {
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

// rejects reports whether the apiserver would refuse this spec, running
// the structural schema and the CEL rules the same way admission does.
func (a *admissionCheck) rejects(t *testing.T, spec map[string]interface{}) bool {
	t.Helper()
	if errs := apiservervalidation.ValidateCustomResource(field.NewPath("spec"), spec, a.schema); len(errs) > 0 {
		return true
	}
	errs, _ := a.cel.Validate(context.TODO(), field.NewPath("spec"), a.structural, spec, nil, celconfig.RuntimeCELCostBudget)
	return len(errs) > 0
}

// TestSpecCELMatchesControllerValidation pins that the admission-time
// CEL rules and the controller's validateTLSPassthroughListeners agree
// on every case the CEL rules cover: reserved ports, duplicate ports,
// out-of-apex hostnames, and names colliding with tlsPassthroughServices.
//
// The two must not drift. CEL keeps a bad spec out of etcd so it never
// aborts the reconcile chain; the Go check still has to reject objects
// admitted before the rules existed. If one side stops rejecting a case
// the other rejects, this test says so.
//
// The check runs the structural schema alongside the CEL rules, because
// the constraints are split across both layers and the split is an
// implementation detail the caller should not have to know: hostname
// format rides on the field's pattern, the reserved-port and apex rules
// on CEL. Asserting only one layer is how the hostname-format gap went
// unnoticed — every malformed hostname is inside the apex, so the CEL
// rule waves it through and only the pattern stops it.
func TestSpecCELMatchesControllerValidation(t *testing.T) {
	a := specValidator(t)

	const apex = "foo.example.com"
	type listener struct {
		name string
		port int32
		host string
	}
	tests := []struct {
		name       string
		listeners  []listener
		services   []string
		wantReject bool
	}{
		{
			name:      "distinct native ports within apex",
			listeners: []listener{{"postgres", 5432, "postgres.foo.example.com"}, {"mysql", 3306, "mysql.foo.example.com"}},
		},
		{
			name:      "wildcard hostname under apex",
			listeners: []listener{{"kafka", 9092, "*.kafka.foo.example.com"}},
		},
		{
			name:      "hostname equal to apex",
			listeners: []listener{{"pg", 5432, apex}},
		},
		{
			name:       "port 443 is reserved",
			listeners:  []listener{{"pg", 443, "pg.foo.example.com"}},
			wantReject: true,
		},
		{
			name:       "port 80 is reserved",
			listeners:  []listener{{"pg", 80, "pg.foo.example.com"}},
			wantReject: true,
		},
		{
			name:       "duplicate port across listeners",
			listeners:  []listener{{"pg", 5432, "pg.foo.example.com"}, {"pg2", 5432, "pg2.foo.example.com"}},
			wantReject: true,
		},
		{
			name:       "hostname outside the apex",
			listeners:  []listener{{"pg", 5432, "pg.evil.example.com"}},
			wantReject: true,
		},
		{
			name:       "sibling domain must not pass the suffix test",
			listeners:  []listener{{"pg", 5432, "pg.evilfoo.example.com"}},
			wantReject: true,
		},
		{
			name:       "name collides with a passthrough service",
			listeners:  []listener{{"api", 5432, "api2.foo.example.com"}},
			services:   []string{"api"},
			wantReject: true,
		},
		// Hostname format is carried by the Pattern on the field, not
		// by CEL, but it belongs in this table for the same reason the
		// CEL rules do: the apex rule is a plain suffix test, so
		// without the pattern each of these typos is within the apex,
		// passes admission, and is caught only by the controller —
		// after the object is already in etcd and the reconcile chain
		// behind it has aborted.
		{
			name:       "underscore in hostname",
			listeners:  []listener{{"pg", 5432, "pg_main.foo.example.com"}},
			wantReject: true,
		},
		{
			name:       "upper-case in hostname",
			listeners:  []listener{{"pg", 5432, "PG.foo.example.com"}},
			wantReject: true,
		},
		{
			name:       "leading dash in hostname label",
			listeners:  []listener{{"pg", 5432, "-pg.foo.example.com"}},
			wantReject: true,
		},
		{
			name:       "wildcard not in the left-most label",
			listeners:  []listener{{"pg", 5432, "*.*.foo.example.com"}},
			wantReject: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			celList := make([]interface{}, 0, len(tc.listeners))
			goList := make([]gatewayv1alpha1.TLSPassthroughListener, 0, len(tc.listeners))
			for _, l := range tc.listeners {
				celList = append(celList, map[string]interface{}{
					"name": l.name, "port": int64(l.port), "hostname": l.host,
				})
				goList = append(goList, gatewayv1alpha1.TLSPassthroughListener{
					Name: l.name, Port: l.port, Hostname: l.host,
				})
			}
			spec := map[string]interface{}{
				"apex":                    apex,
				"tlsPassthroughListeners": celList,
			}
			if tc.services != nil {
				svcs := make([]interface{}, 0, len(tc.services))
				for _, s := range tc.services {
					svcs = append(svcs, s)
				}
				spec["tlsPassthroughServices"] = svcs
			}

			gotCEL := a.rejects(t, spec)
			if gotCEL != tc.wantReject {
				t.Errorf("CEL rejected=%v, want %v", gotCEL, tc.wantReject)
			}

			gotGo := validateTLSPassthroughListeners(goList, tc.services, apex) != nil
			if gotGo != tc.wantReject {
				t.Errorf("controller validation rejected=%v, want %v", gotGo, tc.wantReject)
			}
			if gotCEL != gotGo {
				t.Errorf("CEL and controller disagree: CEL rejected=%v, controller rejected=%v", gotCEL, gotGo)
			}
		})
	}
}

// TestPassthroughListenerCapFitsGatewayAPI pins that a spec filled to
// the schema's maxItems still renders a Gateway the apiserver accepts.
// Gateway API caps spec.listeners at 64 and renderGateway always adds
// the port-80 listener plus the terminate listeners, so a cap of 64
// here would let a spec that satisfies every other rule render 65 and
// be rejected wholesale — taking every app's HTTPS listener down with
// it, the outcome the validation exists to avoid. The bound is read
// from the generated CRD so raising the marker without re-checking the
// arithmetic fails here rather than in a cluster.
func TestPassthroughListenerCapFitsGatewayAPI(t *testing.T) {
	const gatewayAPIListenerCap = 64

	raw, err := os.ReadFile(crdPath)
	if err != nil {
		t.Fatalf("read CRD: %v", err)
	}
	var crd apiextensionsv1.CustomResourceDefinition
	if err := yaml.Unmarshal(raw, &crd); err != nil {
		t.Fatalf("unmarshal CRD: %v", err)
	}
	var maxItems int64
	for i := range crd.Spec.Versions {
		v := &crd.Spec.Versions[i]
		if v.Name != "v1alpha1" || v.Schema == nil || v.Schema.OpenAPIV3Schema == nil {
			continue
		}
		spec := v.Schema.OpenAPIV3Schema.Properties["spec"]
		field := spec.Properties["tlsPassthroughListeners"]
		if field.MaxItems == nil {
			t.Fatal("tlsPassthroughListeners has no maxItems; the cap is unbounded")
		}
		maxItems = *field.MaxItems
	}

	listeners := make([]gatewayv1alpha1.TLSPassthroughListener, 0, maxItems)
	for i := int64(0); i < maxItems; i++ {
		listeners = append(listeners, gatewayv1alpha1.TLSPassthroughListener{
			Name:     fmt.Sprintf("db%d", i),
			Port:     int32(10000 + i),
			Hostname: fmt.Sprintf("db%d.foo.example.com", i),
		})
	}
	tgw := &gatewayv1alpha1.TenantGateway{
		ObjectMeta: metav1.ObjectMeta{Name: "cozystack", Namespace: "tenant-foo"},
		Spec: gatewayv1alpha1.TenantGatewaySpec{
			Apex:                    "foo.example.com",
			CertMode:                gatewayv1alpha1.CertModeHTTP01,
			GatewayClassName:        "cilium",
			TLSPassthroughListeners: listeners,
		},
	}

	r := &Reconciler{Scheme: newScheme(t)}
	gw, err := r.renderGateway(tgw, []string{"app.foo.example.com"}, nil)
	if err != nil {
		t.Fatalf("renderGateway: %v", err)
	}
	if got := len(gw.Spec.Listeners); got > gatewayAPIListenerCap {
		t.Errorf("maxItems=%d renders %d listeners, over the Gateway API cap of %d", maxItems, got, gatewayAPIListenerCap)
	}
}

// TestSpecCELAcceptsEmptyPassthroughListeners guards the has() guards
// themselves: every rule is written to short-circuit when the optional
// field is absent, so a spec that never mentions tlsPassthroughListeners
// must pass. Dropping a "!has(...)" prefix would reject every existing
// TenantGateway in the cluster on its next write.
func TestSpecCELAcceptsEmptyPassthroughListeners(t *testing.T) {
	a := specValidator(t)
	for _, spec := range []map[string]interface{}{
		{"apex": "foo.example.com"},
		{"apex": "foo.example.com", "tlsPassthroughServices": []interface{}{"api"}},
		{"apex": "foo.example.com", "tlsPassthroughListeners": []interface{}{}},
	} {
		if a.rejects(t, spec) {
			t.Errorf("spec %v was rejected, want accepted", spec)
		}
	}
}
