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

package model

import (
	"strings"

	corev1 "k8s.io/api/core/v1"

	"github.com/cozystack/cozystack/internal/gpu"
)

// QuotaKey is a parsed gpu.cozystack.io budget key.
type QuotaKey struct {
	// Class is the class the key limits; empty when Total is set.
	Class string
	Total bool
}

// ParseQuotaKey parses a resourceQuotas key of the form
// gpu.cozystack.io/<class> or gpu.cozystack.io/total.
func ParseQuotaKey(key corev1.ResourceName) (QuotaKey, bool) {
	rest, ok := strings.CutPrefix(string(key), gpu.QuotaKeyPrefix)
	if !ok || rest == "" || strings.Contains(rest, "/") {
		return QuotaKey{}, false
	}
	if rest == gpu.QuotaTotal {
		return QuotaKey{Total: true}, true
	}
	return QuotaKey{Class: rest}, true
}

// ClassQuotaKey is the budget key of one class.
func ClassQuotaKey(class string) corev1.ResourceName {
	return corev1.ResourceName(gpu.QuotaKeyPrefix + class)
}

// IsGPUQuotaKey reports whether key is in the gpu.cozystack.io namespace,
// well-formed or not. Such keys are budgets of GPUClaim, never pod
// quota, so they stay out of the namespace ResourceQuota, the declaration
// gate and the quota pool model.
func IsGPUQuotaKey(key corev1.ResourceName) bool {
	return strings.HasPrefix(string(key), gpu.QuotaKeyPrefix)
}

// WithoutGPUQuotaKeys returns a copy of quota without GPU budget keys.
func WithoutGPUQuotaKeys(quota corev1.ResourceList) corev1.ResourceList {
	out := corev1.ResourceList{}
	for k, v := range quota {
		if !IsGPUQuotaKey(k) {
			out[k] = v
		}
	}
	return out
}

// RawQuotaLimit returns the limit a tenant's resourceQuotas puts on the
// GPU resource name directly (a raw key), in any of the forms a quota
// accepts for an extended resource. When more than one form is present
// the smallest wins, as each caps the same pods.
func RawQuotaLimit(quota corev1.ResourceList, name corev1.ResourceName) (int64, bool) {
	var limit int64
	found := false
	for _, k := range []corev1.ResourceName{name, "requests." + name, "limits." + name} {
		q, ok := quota[k]
		if !ok {
			continue
		}
		if v := q.Value(); !found || v < limit {
			limit = v
		}
		found = true
	}
	return limit, found
}
