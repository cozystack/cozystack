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
	"slices"
	"strings"
	"testing"

	admissionregistrationv1 "k8s.io/api/admissionregistration/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apiserver/pkg/admission"
	"k8s.io/apiserver/pkg/admission/plugin/policy/validating"
	celconfig "k8s.io/apiserver/pkg/apis/cel"
	"k8s.io/apiserver/pkg/authentication/user"

	"github.com/cozystack/cozystack/internal/lineagecontrollerwebhook"
)

const schedulingClassPolicyPath = "../../../../packages/system/cozystack-basics/templates/scheduling-class-policy.yaml"

func TestSchedulingClassPolicy_Binding(t *testing.T) {
	policy, binding := loadPolicy(t, schedulingClassPolicyPath)

	if binding.Spec.PolicyName != policy.Name {
		t.Errorf("binding names policy %q, want %q", binding.Spec.PolicyName, policy.Name)
	}
	if !slices.Equal(binding.Spec.ValidationActions, []admissionregistrationv1.ValidationAction{admissionregistrationv1.Deny}) {
		t.Errorf("binding validationActions = %v, want [Deny]", binding.Spec.ValidationActions)
	}
	if policy.Spec.FailurePolicy == nil || *policy.Spec.FailurePolicy != admissionregistrationv1.Fail {
		t.Errorf("failurePolicy must be Fail")
	}

	ops := []admissionregistrationv1.OperationType{admissionregistrationv1.Create, admissionregistrationv1.Update}
	rules := policy.Spec.MatchConstraints.ResourceRules
	if len(rules) != 1 {
		t.Fatalf("want one resource rule, got %d", len(rules))
	}
	if !slices.Equal(rules[0].APIGroups, []string{"apps.cozystack.io"}) || !slices.Equal(rules[0].Resources, []string{"*"}) {
		t.Errorf("rule must cover every apps.cozystack.io resource, got groups %v resources %v", rules[0].APIGroups, rules[0].Resources)
	}
	if !slices.Equal(rules[0].Operations, ops) {
		t.Errorf("operations = %v, want %v", rules[0].Operations, ops)
	}
	// A tenant's spec.schedulingClass is the class of the child tenant, not a
	// choice among its own namespace's classes.
	excluded := policy.Spec.MatchConstraints.ExcludeResourceRules
	if len(excluded) != 1 || !slices.Equal(excluded[0].Resources, []string{"tenants"}) {
		t.Errorf("tenants must be excluded, got %+v", excluded)
	}
}

// The annotation and label keys are spelled out in the policy; they must stay
// the ones the tenant chart writes and the lineage webhook reads.
func TestSchedulingClassPolicy_ReadsTheWebhookKeys(t *testing.T) {
	policy, _ := loadPolicy(t, schedulingClassPolicyPath)

	var expressions strings.Builder
	for _, v := range policy.Spec.Variables {
		expressions.WriteString(v.Expression)
	}
	for _, key := range []string{lineagecontrollerwebhook.AllowedSchedulingClassesAnnotation, "scheduler.cozystack.io/scheduling-class"} {
		if !strings.Contains(expressions.String(), `"`+key+`"`) {
			t.Errorf("policy variables do not read %q", key)
		}
	}
}

func TestSchedulingClassPolicy_Decisions(t *testing.T) {
	policy, _ := loadPolicy(t, schedulingClassPolicyPath)
	validator := compilePolicy(t, policy)

	type spec = map[string]any
	cases := []struct {
		name        string
		old         spec // nil means CREATE
		new         spec // nil omits spec from the object
		tenantClass string
		allowed     *string  // nil leaves the annotation off
		groups      []string // of the requesting user; none means a tenant user
		denied      bool
	}{
		{name: "no spec", new: nil},
		{name: "field absent", new: spec{"replicas": int64(2)}},
		{name: "field empty", new: spec{"schedulingClass": ""}, tenantClass: "tenant-class"},
		{name: "allowed class", new: spec{"schedulingClass": "co-region-b"}, allowed: ptr("co-region-a, co-region-b")},
		{name: "allowed class without a tenant class", new: spec{"schedulingClass": "co-region-a"}, allowed: ptr("co-region-a")},
		{name: "the tenant's own class", new: spec{"schedulingClass": "tenant-class"}, tenantClass: "tenant-class"},
		{name: "class outside the list", new: spec{"schedulingClass": "elsewhere"}, tenantClass: "tenant-class", allowed: ptr("co-region-a"), denied: true},
		{name: "class with no list", new: spec{"schedulingClass": "elsewhere"}, tenantClass: "tenant-class", denied: true},
		{name: "class with an empty list", new: spec{"schedulingClass": "elsewhere"}, allowed: ptr(""), denied: true},
		{name: "prefix of an allowed class", new: spec{"schedulingClass": "co-region"}, allowed: ptr("co-region-a"), denied: true},
		{name: "update switching to a class outside the list", old: spec{"schedulingClass": "co-region-a"}, new: spec{"schedulingClass": "elsewhere"}, allowed: ptr("co-region-a"), denied: true},
		{name: "update setting a class outside the list", old: spec{}, new: spec{"schedulingClass": "elsewhere"}, denied: true},
		// The class may have been dropped from the list since admission; the
		// webhook keeps such an application on the tenant class, and it must
		// stay editable.
		{name: "update keeping a class no longer allowed", old: spec{"schedulingClass": "co-region-a"}, new: spec{"schedulingClass": "co-region-a", "replicas": int64(3)}},
		{name: "update clearing the class", old: spec{"schedulingClass": "co-region-a"}, new: spec{"schedulingClass": ""}},
		// Cluster administrators extend the list rather than bypass it.
		{name: "cluster admin choosing a class outside the list", new: spec{"schedulingClass": "elsewhere"}, groups: []string{"system:masters"}, denied: true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			gvk := schema.GroupVersionKind{Group: "apps.cozystack.io", Version: "v1alpha1", Kind: "Postgres"}
			gvr := schema.GroupVersionResource{Group: "apps.cozystack.io", Version: "v1alpha1", Resource: "postgreses"}
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

			ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "tenant-test"}}
			if tc.tenantClass != "" {
				ns.Labels = map[string]string{"scheduler.cozystack.io/scheduling-class": tc.tenantClass}
			}
			if tc.allowed != nil {
				ns.Annotations = map[string]string{lineagecontrollerwebhook.AllowedSchedulingClassesAnnotation: *tc.allowed}
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

			result := validator.Validate(context.Background(), gvr, versioned, nil, ns, celconfig.RuntimeCELCostBudget, nil)
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
			for _, d := range denials {
				if !strings.Contains(d.Message, "Postgres db") || !strings.Contains(d.Message, "allowedSchedulingClasses") {
					t.Errorf("denial message must name the application and the tenant setting that allows a class, got %q", d.Message)
				}
			}
		})
	}
}

func ptr(s string) *string { return &s }
