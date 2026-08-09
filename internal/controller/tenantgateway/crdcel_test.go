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
	"strings"
	"testing"

	apiextensions "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	apiextvalidation "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/validation"
	"k8s.io/apiextensions-apiserver/pkg/apiserver/schema"
	schemacel "k8s.io/apiextensions-apiserver/pkg/apiserver/schema/cel"
	"k8s.io/apiextensions-apiserver/pkg/apiserver/schema/listtype"
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

// v1alpha1SpecSchema returns the generated CRD's v1alpha1 spec schema,
// failing the test when no such version carries one.
//
// Every caller reads a marker off this schema to prove the marker
// generated. Searching for the version inline instead puts the
// assertions inside the search loop, where a renamed or dropped version
// skips the body and the test reports green having checked nothing —
// the precise regression these tests exist to catch. Returning through
// one helper that fails on absence makes that outcome unreachable.
func v1alpha1SpecSchema(t *testing.T) *apiextensionsv1.JSONSchemaProps {
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
		if v.Name != "v1alpha1" || v.Schema == nil || v.Schema.OpenAPIV3Schema == nil {
			continue
		}
		if p, ok := v.Schema.OpenAPIV3Schema.Properties["spec"]; ok {
			return &p
		}
	}
	t.Fatal("v1alpha1 spec schema not found in CRD")
	return nil
}

// specValidator compiles the spec schema the way the apiserver
// evaluates an incoming object: structural validation plus the CEL
// rules against the per-request runtime budget.
//
// This does NOT check whether the apiserver would accept the CRD in the
// first place. Install-time cost estimation is a separate code path
// with a separate budget, and a CRD can pass everything here while
// being refused on install. TestCRDPassesInstallTimeValidation covers
// that.
func specValidator(t *testing.T) *admissionCheck {
	t.Helper()

	specProps := v1alpha1SpecSchema(t)
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
// CEL rules and the controller's own validation agree on every case the
// CEL rules cover: reserved ports, duplicate ports, out-of-apex
// hostnames, names colliding with tlsPassthroughServices, and the cert
// modes that refuse the field outright.
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
		certMode   gatewayv1alpha1.CertMode
		wantReject bool
	}{
		{
			name:      "distinct native ports within apex",
			listeners: []listener{{"postgres", 5432, "postgres.foo.example.com"}, {"mysql", 3306, "mysql.foo.example.com"}},
		},
		{
			name:      "http01 accepts a passthrough listener",
			listeners: []listener{{"postgres", 5432, "postgres.foo.example.com"}},
			certMode:  gatewayv1alpha1.CertModeHTTP01,
		},
		{
			name:       "dns01 refuses a passthrough listener",
			listeners:  []listener{{"postgres", 5432, "postgres.foo.example.com"}},
			certMode:   gatewayv1alpha1.CertModeDNS01,
			wantReject: true,
		},
		{
			name:       "existingSecret refuses a passthrough listener",
			listeners:  []listener{{"postgres", 5432, "postgres.foo.example.com"}},
			certMode:   gatewayv1alpha1.CertModeExistingSecret,
			wantReject: true,
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
			// Left out when the row does not name one, so the rows that
			// predate the cert-mode rule keep exercising the other rules
			// with the field absent, the way the CRD's own default would
			// never leave it.
			if tc.certMode != "" {
				spec["certMode"] = string(tc.certMode)
			}

			gotCEL := a.rejects(t, spec)
			if gotCEL != tc.wantReject {
				t.Errorf("CEL rejected=%v, want %v", gotCEL, tc.wantReject)
			}

			gotGo := validateTLSPassthroughListeners(goList, tc.services, apex) != nil ||
				validatePassthroughListenerCertMode(goList, tc.certMode) != nil
			if gotGo != tc.wantReject {
				t.Errorf("controller validation rejected=%v, want %v", gotGo, tc.wantReject)
			}
			if gotCEL != gotGo {
				t.Errorf("CEL and controller disagree: CEL rejected=%v, controller rejected=%v", gotCEL, gotGo)
			}
		})
	}
}

