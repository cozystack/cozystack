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
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apiserver/pkg/admission"
	"k8s.io/apiserver/pkg/admission/plugin/policy/validating"
	celconfig "k8s.io/apiserver/pkg/apis/cel"
	"k8s.io/apiserver/pkg/authentication/user"
)

const tenantNamespaceDeletePolicyPath = "../../../../packages/system/cozystack-basics/templates/tenant-namespace-delete-policy.yaml"

func TestTenantNamespaceDeletePolicy_Binding(t *testing.T) {
	policy, binding := loadPolicy(t, tenantNamespaceDeletePolicyPath)

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
	if !slices.Equal(rule.APIGroups, []string{""}) || !slices.Equal(rule.APIVersions, []string{"v1"}) {
		t.Errorf("rule must match core v1 only, got apiGroups %v apiVersions %v", rule.APIGroups, rule.APIVersions)
	}
	if !slices.Equal(rule.Operations, []admissionregistrationv1.OperationType{admissionregistrationv1.Delete}) {
		t.Errorf("operations = %v, want [DELETE]", rule.Operations)
	}
	if !slices.Equal(rule.Resources, []string{"namespaces"}) {
		t.Errorf("resources = %v, want [namespaces]", rule.Resources)
	}

	if binding.Spec.MatchResources == nil || binding.Spec.MatchResources.ObjectSelector == nil {
		t.Fatal("binding must select namespaces by label")
	}
	selector, err := metav1.LabelSelectorAsSelector(binding.Spec.MatchResources.ObjectSelector)
	if err != nil {
		t.Fatalf("binding objectSelector: %v", err)
	}
	selected := []struct {
		name   string
		labels labels.Set
		want   bool
	}{
		{name: "tenant namespace under tenant-root", labels: labels.Set{"tenant.cozystack.io/tenant-root": "", "tenant.cozystack.io/tenant-foo": "", "namespace.cozystack.io/host": "foo.example.org"}, want: true},
		{name: "nested tenant namespace", labels: labels.Set{"tenant.cozystack.io/tenant-root": "", "tenant.cozystack.io/tenant-foo": "", "tenant.cozystack.io/tenant-foo-bar": ""}, want: true},
		{name: "tenant-root namespace", labels: labels.Set{"platform.cozystack.io/no-delete": "true", "namespace.cozystack.io/host": "example.org", "namespace.cozystack.io/gateway": "tenant-root"}},
		{name: "namespace named tenant-* without tenant labels", labels: labels.Set{"namespace.cozystack.io/host": "case-probe.example.org"}},
		{name: "unlabelled namespace", labels: labels.Set{}},
	}
	for _, tc := range selected {
		if got := selector.Matches(tc.labels); got != tc.want {
			t.Errorf("%s: selected = %v, want %v", tc.name, got, tc.want)
		}
	}
}

func TestTenantNamespaceDeletePolicy_Decisions(t *testing.T) {
	policy, _ := loadPolicy(t, tenantNamespaceDeletePolicyPath)
	validator := compilePolicy(t, policy)

	fluxGroups := []string{"system:serviceaccounts", "system:serviceaccounts:cozy-fluxcd", "system:authenticated"}
	kubeSystemGroups := []string{"system:serviceaccounts", "system:serviceaccounts:kube-system", "system:authenticated"}
	adminGroups := []string{"system:masters", "system:authenticated"}

	cases := []struct {
		name      string
		namespace string
		user      string
		groups    []string
		denied    bool
		tenant    string
		parent    string
	}{
		{name: "helm-controller uninstalling a Tenant", namespace: "tenant-foo", user: "system:serviceaccount:cozy-fluxcd:flux", groups: fluxGroups},
		{name: "helm-controller uninstalling a nested Tenant", namespace: "tenant-foo-bar", user: "system:serviceaccount:cozy-fluxcd:flux", groups: fluxGroups},
		{name: "garbage collector following a deleted parent namespace", namespace: "tenant-foo-bar", user: "system:serviceaccount:kube-system:generic-garbage-collector", groups: kubeSystemGroups},
		{name: "cluster admin deleting a tenant namespace", namespace: "tenant-foo", user: "admin", groups: adminGroups, denied: true, tenant: "foo", parent: "tenant-root"},
		{name: "cluster admin deleting a nested tenant namespace", namespace: "tenant-foo-bar", user: "admin", groups: adminGroups, denied: true, tenant: "bar", parent: "tenant-foo"},
		{name: "cluster admin deleting a third-level tenant namespace", namespace: "tenant-foo-bar-baz", user: "admin", groups: adminGroups, denied: true, tenant: "baz", parent: "tenant-foo-bar"},
		{name: "cluster admin deleting a labelled namespace without a dash", namespace: "standalone", user: "admin", groups: adminGroups, denied: true, tenant: "standalone", parent: "tenant-root"},
		{name: "OIDC user bound to cluster-admin", namespace: "tenant-foo", user: "cluster-admin-user", groups: []string{"cozystack-cluster-admin", "system:authenticated"}, denied: true, tenant: "foo", parent: "tenant-root"},
		{name: "cozy-system service account", namespace: "tenant-foo", user: "system:serviceaccount:cozy-system:cozystack-api", groups: []string{"system:serviceaccounts", "system:serviceaccounts:cozy-system", "system:authenticated"}, denied: true, tenant: "foo", parent: "tenant-root"},
		{name: "tenant service account", namespace: "tenant-foo-bar", user: "system:serviceaccount:tenant-foo:default", groups: []string{"system:serviceaccounts", "system:serviceaccounts:tenant-foo", "system:authenticated"}, denied: true, tenant: "bar", parent: "tenant-foo"},
		{name: "user without groups", namespace: "tenant-foo", user: "no-groups", denied: true, tenant: "foo", parent: "tenant-root"},
	}

	gvk := schema.GroupVersionKind{Version: "v1", Kind: "Namespace"}
	gvr := schema.GroupVersionResource{Version: "v1", Resource: "namespaces"}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			oldObj := &unstructured.Unstructured{Object: map[string]any{
				"apiVersion": "v1",
				"kind":       "Namespace",
				"metadata": map[string]any{
					"name":   tc.namespace,
					"labels": map[string]any{"tenant.cozystack.io/tenant-root": ""},
				},
			}}
			var newObj runtime.Object
			attrs := admission.NewAttributesRecord(newObj, oldObj, gvk, tc.namespace, tc.namespace, gvr, "", admission.Delete, &metav1.DeleteOptions{}, false, &user.DefaultInfo{Name: tc.user, Groups: tc.groups})
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
			for _, d := range denials {
				for _, want := range []string{
					"kubectl delete tenants.apps.cozystack.io " + tc.tenant + " --namespace " + tc.parent + ".",
					"kubectl label namespace " + tc.namespace + " tenant.cozystack.io/tenant-root-",
				} {
					if !strings.Contains(d.Message, want) {
						t.Errorf("denial message must carry %q, got %q", want, d.Message)
					}
				}
			}
		})
	}
}
