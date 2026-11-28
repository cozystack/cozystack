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
	"strings"
	"testing"

	apiextv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	appsv1alpha1 "github.com/cozystack/cozystack/pkg/apis/apps/v1alpha1"
)

// CAPI starts a new pool at its min-size annotation without comparing it with
// the max, so a pool with class entries whose minReplicas is above maxReplicas
// would run more GPUs than count times maxReplicas. Pools without class
// entries keep admitting min above max.
func TestWorkerMinReplicasAboveMaxIsRefused(t *testing.T) {
	registerTestSchemas(t)
	for _, tc := range []struct {
		name, raw string
		refused   bool
	}{
		{name: "class pool min above max", raw: `{"minReplicas":50,"maxReplicas":1,"gpus":[{"className":"h100"}]}`, refused: true},
		{name: "class pool min above default max", raw: `{"minReplicas":11,"gpus":[{"className":"h100"}]}`, refused: true},
		{name: "pool without GPUs keeps min above max", raw: `{"minReplicas":3,"maxReplicas":2}`},
		{name: "raw-name pool keeps min above max", raw: `{"minReplicas":3,"maxReplicas":2,"gpus":[{"name":"nvidia.com/GH100_H100"}]}`},
		{name: "whole-valued float min above max", raw: `{"minReplicas":5.0,"maxReplicas":1,"gpus":[{"className":"h100"}]}`, refused: true},
		{name: "string min above max", raw: `{"minReplicas":"5","maxReplicas":1,"gpus":[{"className":"h100"}]}`, refused: true},
		{name: "fractional min", raw: `{"minReplicas":4.5,"maxReplicas":10,"gpus":[{"className":"h100"}]}`, refused: true},
		{name: "non-numeric min", raw: `{"minReplicas":"x","maxReplicas":10,"gpus":[{"className":"h100"}]}`, refused: true},
		{name: "whole-valued float min equal max", raw: `{"minReplicas":3.0,"maxReplicas":3,"gpus":[{"className":"h100"}]}`},
		{name: "string min below max", raw: `{"minReplicas":"1","maxReplicas":3,"gpus":[{"className":"h100"}]}`},
		{name: "min equal max", raw: `{"minReplicas":2,"maxReplicas":2,"gpus":[{"className":"h100"}]}`},
		{name: "min below max", raw: `{"minReplicas":1,"maxReplicas":2,"gpus":[{"className":"h100"}]}`},
		{name: "defaults", raw: `{"gpus":[{"className":"h100"}]}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := newAppREST(t, kubernetesNodesKind, "kubernetes-nodes-", "kubernetesnodes", tenantHelmRelease(t, "root", rootTenantNamespace, nil))
			app := &appsv1alpha1.Application{
				ObjectMeta: metav1.ObjectMeta{Name: "pool", Namespace: rootTenantNamespace},
				Spec:       &apiextv1.JSON{Raw: []byte(tc.raw)},
			}
			errs := r.validateGPUBudget(t.Context(), app, nil)
			if !tc.refused {
				if len(errs) > 0 {
					t.Fatalf("refused: %v", errs)
				}
				return
			}
			if len(errs) == 0 || !strings.Contains(errs.ToAggregate().Error(), "minReplicas") {
				t.Fatalf("errs = %v, want a minReplicas refusal", errs)
			}
		})
	}
}

// A minReplicas the bounds check cannot read must not hide the pool's GPU
// demand from the pool accounting.
func TestStoredPoolWithUnreadableMinReplicasStillCounts(t *testing.T) {
	registerTestSchemas(t)
	stored := `{"minReplicas":"1","maxReplicas":3,"gpus":[{"className":"h100"}]}`
	got, err := gpuDemand(kubernetesNodesKind, []byte(stored))
	if err != nil || got[h100Key] != 3 {
		t.Fatalf("gpuDemand = %v, %v; want %s reserved 3", got, err, h100Key)
	}
	root := tenantHelmRelease(t, "root", rootTenantNamespace, map[string]string{h100Key: "4"})
	r := newVMInstanceREST(t, root, nodesRelease(t, "pool", rootTenantNamespace, stored))
	errs := r.validateGPUBudget(t.Context(), vmApplication("vm", rootTenantNamespace, `{"gpus":[{"className":"h100","count":2}]}`), nil)
	expectGPUErrs(t, errs, "3 is already reserved")
}