// TestCRDPassesInstallTimeValidation runs the validation kube-apiserver
// applies when the CRD itself is written. It is a different budget from
// the per-request one the other tests exercise: the install-time
// estimator assumes the largest value each declared type permits, so an
// unbounded string or list inside a CEL rule blows the estimate even
// though every real object is small.
//
// What fails it is not an object but the CRD, on CREATE and on UPDATE
// alike. The CRD ships in the cozystack-controller chart, so that
// freezes the whole TenantGateway API across every cluster, fresh or
// upgraded. No other test in this suite touches that code path, which
// leaves the size bounds on spec.apex and spec.tlsPassthroughServices
// with no other guard.
func TestCRDPassesInstallTimeValidation(t *testing.T) {
	raw, err := os.ReadFile(crdPath)
	if err != nil {
		t.Fatalf("read CRD: %v", err)
	}
	var v1crd apiextensionsv1.CustomResourceDefinition
	if err := yaml.Unmarshal(raw, &v1crd); err != nil {
		t.Fatalf("unmarshal CRD: %v", err)
	}
	// A CRD manifest on disk has no status; validation demands at least
	// one stored version, so supply what the apiserver would have
	// written. Without this the run fails on an unrelated status error
	// and tells us nothing about cost.
	v1crd.Status.StoredVersions = []string{"v1alpha1"}

	var internal apiextensions.CustomResourceDefinition
	if err := apiextensionsv1.Convert_v1_CustomResourceDefinition_To_apiextensions_CustomResourceDefinition(&v1crd, &internal, nil); err != nil {
		t.Fatalf("convert CRD: %v", err)
	}
	for _, e := range apiextvalidation.ValidateCustomResourceDefinition(context.TODO(), &internal) {
		t.Errorf("apiserver would reject this CRD: %v", e)
	}
}

// TestTightenedConstraintsOnExistingObjects pins that every constraint
// added to an already-shipped field is one the apiserver ratchets, so an
// object whose stored value violates one keeps accepting writes that
// leave that value alone. The package README states this, and stating it
// in prose alone is how a claim drifts from the schema it describes.
//
// Ratcheting is on by default from Kubernetes 1.30 and the management
// cluster requires 1.33, so this holds on every supported cluster. It
// covers ordinary schema validations only: a list-type error is never
// ratcheted, which is why the duplicate case below must stay admissible.
// Declaring this list a set would refuse every write to an object that
// already carries a duplicate, including a write that changes something
// else — the controller reports the duplicate on status instead. That
// case is the guard: it reddens if the marker comes back.
//
// The run pairs each field with the listtype validator as well, because
// the schema validator does not implement list semantics and would call
// a duplicate admissible whether or not the marker were there.
func TestTightenedConstraintsOnExistingObjects(t *testing.T) {
	a := specValidator(t)

	spec := func(apex string, services ...string) map[string]interface{} {
		svcs := make([]interface{}, 0, len(services))
		for _, s := range services {
			svcs = append(svcs, s)
		}
		return map[string]interface{}{
			"apex":                   apex,
			"certMode":               "http01",
			"gatewayClassName":       "cilium",
			"tlsPassthroughServices": svcs,
		}
	}

	// rejectsUpdate answers what the apiserver does to a write of new
	// over old: the schema layer with ratcheting on, then the list-type
	// layer, which has no old value to correlate against and so cannot
	// ratchet anything.
	rejectsUpdate := func(newSpec, oldSpec map[string]interface{}) bool {
		if errs := apiservervalidation.ValidateCustomResourceUpdate(
			field.NewPath("spec"), newSpec, oldSpec, a.schema,
			apiservervalidation.WithRatcheting(nil),
		); len(errs) > 0 {
			return true
		}
		return len(listtype.ValidateListSetsAndMaps(field.NewPath("spec"), a.structural, newSpec)) > 0
	}

	for _, tc := range []struct {
		name         string
		newSpec, old map[string]interface{}
		wantRejected bool
	}{{
		name:         "upper-case apex left untouched",
		newSpec:      spec("FOO.example.com", "api"),
		old:          spec("FOO.example.com", "api"),
		wantRejected: false,
	}, {
		name:         "upper-case apex introduced by this write",
		newSpec:      spec("FOO.example.com", "api"),
		old:          spec("foo.example.com", "api"),
		wantRejected: true,
	}, {
		name:         "malformed service entry left untouched",
		newSpec:      spec("foo.example.com", "API_X"),
		old:          spec("foo.example.com", "API_X"),
		wantRejected: false,
	}, {
		name:         "malformed service entry introduced by this write",
		newSpec:      spec("foo.example.com", "API_X"),
		old:          spec("foo.example.com", "api"),
		wantRejected: true,
	}, {
		name:         "repeated service entry stays admissible",
		newSpec:      spec("foo.example.com", "api", "api"),
		old:          spec("foo.example.com", "api", "api"),
		wantRejected: false,
	}, {
		name:         "unique service entries",
		newSpec:      spec("foo.example.com", "api", "vm-exportproxy"),
		old:          spec("foo.example.com", "api", "vm-exportproxy"),
		wantRejected: false,
	}} {
		t.Run(tc.name, func(t *testing.T) {
			if got := rejectsUpdate(tc.newSpec, tc.old); got != tc.wantRejected {
				t.Errorf("update rejected = %v, want %v", got, tc.wantRejected)
			}
		})
	}
}

