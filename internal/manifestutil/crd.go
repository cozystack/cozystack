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

	// The CRD named if the wait is cut short. Until a poll observes the set
	// nothing is known about which one lags, so the first of it stands in;
	// after that it holds the CRD the last poll stopped on.
	pendingCRD := crdNames[0]

	// Cancellation leaves the loop from two places and reads the same from
	// both, which is the point rather than an accident. When the deadline and
	// a tick come ready together, select picks between them at random, so any
	// difference between the two exits would reach the operator at random too.
	cancelled := func() error {
		return fmt.Errorf("context cancelled while waiting for CRD %q to be established: %w", pendingCRD, ctx.Err())
	}

	for {
		// Polling a dead context spends a Get to learn nothing: a client
		// refuses it, and the refusal would land on the first name in the list
		// whichever CRD is actually lagging, renaming what gets reported.
		if ctx.Err() != nil {
			return cancelled()
		}

		allEstablished := true
		for _, name := range crdNames {
			crd := &unstructured.Unstructured{}
			crd.SetGroupVersionKind(crdGVK)
			if err := k8sClient.Get(ctx, types.NamespacedName{Name: name}, crd); err != nil {
				// The poll was entered on a live context, so this is the CRD
				// it stopped on and not whichever name a blanket refusal
				// reached first. A context dying inside the poll still lands
				// here and does record the first name to fail: this branch
				// cannot tell a refused request from a CRD status, which is
				// the wider problem it has for RBAC and connection errors too.
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
			return cancelled()
		case <-ticker.C:
		}
	}
}

// CollectCRDNames returns the names of all CustomResourceDefinition objects
// from the given list of unstructured objects. Only objects with
// apiVersion "apiextensions.k8s.io/v1" and kind "CustomResourceDefinition"
// are matched.
func CollectCRDNames(objects []*unstructured.Unstructured) []string {
	var names []string
	for _, obj := range objects {
		if obj.GetAPIVersion() == "apiextensions.k8s.io/v1" && obj.GetKind() == "CustomResourceDefinition" {
			names = append(names, obj.GetName())
		}
	}
	return names
}
