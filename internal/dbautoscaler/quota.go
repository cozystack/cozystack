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

package dbautoscaler

import (
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
)

// presetLadder mirrors cozy-lib's resource presets
// (packages/library/cozy-lib/templates/_resourcepresets.tpl). It is compiled in
// so the operator needs no cluster read of preset definitions (per the proposal
// Security section). Values are {cpu, memory}.
var presetLadder = map[string][2]string{
	"t1.nano": {"250m", "128Mi"}, "t1.micro": {"500m", "256Mi"}, "t1.small": {"1", "512Mi"},
	"t1.medium": {"2", "1Gi"}, "t1.large": {"4", "2Gi"}, "t1.xlarge": {"8", "4Gi"},
	"t1.2xlarge": {"16", "8Gi"}, "t1.4xlarge": {"32", "16Gi"},
	"c1.nano": {"250m", "256Mi"}, "c1.micro": {"500m", "512Mi"}, "c1.small": {"1", "1Gi"},
	"c1.medium": {"2", "2Gi"}, "c1.large": {"4", "4Gi"}, "c1.xlarge": {"8", "8Gi"},
	"c1.2xlarge": {"16", "16Gi"}, "c1.4xlarge": {"32", "32Gi"},
	"s1.nano": {"250m", "512Mi"}, "s1.micro": {"500m", "1Gi"}, "s1.small": {"1", "2Gi"},
	"s1.medium": {"2", "4Gi"}, "s1.large": {"4", "8Gi"}, "s1.xlarge": {"8", "16Gi"},
	"s1.2xlarge": {"16", "32Gi"}, "s1.4xlarge": {"32", "64Gi"},
	"u1.nano": {"250m", "1Gi"}, "u1.micro": {"500m", "2Gi"}, "u1.small": {"1", "4Gi"},
	"u1.medium": {"2", "8Gi"}, "u1.large": {"4", "16Gi"}, "u1.xlarge": {"8", "32Gi"},
	"u1.2xlarge": {"16", "64Gi"}, "u1.4xlarge": {"32", "128Gi"},
	"m1.nano": {"250m", "2Gi"}, "m1.micro": {"500m", "4Gi"}, "m1.small": {"1", "8Gi"},
	"m1.medium": {"2", "16Gi"}, "m1.large": {"4", "32Gi"}, "m1.xlarge": {"8", "64Gi"},
	"m1.2xlarge": {"16", "128Gi"}, "m1.4xlarge": {"32", "256Gi"},
	// Deprecated legacy aliases (kept for values written before the rename).
	"nano": {"250m", "128Mi"}, "micro": {"500m", "256Mi"}, "small": {"1", "512Mi"},
	"medium": {"1", "1Gi"}, "large": {"2", "2Gi"}, "xlarge": {"4", "4Gi"}, "2xlarge": {"8", "8Gi"},
}

// PerPodResources returns the CPU and memory request of a single database
// instance, from explicit resources when set, otherwise from resourcesPreset.
// ok is false when neither is resolvable.
func PerPodResources(appValues map[string]any) (cpu, mem resource.Quantity, ok bool) {
	if res, isMap := appValues["resources"].(map[string]any); isMap {
		cpuStr, cok := res["cpu"].(string)
		memStr, mok := res["memory"].(string)
		if cok && mok && cpuStr != "" && memStr != "" {
			cq, cerr := resource.ParseQuantity(cpuStr)
			mq, merr := resource.ParseQuantity(memStr)
			if cerr == nil && merr == nil {
				return cq, mq, true
			}
		}
	}
	preset, _ := appValues["resourcesPreset"].(string)
	if preset == "" {
		preset = "t1.micro"
	}
	pair, found := presetLadder[preset]
	if !found {
		return resource.Quantity{}, resource.Quantity{}, false
	}
	return resource.MustParse(pair[0]), resource.MustParse(pair[1]), true
}

// MaxReplicasWithinQuota returns the largest total instance count that fits the
// tenant quotas, given the per-pod request and the currently observed count. It
// returns nil when no quota bounds cpu/memory (unbounded / unknown). The check is
// advisory: a concurrent allocation can consume quota between check and pod
// creation, which is why the reconciler still guards the StuckScaling path.
func MaxReplicasWithinQuota(quotas []corev1.ResourceQuota, current int32, cpuPerPod, memPerPod resource.Quantity) *int32 {
	bounded := false
	best := int32(1 << 30)

	consider := func(hardName, usedFromName corev1.ResourceName, perPod resource.Quantity) {
		perMilli := perPod.MilliValue()
		if perMilli <= 0 {
			return
		}
		for i := range quotas {
			q := &quotas[i]
			hard, hasHard := q.Status.Hard[hardName]
			if !hasHard {
				continue
			}
			used := q.Status.Used[usedFromName]
			headroom := hard.MilliValue() - used.MilliValue()
			if headroom < 0 {
				headroom = 0
			}
			additional := int32(headroom / perMilli)
			total := current + additional
			if total < best {
				best = total
			}
			bounded = true
		}
	}

	// A quota may bound either the request-scoped name (requests.cpu) or the bare
	// compute name (cpu); check both.
	consider(corev1.ResourceRequestsCPU, corev1.ResourceRequestsCPU, cpuPerPod)
	consider(corev1.ResourceCPU, corev1.ResourceCPU, cpuPerPod)
	consider(corev1.ResourceRequestsMemory, corev1.ResourceRequestsMemory, memPerPod)
	consider(corev1.ResourceMemory, corev1.ResourceMemory, memPerPod)

	if !bounded {
		return nil
	}
	return &best
}
