/*
Copyright 2025 The Cozystack Authors.

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

package manifestutil

import (
	"context"
	"fmt"
	"time"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"
)

var crdGVK = schema.GroupVersionKind{
	Group:   "apiextensions.k8s.io",
	Version: "v1",
	Kind:    "CustomResourceDefinition",
}

// WaitForCRDsEstablished polls the API server until all named CRDs have the
// Established condition set to True, or the context is cancelled.
func WaitForCRDsEstablished(ctx context.Context, k8sClient client.Client, crdNames []string) error {
	if len(crdNames) == 0 {
		return nil
	}

	logger := log.FromContext(ctx)
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return fmt.Errorf("context cancelled while waiting for CRDs to be established: %w", ctx.Err())
		default:
		}

		allEstablished := true
		var pendingCRD string
		for _, name := range crdNames {
			crd := &unstructured.Unstructured{}
			crd.SetGroupVersionKind(crdGVK)
			if err := k8sClient.Get(ctx, types.NamespacedName{Name: name}, crd); err != nil {
				allEstablished = false
				pendingCRD = name
				break
			}

			conditions, found, err := unstructured.NestedSlice(crd.Object, "status", "conditions")
			if err != nil || !found {
				allEstablished = false
				pendingCRD = name
				break
			}

			established := false
			for _, c := range conditions {
				cond, ok := c.(map[string]interface{})
				if !ok {
					continue
				}
				if cond["type"] == "Established" && cond["status"] == "True" {
					established = true
					break
				}
			}
			if !established {
				allEstablished = false
				pendingCRD = name
				break
			}
		}

		if allEstablished {
			logger.Info("All CRDs established", "count", len(crdNames))
			return nil
		}

		logger.V(1).Info("Waiting for CRD to be established", "crd", pendingCRD)

		select {
		case <-ctx.Done():
			return fmt.Errorf("context cancelled while waiting for CRD %q to be established: %w", pendingCRD, ctx.Err())
		case <-ticker.C:
		}
	}
}

// CollectCRDNames returns the names of all CustomResourceDefinition objects
// from the given list of unstructured objects.
func CollectCRDNames(objects []*unstructured.Unstructured) []string {
	var names []string
	for _, obj := range objects {
		if obj.GetKind() == "CustomResourceDefinition" {
			names = append(names, obj.GetName())
		}
	}
	return names
}
