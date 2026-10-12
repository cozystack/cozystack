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
	"context"
	"os"
	"slices"
	"strings"
	"testing"

	admissionregistrationv1 "k8s.io/api/admissionregistration/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apiserver/pkg/admission"
	plugincel "k8s.io/apiserver/pkg/admission/plugin/cel"
	"k8s.io/apiserver/pkg/admission/plugin/policy/validating"
	"k8s.io/apiserver/pkg/admission/plugin/webhook/matchconditions"
	celconfig "k8s.io/apiserver/pkg/apis/cel"
	"k8s.io/apiserver/pkg/authentication/user"
	"k8s.io/apiserver/pkg/cel/environment"
	"sigs.k8s.io/yaml"
)

// The policy is read from the chart that ships it, so the test exercises the
// expressions a cluster actually receives rather than a copy of them.
const externalTLSPolicyPath = "../../../../packages/system/cozystack-basics/templates/database-external-tls-policy.yaml"

// loadExternalTLSPolicy returns the policy and its binding from the chart
// template. The template's only directives are the capability guard lines
// around the manifests; any other directive would mean the test is no longer
// looking at what renders, so it fails instead of guessing.
func loadExternalTLSPolicy(t *testing.T) (*admissionregistrationv1.ValidatingAdmissionPolicy, *admissionregistrationv1.ValidatingAdmissionPolicyBinding) {
	t.Helper()

	raw, err := os.ReadFile(externalTLSPolicyPath)
	if err != nil {
		t.Fatalf("read policy template: %v", err)
	}
	var kept []string
	for line := range strings.SplitSeq(string(raw), "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "{{") && strings.HasSuffix(trimmed, "}}") {
			continue
		}
		if strings.Contains(line, "{{") {
			t.Fatalf("policy template carries an inline directive the test cannot render: %q", line)
		}
		kept = append(kept, line)
	}

	var policy *admissionregistrationv1.ValidatingAdmissionPolicy
	var binding *admissionregistrationv1.ValidatingAdmissionPolicyBinding
	for doc := range strings.SplitSeq(strings.Join(kept, "\n"), "\n---\n") {
		var meta struct {
			Kind string `json:"kind"`
		}
		if err := yaml.Unmarshal([]byte(doc), &meta); err != nil {
			t.Fatalf("parse document: %v", err)
		}
		switch meta.Kind {
		case "ValidatingAdmissionPolicy":
			policy = &admissionregistrationv1.ValidatingAdmissionPolicy{}
			if err := yaml.UnmarshalStrict([]byte(doc), policy); err != nil {
				t.Fatalf("parse policy: %v", err)
			}
		case "ValidatingAdmissionPolicyBinding":
			binding = &admissionregistrationv1.ValidatingAdmissionPolicyBinding{}
			if err := yaml.UnmarshalStrict([]byte(doc), binding); err != nil {
				t.Fatalf("parse binding: %v", err)
			}
		}
	}
	if policy == nil || binding == nil {
		t.Fatal("policy template must carry one ValidatingAdmissionPolicy and one binding")
	}
	return policy, binding
}

// compileExternalTLSPolicy mirrors compilePolicy in
// k8s.io/apiserver/pkg/admission/plugin/policy/validating, which is
// unexported: the same composition environment, the same variable, match
// condition, validation and message compilers.
func compileExternalTLSPolicy(t *testing.T, policy *admissionregistrationv1.ValidatingAdmissionPolicy) validating.Validator {
	t.Helper()

	compiler, err := plugincel.NewCompositedCompiler(environment.MustBaseEnvSet(environment.DefaultCompatibilityVersion()))
	if err != nil {
		t.Fatalf("composited compiler: %v", err)
	}
	vars := plugincel.OptionalVariableDeclarations{HasAuthorizer: true}

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
		matcher = matchconditions.NewMatcher(compiler.CompileCondition(conds, vars, environment.StoredExpressions), policy.Spec.FailurePolicy, "policy", "validate", policy.Name)
	}

	validations := make([]plugincel.ExpressionAccessor, len(policy.Spec.Validations))
	messages := make([]plugincel.ExpressionAccessor, len(policy.Spec.Validations))
	for i, v := range policy.Spec.Validations {
		validations[i] = &validating.ValidationCondition{Expression: v.Expression, Message: v.Message, Reason: v.Reason}
		if v.MessageExpression != "" {
			messages[i] = &validating.MessageExpressionCondition{MessageExpression: v.MessageExpression}
		}
	}
	validationFilter := compiler.CompileCondition(validations, vars, environment.StoredExpressions)
	messageFilter := compiler.CompileCondition(messages, plugincel.OptionalVariableDeclarations{}, environment.StoredExpressions)
	for _, errs := range [][]error{validationFilter.CompilationErrors(), messageFilter.CompilationErrors()} {
		for _, err := range errs {
			t.Errorf("policy expression does not compile: %v", err)
		}
	}
	return validating.NewValidator(validationFilter, matcher, compiler.CompileCondition(nil, vars, environment.StoredExpressions), messageFilter, policy.Spec.FailurePolicy, nil)
}

