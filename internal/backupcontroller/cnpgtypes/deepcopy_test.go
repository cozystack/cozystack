// SPDX-License-Identifier: Apache-2.0

package cnpgtypes

import (
	"testing"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
)

func TestObjectStoreDeepCopySidecarResources(t *testing.T) {
	original := &ObjectStore{Spec: ObjectStoreSpec{
		InstanceSidecarConfiguration: &InstanceSidecarConfiguration{
			Resources: corev1.ResourceRequirements{
				Requests: corev1.ResourceList{corev1.ResourceMemory: resource.MustParse("256Mi")},
				Limits:   corev1.ResourceList{corev1.ResourceMemory: resource.MustParse("1Gi")},
			},
			Env: []EnvVar{{Name: "AWS_REQUEST_CHECKSUM_CALCULATION", Value: "when_required"}},
		},
	}}
	copy := original.DeepCopy()
	copy.Spec.InstanceSidecarConfiguration.Resources.Requests[corev1.ResourceMemory] = resource.MustParse("512Mi")
	copy.Spec.InstanceSidecarConfiguration.Resources.Limits[corev1.ResourceMemory] = resource.MustParse("2Gi")
	copy.Spec.InstanceSidecarConfiguration.Env[0].Value = "changed"
	got := original.Spec.InstanceSidecarConfiguration
	if got.Resources.Requests.Memory().String() != "256Mi" || got.Resources.Limits.Memory().String() != "1Gi" || got.Env[0].Value != "when_required" {
		t.Fatalf("mutating the copy changed the original sidecar configuration: %+v", got)
	}
}
