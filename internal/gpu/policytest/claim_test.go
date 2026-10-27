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

package policytest

import (
	"slices"
	"strings"
	"testing"

	admissionregistrationv1 "k8s.io/api/admissionregistration/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apiserver/pkg/authentication/user"

	"github.com/cozystack/cozystack/internal/gpu"
)

const (
	claimPolicyFile = "gpu-claim-tamper-policy.yaml"
	claimTamper     = "cozystack-gpu-claim-tamper"
)

func claim(finalizers []string, labels map[string]string) *unstructured.Unstructured {
	u := obj(claimGVK.GroupVersion().String(), claimGVK.Kind, "tenant-acme", "train-0", map[string]any{
		"spec": map[string]any{"className": "l40s-24c", "count": int64(1)},
	})
	if finalizers != nil {
		u.SetFinalizers(finalizers)
	}
	if labels != nil {
		u.SetLabels(labels)
	}
	return u
}

func TestClaimTamperMatch(t *testing.T) {
	p := get(t, claimPolicyFile, claimTamper).policy
	rules := p.Spec.MatchConstraints.ResourceRules
	if len(rules) != 1 {
		t.Fatalf("want one resource rule, got %d", len(rules))
	}
	r := rules[0]
	if !slices.Equal(r.APIGroups, []string{gpu.Group}) || !slices.Equal(r.Resources, []string{"gpuclaims"}) {
		t.Errorf("rule = %+v, want gpuclaims in %s", r, gpu.Group)
	}
	ops := []admissionregistrationv1.OperationType{admissionregistrationv1.Create, admissionregistrationv1.Update}
	if !slices.Equal(r.Operations, ops) {
		t.Errorf("operations = %v, want %v", r.Operations, ops)
	}
}

func TestClaimTamper(t *testing.T) {
	v := compile(t, get(t, claimPolicyFile, claimTamper).policy)
	held := []string{gpu.FinalizerClaim}
	mark := map[string]string{gpu.LabelTemplateClaim: gpu.LabelValueTrue}
	gcSA := &user.DefaultInfo{Name: "system:serviceaccount:kube-system:generic-garbage-collector", Groups: []string{"system:serviceaccounts", "system:serviceaccounts:kube-system", "system:authenticated"}}
	otherPlatformSA := &user.DefaultInfo{Name: "system:serviceaccount:cozy-system:cozystack-api", Groups: []string{"system:serviceaccounts", "system:serviceaccounts:cozy-system", "system:authenticated"}}
	impostorSA := &user.DefaultInfo{Name: "system:serviceaccount:tenant-acme:cozystack-controller", Groups: []string{"system:serviceaccounts", "system:serviceaccounts:tenant-acme", "system:authenticated"}}
	kcm := &user.DefaultInfo{Name: "system:kube-controller-manager", Groups: []string{"system:authenticated"}}

	cases := []struct {
		name     string
		old, new *unstructured.Unstructured
		user     user.Info
		denied   bool
	}{
		{name: "tenant orders a claim", new: claim(nil, nil), user: tenantUser},
		{name: "tenant labels a claim for a pool", old: claim(held, nil), new: claim(held, map[string]string{"pool": "inference"}), user: tenantUser},
		{name: "tenant edits a held claim without touching finalizers", old: claim(held, nil), new: claim(held, nil), user: tenantUser},
		{name: "tenant removes the claim finalizer", old: claim(held, nil), new: claim([]string{}, nil), user: tenantUser, denied: true},
		{name: "tenant drops the finalizer list", old: claim(held, nil), new: claim(nil, nil), user: tenantUser, denied: true},
		{name: "tenant adds a finalizer", old: claim(held, nil), new: claim(append(slices.Clone(held), "example.com/keep"), nil), user: tenantUser, denied: true},
		{name: "tenant orders a claim with the controller's finalizer", new: claim(held, nil), user: tenantUser, denied: true},
		{name: "tenant orders a claim with a finalizer of its own", new: claim([]string{"example.com/keep"}, nil), user: tenantUser, denied: true},
		{name: "tenant service account removes the claim finalizer", old: claim(held, nil), new: claim([]string{}, nil), user: tenantNamespaced, denied: true},
		{name: "tenant reorders finalizers", old: claim([]string{gpu.FinalizerClaim, "orphan"}, nil), new: claim([]string{"orphan", gpu.FinalizerClaim}, nil), user: tenantUser},
		// The API server and the garbage collector own these two.
		{name: "foreground deletion finalizer comes and goes", old: claim(held, nil), new: claim(append(slices.Clone(held), "foregroundDeletion"), nil), user: tenantUser},
		{name: "orphan finalizer removed", old: claim(append(slices.Clone(held), "orphan"), nil), new: claim(held, nil), user: tenantUser},
		{name: "controller adds the claim finalizer", old: claim(nil, nil), new: claim(held, nil), user: controllerSA},
		{name: "controller removes the claim finalizer", old: claim(held, nil), new: claim([]string{}, nil), user: controllerSA},
		{name: "garbage collector removes a finalizer", old: claim(append(slices.Clone(held), "foregroundDeletion"), nil), new: claim(held, nil), user: gcSA},
		{name: "controller manager removes a finalizer", old: claim(held, nil), new: claim([]string{}, nil), user: kcm},
		{name: "cluster admin removes the claim finalizer", old: claim(held, nil), new: claim([]string{}, nil), user: clusterAdmin},
		{name: "tenant orders a claim with the template mark", new: claim(nil, mark), user: tenantUser, denied: true},
		{name: "tenant adds the template mark", old: claim(held, nil), new: claim(held, mark), user: tenantUser, denied: true},
		{name: "tenant removes the template mark", old: claim(held, mark), new: claim(held, map[string]string{}), user: tenantUser, denied: true},
		{name: "tenant changes the template mark", old: claim(held, mark), new: claim(held, map[string]string{gpu.LabelTemplateClaim: "false"}), user: tenantUser, denied: true},
		{name: "tenant edits other labels of a template claim", old: claim(held, mark), new: claim(held, map[string]string{gpu.LabelTemplateClaim: gpu.LabelValueTrue, "team": "ml"}), user: tenantUser},
		{name: "controller creates a template claim", new: claim(nil, mark), user: controllerSA},
		// Platform callers are trusted by namespace group, not by name.
		{name: "another cozy-system service account removes the claim finalizer", old: claim(held, nil), new: claim([]string{}, nil), user: otherPlatformSA},
		{name: "tenant service account named like the controller removes the claim finalizer", old: claim(held, nil), new: claim([]string{}, nil), user: impostorSA, denied: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			denials := evaluate(t, v, request{gvk: claimGVK, resource: "gpuclaims", namespace: "tenant-acme", obj: tc.new, old: tc.old, user: tc.user})
			if got := len(denials) > 0; got != tc.denied {
				t.Fatalf("denied = %v, want %v (%v)", got, tc.denied, denials)
			}
			for _, m := range denials {
				if !strings.Contains(m, "GPUClaim train-0") {
					t.Errorf("message must name the claim, got %q", m)
				}
			}
		})
	}
}
