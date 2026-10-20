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

// Package model is the GPU logic shared by the binder, the admission
// webhooks and cozystack-api: the phase table, quota keys, the tenant
// ancestor walk, budgets, entitlement, capacity and node eligibility. It
// holds no client; callers build a Snapshot from their own caches.
package model

import gpuv1alpha1 "github.com/cozystack/cozystack/api/gpu/v1alpha1"

// HoldsGPUs reports whether a device in phase p holds units on a node.
func HoldsGPUs(p gpuv1alpha1.GPUDevicePhase) bool {
	return p == gpuv1alpha1.GPUDeviceBound || p == gpuv1alpha1.GPUDeviceAttached
}

// CountsAgainstBudget reports whether a device in phase p counts against
// GPU budgets. A Lost device counts so that its rebind never fails a
// budget check.
func CountsAgainstBudget(p gpuv1alpha1.GPUDevicePhase) bool {
	return HoldsGPUs(p) || p == gpuv1alpha1.GPUDeviceLost
}

// InUsageRecords reports whether a device in phase p has a usage record.
func InUsageRecords(p gpuv1alpha1.GPUDevicePhase) bool {
	return HoldsGPUs(p)
}
