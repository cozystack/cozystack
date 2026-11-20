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
	"k8s.io/apimachinery/pkg/util/validation"

	"github.com/cozystack/cozystack/internal/gpu"
)

// QuotaKey is a parsed GPU budget key.
type QuotaKey struct {
	// Class is the class the key limits; empty when Total is set.
	Class string
	Total bool
}

// ParseQuotaKey parses a resourceQuotas key of the form
// <class>.gpuclass.cozystack.io/devices or gpuclass.cozystack.io/devices.
func ParseQuotaKey(key corev1.ResourceName) (QuotaKey, bool) {
	if key == gpu.QuotaKeyTotal {
		return QuotaKey{Total: true}, true
	}
	class, ok := strings.CutSuffix(string(key), "."+gpu.QuotaKeyTotal)
	if !ok || len(validation.IsDNS1123Label(class)) > 0 {
		return QuotaKey{}, false
	}
	return QuotaKey{Class: class}, true
}

// ClassQuotaKey is the budget key of one class.
func ClassQuotaKey(class string) corev1.ResourceName {
	return corev1.ResourceName(class + "." + gpu.QuotaKeyTotal)
}

// IsGPUQuotaKey reports whether key is in the GPU budget domain,
// well-formed or not. Such keys are not resource names a pod requests, so
// they stay out of the namespace ResourceQuota.
func IsGPUQuotaKey(key corev1.ResourceName) bool {
	domain, _, _ := strings.Cut(string(key), "/")
	return domain == gpu.QuotaKeyDomain || strings.HasSuffix(domain, "."+gpu.QuotaKeyDomain)
}
