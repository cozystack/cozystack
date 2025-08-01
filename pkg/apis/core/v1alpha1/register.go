// SPDX-License-Identifier: Apache-2.0
// Copyright 2025 The Cozystack Authors.

package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/klog/v2"
)

// -----------------------------------------------------------------------------
// Group / version boiler-plate
// -----------------------------------------------------------------------------

// GroupName is the API group for every resource in this package.
const GroupName = "core.cozystack.io"

// SchemeGroupVersion is the canonical {group,version} for v1alpha1.
var SchemeGroupVersion = schema.GroupVersion{Group: GroupName, Version: "v1alpha1"}

// -----------------------------------------------------------------------------
// Scheme registration helpers
// -----------------------------------------------------------------------------

var (
	// SchemeBuilder is used by generated deepcopy code.
	SchemeBuilder      runtime.SchemeBuilder
	localSchemeBuilder = &SchemeBuilder
	AddToScheme        = localSchemeBuilder.AddToScheme
)

func init() {
	// Manually-written types go here.  Generated deepcopy code is wired in
	// via `zz_generated.deepcopy.go`.
	localSchemeBuilder.Register(addKnownTypes)
}

// addKnownTypes is called from init().
func addKnownTypes(scheme *runtime.Scheme) error {
	metav1.AddToGroupVersion(scheme, SchemeGroupVersion)
	return nil
}

// Resource turns an unqualified resource name into a fully-qualified one.
func Resource(resource string) schema.GroupResource {
	return SchemeGroupVersion.WithResource(resource).GroupResource()
}

// -----------------------------------------------------------------------------
// Public helpers consumed by the apiserver wiring
// -----------------------------------------------------------------------------

// RegisterStaticTypes adds *compile-time* resources such as TenantNamespace.
func RegisterStaticTypes(scheme *runtime.Scheme) {
	scheme.AddKnownTypes(SchemeGroupVersion,
		&TenantNamespace{},
		&TenantNamespaceList{},
	)
	metav1.AddToGroupVersion(scheme, SchemeGroupVersion)
	klog.V(1).Info("Registered static kind: TenantNamespace")
}