func TestDatabaseExternalTLSPolicy_Binding(t *testing.T) {
	policy, binding := loadExternalTLSPolicy(t)

	if binding.Spec.PolicyName != policy.Name {
		t.Errorf("binding names policy %q, want %q", binding.Spec.PolicyName, policy.Name)
	}
	if !slices.Equal(binding.Spec.ValidationActions, []admissionregistrationv1.ValidationAction{admissionregistrationv1.Deny}) {
		t.Errorf("binding validationActions = %v, want [Deny]", binding.Spec.ValidationActions)
	}
	if policy.Spec.FailurePolicy == nil || *policy.Spec.FailurePolicy != admissionregistrationv1.Fail {
		t.Errorf("failurePolicy must be Fail")
	}

	rules := policy.Spec.MatchConstraints.ResourceRules
	if len(rules) != 1 {
		t.Fatalf("want one resource rule, got %d", len(rules))
	}
	rule := rules[0]
	if !slices.Equal(rule.APIGroups, []string{"apps.cozystack.io"}) {
		t.Errorf("apiGroups = %v", rule.APIGroups)
	}
	// Both operations matter: without UPDATE an existing release could be
	// edited into a plaintext external endpoint after a compliant create.
	ops := []admissionregistrationv1.OperationType{admissionregistrationv1.Create, admissionregistrationv1.Update}
	if !slices.Equal(rule.Operations, ops) {
		t.Errorf("operations = %v, want %v", rule.Operations, ops)
	}
	if !slices.Equal(rule.Resources, []string{"redises"}) {
		t.Errorf("resources = %v, want [redises]", rule.Resources)
	}
}