// TestPassthroughListenerNameUniquenessIsEnforcedBySchema pins the
// listType=map / listMapKey=name markers.
//
// Name uniqueness is the one rule with no CEL equivalent and no entry
// in the parity table — the field doc and the parity test both point at
// these markers as the reason. Nothing else asserts they are there:
// the per-object harness does not implement list-map semantics, so
// dropping the markers leaves the whole suite green while duplicate
// names become admissible, render two tls-<name> listeners, and get the
// Gateway rejected wholesale.
func TestPassthroughListenerNameUniquenessIsEnforcedBySchema(t *testing.T) {
	listenersSchema := v1alpha1SpecSchema(t).Properties["tlsPassthroughListeners"]
	if listenersSchema.XListType == nil || *listenersSchema.XListType != "map" {
		t.Fatalf("tlsPassthroughListeners x-kubernetes-list-type=%v, want map; duplicate names would be admissible", listenersSchema.XListType)
	}
	if len(listenersSchema.XListMapKeys) != 1 || listenersSchema.XListMapKeys[0] != "name" {
		t.Fatalf("tlsPassthroughListeners x-kubernetes-list-map-keys=%v, want [name]", listenersSchema.XListMapKeys)
	}
}

// TestPassthroughListenerCapFitsGatewayAPI pins that a spec filled to
// the schema's maxItems, on a tenant publishing one app, still renders
// within Gateway API's 64-listener cap. The bound is read from the
// generated CRD so raising the marker without re-checking the
// arithmetic fails here rather than in a cluster.
//
// This is a bound on one field's contribution, not proof the total
// always fits — the rendered count also grows with published hostnames
// and passthrough services, and a tenant can exceed 64 with far fewer
// entries than the cap. TestRenderGatewayRejectsOverListenerCap covers
// the total.
func TestPassthroughListenerCapFitsGatewayAPI(t *testing.T) {
	const gatewayAPIListenerCap = 64

	listenersSchema := v1alpha1SpecSchema(t).Properties["tlsPassthroughListeners"]
	if listenersSchema.MaxItems == nil {
		t.Fatal("tlsPassthroughListeners has no maxItems; the cap is unbounded")
	}
	maxItems := *listenersSchema.MaxItems

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

// TestRenderGatewayRejectsOverListenerCap pins the total-count guard.
// Each individual field is bounded, but the Gateway's 64 slots are
// shared between the port-80 listener, per-hostname HTTPS listeners,
// passthrough services and passthrough listeners — so a spec where
// every field is within its own cap can still overflow. Without the
// guard the overflow surfaces as the apiserver rejecting the whole
// Gateway, which drops every app's HTTPS listener and reports nothing
// on the TenantGateway the tenant actually manages.
func TestRenderGatewayRejectsOverListenerCap(t *testing.T) {
	listeners := make([]gatewayv1alpha1.TLSPassthroughListener, 0, 62)
	for i := 0; i < 62; i++ {
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
	// 1 http + 62 passthrough + 2 published hostnames = 65.
	_, err := r.renderGateway(tgw, []string{"a.foo.example.com", "b.foo.example.com"}, nil)
	if err == nil {
		t.Fatal("expected an error for a 65-listener Gateway, got nil")
	}
	if !strings.Contains(err.Error(), "over the Gateway API cap") {
		t.Errorf("error does not name the budget: %v", err)
	}

	// One fewer published hostname lands exactly on the cap.
	if _, err := r.renderGateway(tgw, []string{"a.foo.example.com"}, nil); err != nil {
		t.Errorf("64 listeners should be accepted, got %v", err)
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
