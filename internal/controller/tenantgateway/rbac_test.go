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
	"os"
	"slices"
	"testing"

	rbacv1 "k8s.io/api/rbac/v1"
	"sigs.k8s.io/yaml"
)

// clusterRolePath is the ClusterRole the cozystack-controller chart
// ships. It is written by hand: the kubebuilder rbac markers in this
// package document the grants but generate nothing, because
// hack/update-codegen.sh runs controller-gen over ./api/... only and
// writes no rbac artifact anywhere. Nothing but this test connects the
// two.
const clusterRolePath = "../../../packages/system/cozystack-controller/templates/rbac.yaml"

// TestShippedClusterRoleNamesEveryTypeThisControllerReads pins that
// each type this reconciler watches or reads has a rule naming its API
// group, rather than reaching the apiserver only through the
// read-everything rule at the foot of the ClusterRole.
//
// An informer that starts without a grant fails with Forbidden and
// takes the manager down with it, and the failure arrives whenever
// someone narrows that catch-all, long after the watch that needed it
// was added. A rule naming the group is what survives that edit.
//
// Resource wildcards inside a named group are accepted: the rule
// covering this project's own CRDs is written that way, and narrowing
// the catch-all does not touch it. An apiGroups entry of "*" is not,
// because it is the rule this test exists to stop the controller
// depending on.
//
// The template is parsed as YAML rather than rendered, which holds
// because it carries no Helm actions. Putting one in it, a
// {{/* */}} comment included, means rendering the chart here instead.
func TestShippedClusterRoleNamesEveryTypeThisControllerReads(t *testing.T) {
	needed := []struct {
		group    string
		resource string
	}{
		{"gateway.cozystack.io", "tenantgateways"},
		{"gateway.networking.k8s.io", "gateways"},
		{"gateway.networking.k8s.io", "httproutes"},
		{"gateway.networking.k8s.io", "tlsroutes"},
		{"gateway.networking.k8s.io", "referencegrants"},
		{"cert-manager.io", "certificates"},
		{"cert-manager.io", "issuers"},
		{"", "namespaces"},
		{"", "services"},
	}

	raw, err := os.ReadFile(clusterRolePath)
	if err != nil {
		t.Fatalf("read ClusterRole: %v", err)
	}
	var role rbacv1.ClusterRole
	if err := yaml.Unmarshal(raw, &role); err != nil {
		t.Fatalf("unmarshal ClusterRole: %v", err)
	}
	if len(role.Rules) == 0 {
		t.Fatal("ClusterRole carries no rules; the path or the document shape changed")
	}

	for _, want := range needed {
		granted := false
		for _, rule := range role.Rules {
			if !slices.Contains(rule.APIGroups, want.group) {
				continue
			}
			if !slices.Contains(rule.Resources, want.resource) && !slices.Contains(rule.Resources, "*") {
				continue
			}
			if !slices.Contains(rule.Verbs, "*") {
				missing := false
				for _, verb := range []string{"get", "list", "watch"} {
					if !slices.Contains(rule.Verbs, verb) {
						missing = true
					}
				}
				if missing {
					continue
				}
			}
			granted = true
			break
		}
		if !granted {
			t.Errorf("no rule names apiGroup %q resource %q with get/list/watch; the controller reaches it only through the read-everything rule, and narrowing that breaks the informer with Forbidden", want.group, want.resource)
		}
	}
}
