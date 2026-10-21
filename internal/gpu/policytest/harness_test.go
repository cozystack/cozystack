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

// Package policytest evaluates the GPU ValidatingAdmissionPolicies shipped by
// cozystack-basics, read from the chart templates themselves, so the tests
// exercise the expressions a cluster receives rather than a copy of them.
package policytest

import (
	"context"
	"os"
	"reflect"
	"strings"
	"testing"

	admissionregistrationv1 "k8s.io/api/admissionregistration/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/version"
	"k8s.io/apiserver/pkg/admission"
	plugincel "k8s.io/apiserver/pkg/admission/plugin/cel"
	"k8s.io/apiserver/pkg/admission/plugin/policy/validating"
	"k8s.io/apiserver/pkg/admission/plugin/webhook/matchconditions"
	celconfig "k8s.io/apiserver/pkg/apis/cel"
	"k8s.io/apiserver/pkg/authentication/user"
	"k8s.io/apiserver/pkg/cel/environment"
	"sigs.k8s.io/yaml"
)

const templatesDir = "../../../packages/system/cozystack-basics/templates/"

// oldestServedMinor is the oldest Kubernetes minor the policies must compile
// on. An expression that compiles only on a newer library would be refused
// by such an API server, and with failurePolicy Fail that blocks every
// request the policy matches.
var oldestServedMinor = version.MajorMinor(1, 30)

type shipped struct {
	policy   *admissionregistrationv1.ValidatingAdmissionPolicy
	bindings []*admissionregistrationv1.ValidatingAdmissionPolicyBinding
}

// load returns the policies of a chart template with their bindings. Every
// binding must name a policy of the file and every policy needs one, so a
// binding pointed at the wrong policy leaves the other unbound and fails
// here. Whole-line directives are the render gates and are dropped; any
// other directive means the test is not looking at what renders, so it fails
// instead of guessing.
func load(t *testing.T, file string) map[string]*shipped {
	t.Helper()

	raw, err := os.ReadFile(templatesDir + file)
	if err != nil {
		t.Fatalf("read %s: %v", file, err)
	}
	var kept []string
	for line := range strings.SplitSeq(string(raw), "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "{{") && strings.HasSuffix(trimmed, "}}") {
			continue
		}
		if strings.Contains(line, "{{") {
			t.Fatalf("%s carries an inline directive the test cannot render: %q", file, line)
		}
		kept = append(kept, line)
	}

	out := map[string]*shipped{}
	var bindings []*admissionregistrationv1.ValidatingAdmissionPolicyBinding
	for doc := range strings.SplitSeq(strings.Join(kept, "\n"), "\n---\n") {
		var meta struct {
			Kind string `json:"kind"`
		}
		if err := yaml.Unmarshal([]byte(doc), &meta); err != nil {
			t.Fatalf("parse document of %s: %v", file, err)
		}
		switch meta.Kind {
		case "ValidatingAdmissionPolicy":
			p := &admissionregistrationv1.ValidatingAdmissionPolicy{}
			if err := yaml.UnmarshalStrict([]byte(doc), p); err != nil {
				t.Fatalf("parse policy of %s: %v", file, err)
			}
			if p.Spec.FailurePolicy == nil || *p.Spec.FailurePolicy != admissionregistrationv1.Fail {
				t.Fatalf("%s: policy %s must fail closed", file, p.Name)
			}
			out[p.Name] = &shipped{policy: p}
		case "ValidatingAdmissionPolicyBinding":
			b := &admissionregistrationv1.ValidatingAdmissionPolicyBinding{}
			if err := yaml.UnmarshalStrict([]byte(doc), b); err != nil {
				t.Fatalf("parse binding of %s: %v", file, err)
			}
			if len(b.Spec.ValidationActions) != 1 || b.Spec.ValidationActions[0] != admissionregistrationv1.Deny {
				t.Fatalf("%s: binding %s validationActions = %v, want [Deny]", file, b.Name, b.Spec.ValidationActions)
			}
			bindings = append(bindings, b)
		case "":
		default:
			t.Fatalf("%s carries an unexpected %s", file, meta.Kind)
		}
	}
	for _, b := range bindings {
		s, ok := out[b.Spec.PolicyName]
		if !ok {
			t.Fatalf("%s: binding %s names policy %q, which the file does not carry", file, b.Name, b.Spec.PolicyName)
		}
		s.bindings = append(s.bindings, b)
	}
	if len(out) == 0 {
		t.Fatalf("%s carries no policy", file)
	}
	for name, s := range out {
		if len(s.bindings) == 0 {
			t.Fatalf("%s: policy %s has no binding", file, name)
		}
	}
	return out
}

