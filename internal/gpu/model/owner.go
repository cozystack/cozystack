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
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	gpuv1alpha1 "github.com/cozystack/cozystack/api/gpu/v1alpha1"
)

// OwnedByClaim reports an object a GPUClaim owns, such as its usage record.
func OwnedByClaim(o metav1.Object) bool {
	for _, ref := range o.GetOwnerReferences() {
		if ref.APIVersion == gpuv1alpha1.GroupVersion.String() && ref.Kind == "GPUClaim" {
			return true
		}
	}
	return false
}
