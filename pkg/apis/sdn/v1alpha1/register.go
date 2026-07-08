// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Cozystack Authors.

package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

// -----------------------------------------------------------------------------
// Group / version boiler-plate
// -----------------------------------------------------------------------------

// GroupName is the API group for every resource in this package.
const GroupName = "sdn.cozystack.io"

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

// addKnownTypes registers the SecurityGroup kinds and group-version meta. It is
// wired into AddToScheme via the SchemeBuilder, so any scheme built through
// Install (the apiserver, the roundtrip tests) recognizes the concrete types —
// not just the server-start path.
func addKnownTypes(scheme *runtime.Scheme) error {
	scheme.AddKnownTypes(SchemeGroupVersion,
		&SecurityGroup{},
		&SecurityGroupList{},
	)
	metav1.AddToGroupVersion(scheme, SchemeGroupVersion)
	return nil
}

// Resource turns an unqualified resource name into a fully-qualified one.
func Resource(resource string) schema.GroupResource {
	return SchemeGroupVersion.WithResource(resource).GroupResource()
}
