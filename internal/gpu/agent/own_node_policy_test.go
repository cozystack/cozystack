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

package agent

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

const (
	ownNodePolicyPath = "../../../packages/system/gpu-node-agent/templates/own-node-policy.yaml"
	agentNamespace    = "cozy-gpu-operator"
	agentUser         = "system:serviceaccount:" + agentNamespace + ":gpu-node-agent"
	nodeNameExtra     = "authentication.kubernetes.io/node-name"
)

// loadOwnNodePolicy reads the policy the chart ships, with the release
// namespace filled in; any other template directive fails the test, since
// it would no longer be looking at what renders.
func loadOwnNodePolicy(t *testing.T) (*admissionregistrationv1.ValidatingAdmissionPolicy, *admissionregistrationv1.ValidatingAdmissionPolicyBinding) {
	t.Helper()
	raw, err := os.ReadFile(ownNodePolicyPath)
	if err != nil {
		t.Fatal(err)
	}
	text := strings.ReplaceAll(string(raw), "{{ .Release.Namespace }}", agentNamespace)
	if strings.Contains(text, "{{") {
		t.Fatal("policy template carries a directive the test cannot render")
	}
	var policy *admissionregistrationv1.ValidatingAdmissionPolicy
	var binding *admissionregistrationv1.ValidatingAdmissionPolicyBinding
	for doc := range strings.SplitSeq(text, "\n---\n") {
		var meta struct {
			Kind string `json:"kind"`
		}
		if err := yaml.Unmarshal([]byte(doc), &meta); err != nil {
			t.Fatal(err)
		}
		switch meta.Kind {
		case "ValidatingAdmissionPolicy":
			policy = &admissionregistrationv1.ValidatingAdmissionPolicy{}
			err = yaml.UnmarshalStrict([]byte(doc), policy)
		case "ValidatingAdmissionPolicyBinding":
			binding = &admissionregistrationv1.ValidatingAdmissionPolicyBinding{}
			err = yaml.UnmarshalStrict([]byte(doc), binding)
		}
		if err != nil {
			t.Fatal(err)
		}
	}
	if policy == nil || binding == nil {
		t.Fatal("want one policy and one binding")
	}
	return policy, binding
}

// compilePolicy mirrors the unexported compilePolicy of
// k8s.io/apiserver/pkg/admission/plugin/policy/validating.
func compilePolicy(t *testing.T, policy *admissionregistrationv1.ValidatingAdmissionPolicy) validating.Validator {
	t.Helper()
	compiler, err := plugincel.NewCompositedCompiler(environment.MustBaseEnvSet(environment.DefaultCompatibilityVersion()))
	if err != nil {
		t.Fatal(err)
	}
	vars := plugincel.OptionalVariableDeclarations{HasAuthorizer: true}
	var conds []plugincel.ExpressionAccessor
	for i := range policy.Spec.MatchConditions {
		conds = append(conds, (*matchconditions.MatchCondition)(&policy.Spec.MatchConditions[i]))
	}
	matchFilter := compiler.CompileCondition(conds, vars, environment.StoredExpressions)
	matcher := matchconditions.NewMatcher(matchFilter, policy.Spec.FailurePolicy, "policy", "validate", policy.Name)
	validations := make([]plugincel.ExpressionAccessor, len(policy.Spec.Validations))
	messages := make([]plugincel.ExpressionAccessor, len(policy.Spec.Validations))
	for i, v := range policy.Spec.Validations {
		validations[i] = &validating.ValidationCondition{Expression: v.Expression, Message: v.Message, Reason: v.Reason}
		if v.MessageExpression != "" {
			messages[i] = &validating.MessageExpressionCondition{MessageExpression: v.MessageExpression}
		}
	}
	validationFilter := compiler.CompileCondition(validations, vars, environment.StoredExpressions)
	for _, errs := range [][]error{matchFilter.CompilationErrors(), validationFilter.CompilationErrors()} {
		for _, err := range errs {
			t.Errorf("policy expression does not compile: %v", err)
		}
	}
	messageFilter := compiler.CompileCondition(messages, plugincel.OptionalVariableDeclarations{}, environment.StoredExpressions)
	return validating.NewValidator(validationFilter, matcher, compiler.CompileCondition(nil, vars, environment.StoredExpressions), messageFilter, policy.Spec.FailurePolicy, nil)
}