func TestDatabaseExternalTLSPolicy_Decisions(t *testing.T) {
	policy, _ := loadExternalTLSPolicy(t)
	validator := compileExternalTLSPolicy(t, policy)

	tlsOn := map[string]any{"enabled": true}
	tlsOff := map[string]any{"enabled": false}

	type spec = map[string]any
	cases := []struct {
		name   string
		old    spec     // nil means CREATE
		new    spec     // nil omits spec from the object
		groups []string // of the requesting user; none means a tenant user
		denied bool
		// turnsTLSOn marks a denied edit that switches TLS on: withdrawing
		// external access does not help there, because the operator refuses
		// the change on a running instance regardless.
		turnsTLSOn bool
	}{
		{name: "redis external with TLS on", new: spec{"external": true, "tls": tlsOn}},
		{name: "redis with no spec fields", new: spec{}},
		{name: "redis with no spec at all", new: nil},
		{name: "redis internal without TLS", new: spec{"external": false}},
		{name: "redis internal with TLS off", new: spec{"tls": tlsOff}},
		{name: "redis external with tls unset", new: spec{"external": true}, denied: true},
		{name: "redis external with an empty tls map", new: spec{"external": true, "tls": map[string]any{}}, denied: true},
		{name: "redis external with a null tls map", new: spec{"external": true, "tls": nil}, denied: true},
		{name: "redis external with TLS explicitly off", new: spec{"external": true, "tls": tlsOff}, denied: true},
		// A spec's tls.enabled is a request, not the instance's state: the API
		// accepts a flip that the operator's RedisFailover schema refuses. So an
		// update may never turn external access on, whatever tls.enabled says.
		{name: "redis update exposing a release without TLS", old: spec{}, new: spec{"external": true}, denied: true},
		{name: "redis update exposing a release while turning TLS on", old: spec{}, new: spec{"external": true, "tls": tlsOn}, denied: true},
		// The second step of: flip tls.enabled on an internal plaintext
		// instance (accepted, refused by the operator), expose it, then remove
		// tls.enabled again. Refusing this step is what closes the sequence.
		{name: "redis update exposing a release whose spec claims TLS", old: spec{"tls": tlsOn}, new: spec{"external": true, "tls": tlsOn}, denied: true},
		{name: "redis update restoring external access after withdrawing it", old: spec{"external": false, "tls": tlsOn}, new: spec{"external": true, "tls": tlsOn}, denied: true},
		// Releases created before the policy keep working: an edit to an
		// instance that is already external is not a new exposure.
		{name: "redis update of a release already plaintext and external", old: spec{"external": true}, new: spec{"external": true, "replicas": int64(3)}},
		{name: "redis update of an external release with TLS on", old: spec{"external": true, "tls": tlsOn}, new: spec{"external": true, "tls": tlsOn, "replicas": int64(3)}},
		// Until the first reconcile creates the RedisFailover, nothing but this
		// policy stops TLS from being dropped from a release admitted as
		// external with TLS, so tls.enabled is frozen while external stays on.
		{name: "redis update dropping TLS from an external release", old: spec{"external": true, "tls": tlsOn}, new: spec{"external": true}, denied: true},
		{name: "redis update turning TLS off on an external release", old: spec{"external": true, "tls": tlsOn}, new: spec{"external": true, "tls": tlsOff}, denied: true},
		{name: "redis update turning TLS on for a plaintext external release", old: spec{"external": true}, new: spec{"external": true, "tls": tlsOn}, denied: true, turnsTLSOn: true},
		{name: "redis update spelling out TLS off on a plaintext external release", old: spec{"external": true}, new: spec{"external": true, "tls": tlsOff}},
		// Withdrawing external access releases the freeze, which is how a tenant
		// unblocks an instance whose tls.enabled was flipped before the policy.
		{name: "redis update withdrawing external access", old: spec{"external": true}, new: spec{"external": false}},
		{name: "redis update withdrawing external access and dropping TLS", old: spec{"external": true, "tls": tlsOn}, new: spec{"external": false}},
		{name: "cluster admin dropping TLS from an external release", old: spec{"external": true, "tls": tlsOn}, new: spec{"external": true}, groups: []string{"system:masters"}},
		{name: "cozy-system service account dropping TLS from an external release", old: spec{"external": true, "tls": tlsOn}, new: spec{"external": true}, groups: []string{"system:serviceaccounts:cozy-system"}},
		// The trusted-caller exemption belongs to the freeze alone: an
		// administrator cannot create or expose a plaintext Redis either.
		{name: "cluster admin creating an external release without TLS", new: spec{"external": true}, groups: []string{"system:masters"}, denied: true},
		{name: "cluster admin exposing an existing release", old: spec{"tls": tlsOn}, new: spec{"external": true, "tls": tlsOn}, groups: []string{"system:masters"}, denied: true},
		{name: "tenant namespace service account dropping TLS from an external release", old: spec{"external": true, "tls": tlsOn}, new: spec{"external": true}, groups: []string{"system:serviceaccounts:tenant-test"}, denied: true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			gvk := schema.GroupVersionKind{Group: "apps.cozystack.io", Version: "v1alpha1", Kind: "Redis"}
			gvr := schema.GroupVersionResource{Group: "apps.cozystack.io", Version: "v1alpha1", Resource: "redises"}
			obj := func(s spec) *unstructured.Unstructured {
				u := &unstructured.Unstructured{Object: map[string]any{
					"apiVersion": gvk.GroupVersion().String(),
					"kind":       gvk.Kind,
					"metadata":   map[string]any{"name": "db", "namespace": "tenant-test"},
				}}
				if s != nil {
					u.Object["spec"] = runtime.DeepCopyJSON(s)
				}
				return u
			}

			var oldObj runtime.Object
			op := admission.Create
			if tc.old != nil {
				oldObj = obj(tc.old)
				op = admission.Update
			}
			newObj := obj(tc.new)
			attrs := admission.NewAttributesRecord(newObj, oldObj, gvk, "tenant-test", "db", gvr, "", op, nil, false, &user.DefaultInfo{Name: "tenant-user", Groups: tc.groups})
			versioned := &admission.VersionedAttributes{Attributes: attrs, VersionedObject: admission.NewLazyObject(newObj), VersionedOldObject: admission.NewLazyObject(oldObj), VersionedKind: gvk}

			result := validator.Validate(context.Background(), gvr, versioned, nil, nil, celconfig.RuntimeCELCostBudget, nil)
			var denials []validating.PolicyDecision
			for _, d := range result.Decisions {
				if d.Evaluation == validating.EvalError {
					t.Fatalf("evaluation error: %s", d.Message)
				}
				if d.Action == validating.ActionDeny {
					denials = append(denials, d)
				}
			}
			if got := len(denials) > 0; got != tc.denied {
				t.Fatalf("denied = %v, want %v (decisions %+v)", got, tc.denied, result.Decisions)
			}
			// On an existing instance TLS cannot change, so the UPDATE messages
			// must not offer turning it on.
			for _, d := range denials {
				want := "Set tls.enabled: true"
				if op == admission.Update {
					want = "create a new instance with tls.enabled: true"
				}
				if !strings.Contains(d.Message, "Redis db") || !strings.Contains(d.Message, want) {
					t.Errorf("denial message must name the release and a remedy that works (%q), got %q", want, d.Message)
				}
				if tc.turnsTLSOn && strings.Contains(d.Message, "Set external: false") {
					t.Errorf("turning TLS on must not be offered the external: false remedy, got %q", d.Message)
				}
			}
		})
	}
}