func get(t *testing.T, file, name string) *shipped {
	t.Helper()
	s, ok := load(t, file)[name]
	if !ok {
		t.Fatalf("%s carries no policy %s", file, name)
	}
	return s
}

// binding returns the only binding of a policy.
func (s *shipped) binding(t *testing.T) *admissionregistrationv1.ValidatingAdmissionPolicyBinding {
	t.Helper()
	if len(s.bindings) != 1 {
		t.Fatalf("policy %s has %d bindings, want one", s.policy.Name, len(s.bindings))
	}
	return s.bindings[0]
}

// compile mirrors compilePolicy in
// k8s.io/apiserver/pkg/admission/plugin/policy/validating, which is
// unexported. It also compiles against the oldest served minor and fails on
// an expression that library refuses.
func compile(t *testing.T, policy *admissionregistrationv1.ValidatingAdmissionPolicy) validating.Validator {
	t.Helper()
	compileAt(t, policy, oldestServedMinor)
	return compileAt(t, policy, environment.DefaultCompatibilityVersion())
}

func compileAt(t *testing.T, policy *admissionregistrationv1.ValidatingAdmissionPolicy, v *version.Version) validating.Validator {
	t.Helper()

	compiler, err := plugincel.NewCompositedCompiler(environment.MustBaseEnvSet(v))
	if err != nil {
		t.Fatalf("composited compiler: %v", err)
	}
	hasParams := policy.Spec.ParamKind != nil
	vars := plugincel.OptionalVariableDeclarations{HasParams: hasParams, HasAuthorizer: true}

	var named []plugincel.NamedExpressionAccessor
	for _, v := range policy.Spec.Variables {
		named = append(named, &validating.Variable{Name: v.Name, Expression: v.Expression})
	}
	compiler.CompileAndStoreVariables(named, vars, environment.StoredExpressions)

	var matcher matchconditions.Matcher
	if len(policy.Spec.MatchConditions) > 0 {
		var conds []plugincel.ExpressionAccessor
		for i := range policy.Spec.MatchConditions {
			conds = append(conds, (*matchconditions.MatchCondition)(&policy.Spec.MatchConditions[i]))
		}
		cc := compiler.CompileCondition(conds, vars, environment.StoredExpressions)
		for _, err := range cc.CompilationErrors() {
			t.Errorf("%s: match condition does not compile on %s: %v", policy.Name, v, err)
		}
		matcher = matchconditions.NewMatcher(cc, policy.Spec.FailurePolicy, "policy", "validate", policy.Name)
	}

	validations := make([]plugincel.ExpressionAccessor, len(policy.Spec.Validations))
	messages := make([]plugincel.ExpressionAccessor, len(policy.Spec.Validations))
	for i, val := range policy.Spec.Validations {
		validations[i] = &validating.ValidationCondition{Expression: val.Expression, Message: val.Message, Reason: val.Reason}
		if val.MessageExpression != "" {
			messages[i] = &validating.MessageExpressionCondition{MessageExpression: val.MessageExpression}
		}
	}
	validationFilter := compiler.CompileCondition(validations, vars, environment.StoredExpressions)
	messageFilter := compiler.CompileCondition(messages, plugincel.OptionalVariableDeclarations{HasParams: hasParams}, environment.StoredExpressions)
	for _, errs := range [][]error{validationFilter.CompilationErrors(), messageFilter.CompilationErrors()} {
		for _, err := range errs {
			t.Errorf("%s: expression does not compile on %s: %v", policy.Name, v, err)
		}
	}
	return validating.NewValidator(validationFilter, matcher, compiler.CompileCondition(nil, vars, environment.StoredExpressions), messageFilter, policy.Spec.FailurePolicy, nil)
}

type request struct {
	gvk       schema.GroupVersionKind
	resource  string
	sub       string
	namespace string
	obj, old  *unstructured.Unstructured
	user      user.Info
	params    runtime.Object
}