func TestOwnNodePolicyMatchesAgentWrites(t *testing.T) {
	policy, binding := loadOwnNodePolicy(t)
	if binding.Spec.PolicyName != policy.Name {
		t.Errorf("binding names %q, want %q", binding.Spec.PolicyName, policy.Name)
	}
	if !slices.Equal(binding.Spec.ValidationActions, []admissionregistrationv1.ValidationAction{admissionregistrationv1.Deny}) {
		t.Errorf("validationActions = %v, want [Deny]", binding.Spec.ValidationActions)
	}
	if policy.Spec.FailurePolicy == nil || *policy.Spec.FailurePolicy != admissionregistrationv1.Fail {
		t.Error("failurePolicy must be Fail")
	}
	rules := policy.Spec.MatchConstraints.ResourceRules
	if len(rules) != 1 {
		t.Fatalf("want one resource rule, got %d", len(rules))
	}
	r := rules[0]
	if !slices.Equal(r.APIGroups, []string{"gpu.cozystack.io"}) {
		t.Errorf("apiGroups = %v", r.APIGroups)
	}
	// A status patch is admitted as UPDATE of gpunodes/status.
	if !slices.Equal(r.Operations, []admissionregistrationv1.OperationType{admissionregistrationv1.Create, admissionregistrationv1.Update}) {
		t.Errorf("operations = %v", r.Operations)
	}
	if !slices.Equal(r.Resources, []string{"gpunodes", "gpunodes/status"}) {
		t.Errorf("resources = %v", r.Resources)
	}
}

func TestOwnNodePolicyDecisions(t *testing.T) {
	policy, _ := loadOwnNodePolicy(t)
	validator := compilePolicy(t, policy)

	tests := []struct {
		name        string
		user        string
		extra       map[string][]string
		object      string
		subresource string
		update      bool
		denied      bool
	}{
		{name: "agent creates its own node's GPUNode", user: agentUser, extra: map[string][]string{nodeNameExtra: {"gpu-node-1"}}, object: "gpu-node-1"},
		{name: "agent patches its own node's status", user: agentUser, extra: map[string][]string{nodeNameExtra: {"gpu-node-1"}}, object: "gpu-node-1", subresource: "status", update: true},
		{name: "agent creates another node's GPUNode", user: agentUser, extra: map[string][]string{nodeNameExtra: {"gpu-node-1"}}, object: "gpu-node-2", denied: true},
		{name: "agent patches another node's status", user: agentUser, extra: map[string][]string{nodeNameExtra: {"gpu-node-1"}}, object: "gpu-node-2", subresource: "status", update: true, denied: true},
		{name: "agent token without a node name", user: agentUser, object: "gpu-node-1", subresource: "status", update: true, denied: true},
		{name: "agent token with an empty node name list", user: agentUser, extra: map[string][]string{nodeNameExtra: {}}, object: "gpu-node-1", denied: true},
		{name: "controller writes any node's status", user: "system:serviceaccount:cozy-system:cozystack-controller", object: "gpu-node-2", subresource: "status", update: true},
		{name: "another pod-bound service account on another node", user: "system:serviceaccount:cozy-gpu-operator:other", extra: map[string][]string{nodeNameExtra: {"gpu-node-1"}}, object: "gpu-node-2", update: true},
		{name: "an administrator", user: "admin", object: "gpu-node-2", update: true},
	}
	gvk := schema.GroupVersionKind{Group: "gpu.cozystack.io", Version: "v1alpha1", Kind: "GPUNode"}
	gvr := schema.GroupVersionResource{Group: "gpu.cozystack.io", Version: "v1alpha1", Resource: "gpunodes"}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			obj := &unstructured.Unstructured{Object: map[string]any{
				"apiVersion": gvk.GroupVersion().String(),
				"kind":       gvk.Kind,
				"metadata":   map[string]any{"name": tt.object},
			}}
			var oldObj runtime.Object
			op := admission.Create
			if tt.update {
				oldObj = obj.DeepCopy()
				op = admission.Update
			}
			attrs := admission.NewAttributesRecord(obj, oldObj, gvk, "", tt.object, gvr, tt.subresource, op, nil, false,
				&user.DefaultInfo{Name: tt.user, Extra: tt.extra})
			versioned := &admission.VersionedAttributes{Attributes: attrs, VersionedObject: admission.NewLazyObject(obj), VersionedOldObject: admission.NewLazyObject(oldObj), VersionedKind: gvk}
			result := validator.Validate(context.Background(), gvr, versioned, nil, nil, celconfig.RuntimeCELCostBudget, nil)
			denied := false
			for _, d := range result.Decisions {
				if d.Evaluation == validating.EvalError {
					t.Fatalf("evaluation error: %s", d.Message)
				}
				if d.Action == validating.ActionDeny {
					denied = true
				}
			}
			if denied != tt.denied {
				t.Errorf("denied = %v, want %v (decisions %+v)", denied, tt.denied, result.Decisions)
			}
		})
	}
}
