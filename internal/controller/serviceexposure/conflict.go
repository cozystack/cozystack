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

package serviceexposure

import (
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

const (
	// managedByLabel / managedByValue mark every CR this controller
	// renders. Reused for the take-over guard and garbage collection.
	// The value is specific to this controller (distinct from the shared
	// cozystack-controller binary) so `kubectl get -l` triage is clear.
	managedByLabel = "cozystack.io/managed-by"
	managedByValue = "serviceexposure-controller"

	// ownerClassAnnotation records which ExposureClass owns a rendered CR.
	// The pool and announcer are class-level (one per class), so ownership
	// is by class, not by individual ServiceExposure. OwnerReferences
	// cannot be used: the Cilium CRs are cluster-scoped and the MetalLB CRs
	// live in cozy-metallb, while the class is cluster-scoped — so a real
	// ownerRef back to a namespaced ServiceExposure is impossible anyway.
	ownerClassAnnotation = "serviceexposure.network.cozystack.io/owner-class"

	// exposureFinalizer guards cross-scope cleanup: because the rendered
	// CRs carry no ownerRef, Kubernetes garbage collection will not remove
	// them when the last ServiceExposure of a class is deleted. The
	// controller does it.
	exposureFinalizer = "network.cozystack.io/serviceexposure-cleanup"
)

// stamp adds the management label and owner-class annotation to a CR.
func stamp(obj *unstructured.Unstructured, className string) {
	labels := obj.GetLabels()
	if labels == nil {
		labels = map[string]string{}
	}
	labels[managedByLabel] = managedByValue
	obj.SetLabels(labels)

	ann := obj.GetAnnotations()
	if ann == nil {
		ann = map[string]string{}
	}
	ann[ownerClassAnnotation] = className
	obj.SetAnnotations(ann)
}

// managedByController reports whether a CR carries this controller's
// management label. A same-named CR lacking it is operator-owned and must
// not be taken over.
func managedByController(obj *unstructured.Unstructured) bool {
	return obj.GetLabels()[managedByLabel] == managedByValue
}

// ownedByClass reports whether a managed CR belongs to the given class.
func ownedByClass(obj *unstructured.Unstructured, className string) bool {
	if !managedByController(obj) {
		return false
	}
	return obj.GetAnnotations()[ownerClassAnnotation] == className
}

// objectKey is a stable identity for a rendered CR across reconciles.
type objectKey struct {
	apiVersion string
	kind       string
	namespace  string
	name       string
}

func keyOf(obj *unstructured.Unstructured) objectKey {
	return objectKey{
		apiVersion: obj.GetAPIVersion(),
		kind:       obj.GetKind(),
		namespace:  obj.GetNamespace(),
		name:       obj.GetName(),
	}
}

// backendCRDMissing reports whether an API error means the backend's CRD
// is not installed in the cluster (or not registered in a test scheme),
// so the controller can surface a clear BackendUnavailable reason instead
// of a generic failure. Covers both the live-cluster RESTMapper miss and
// the scheme miss the fake client raises.
func backendCRDMissing(err error) bool {
	return meta.IsNoMatchError(err) || runtime.IsNotRegisteredError(err)
}

// managedListFor returns an empty UnstructuredList typed to the List GVK
// of the given managed kind, ready for a labelled List call.
func managedListFor(gvk schema.GroupVersionKind) *unstructured.UnstructuredList {
	list := &unstructured.UnstructuredList{}
	list.SetGroupVersionKind(schema.GroupVersionKind{
		Group:   gvk.Group,
		Version: gvk.Version,
		Kind:    gvk.Kind + "List",
	})
	return list
}