// evaluate returns the denial messages for the request, an empty slice when
// it is admitted, and fails on an evaluation error: with failurePolicy Fail
// an error denies too, but for a reason the test did not intend.
func evaluate(t *testing.T, v validating.Validator, r request) []string {
	t.Helper()

	gvr := schema.GroupVersionResource{Group: r.gvk.Group, Version: r.gvk.Version, Resource: r.resource}
	op := admission.Create
	var oldObj runtime.Object
	if r.old != nil {
		op = admission.Update
		oldObj = r.old
	}
	attrs := admission.NewAttributesRecord(r.obj, oldObj, r.gvk, r.namespace, r.obj.GetName(), gvr, r.sub, op, nil, false, r.user)
	versioned := &admission.VersionedAttributes{
		Attributes:         attrs,
		VersionedObject:    admission.NewLazyObject(r.obj),
		VersionedOldObject: admission.NewLazyObject(oldObj),
		VersionedKind:      r.gvk,
	}
	result := v.Validate(context.Background(), gvr, versioned, r.params, nil, celconfig.RuntimeCELCostBudget, nil)
	denials := []string{}
	for _, d := range result.Decisions {
		if d.Evaluation == validating.EvalError {
			t.Fatalf("evaluation error: %s", d.Message)
		}
		if d.Action == validating.ActionDeny {
			denials = append(denials, d.Message)
		}
	}
	return denials
}

func obj(apiVersion, kind, namespace, name string, fields map[string]any) *unstructured.Unstructured {
	u := &unstructured.Unstructured{Object: runtime.DeepCopyJSON(fields)}
	u.SetAPIVersion(apiVersion)
	u.SetKind(kind)
	u.SetName(name)
	if namespace != "" {
		u.SetNamespace(namespace)
	}
	return u
}

var (
	tenantUser       = &user.DefaultInfo{Name: "tenant-admin", Groups: []string{"tenant-acme-admin", "system:authenticated"}}
	clusterAdmin     = &user.DefaultInfo{Name: "admin", Groups: []string{"system:masters", "system:authenticated"}}
	controllerSA     = &user.DefaultInfo{Name: "system:serviceaccount:cozy-system:cozystack-controller", Groups: []string{"system:serviceaccounts", "system:serviceaccounts:cozy-system", "system:authenticated"}}
	kubevirtSA       = &user.DefaultInfo{Name: "system:serviceaccount:cozy-kubevirt:kubevirt-controller", Groups: []string{"system:serviceaccounts", "system:serviceaccounts:cozy-kubevirt", "system:authenticated"}}
	kubevirtOtherSA  = &user.DefaultInfo{Name: "system:serviceaccount:cozy-kubevirt:kubevirt-handler", Groups: []string{"system:serviceaccounts", "system:serviceaccounts:cozy-kubevirt", "system:authenticated"}}
	tenantNamespaced = &user.DefaultInfo{Name: "system:serviceaccount:tenant-acme:default", Groups: []string{"system:serviceaccounts", "system:serviceaccounts:tenant-acme", "system:authenticated"}}
)

// tenantNamespaceSelectors are the one definition of a tenant namespace the
// GPU policies share: tenant-root by name, every other tenant by the ancestor
// label each carries for tenant-root.
var tenantNamespaceSelectors = []metav1.LabelSelector{
	{MatchExpressions: []metav1.LabelSelectorRequirement{{
		Key: "kubernetes.io/metadata.name", Operator: metav1.LabelSelectorOpIn, Values: []string{"tenant-root"},
	}}},
	{MatchExpressions: []metav1.LabelSelectorRequirement{{
		Key: "tenant.cozystack.io/tenant-root", Operator: metav1.LabelSelectorOpExists,
	}}},
}

// requireTenantNamespaceBindings fails unless the policy is bound once per
// tenant namespace selector, and nothing in the policy narrows or widens that.
func requireTenantNamespaceBindings(t *testing.T, s *shipped) {
	t.Helper()
	if len(s.bindings) != len(tenantNamespaceSelectors) {
		t.Fatalf("%s: want %d bindings, got %d", s.policy.Name, len(tenantNamespaceSelectors), len(s.bindings))
	}
	for i, want := range tenantNamespaceSelectors {
		found := false
		for _, b := range s.bindings {
			if mr := b.Spec.MatchResources; mr != nil && mr.NamespaceSelector != nil && reflect.DeepEqual(*mr.NamespaceSelector, want) {
				found = true
			}
		}
		if !found {
			t.Fatalf("%s: no binding selects tenant namespaces by %+v (selector %d)", s.policy.Name, want, i)
		}
	}
	if ns := s.policy.Spec.MatchConstraints.NamespaceSelector; ns != nil && (len(ns.MatchLabels) > 0 || len(ns.MatchExpressions) > 0) {
		t.Fatalf("%s: policy namespaceSelector %+v would narrow the bindings", s.policy.Name, ns)
	}
	if len(s.policy.Spec.MatchConditions) > 0 {
		t.Fatalf("%s: tenant namespaces are selected by the bindings, not by match conditions", s.policy.Name)
	}
}
