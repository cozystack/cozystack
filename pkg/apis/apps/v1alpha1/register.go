// SPDX-License-Identifier: Apache-2.0
// Copyright 2025 The Cozystack Authors.

package v1alpha1

import (
	"github.com/cozystack/cozystack/pkg/config"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/klog/v2"
)

// -----------------------------------------------------------------------------
// Group / version boiler-plate
// -----------------------------------------------------------------------------

// GroupName is the API group for every resource in this package.
const GroupName = "apps.cozystack.io"

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

// RegisterDynamicTypes adds per-tenant “Application” kinds that are only known
// at runtime from a config file.
func RegisterDynamicTypes(scheme *runtime.Scheme, cfg *config.ResourceConfig) error {
	for _, res := range cfg.Resources {
		kind := res.Application.Kind

		gvk := SchemeGroupVersion.WithKind(kind)
		scheme.AddKnownTypeWithName(gvk, &Application{})
		scheme.AddKnownTypeWithName(gvk.GroupVersion().WithKind(kind+"List"), &ApplicationList{})

		gvkInternal := schema.GroupVersion{Group: GroupName, Version: runtime.APIVersionInternal}.WithKind(kind)
		scheme.AddKnownTypeWithName(gvkInternal, &Application{})
		scheme.AddKnownTypeWithName(gvkInternal.GroupVersion().WithKind(kind+"List"), &ApplicationList{})

		klog.V(1).Infof("Registered dynamic kind: %s", kind)
	}
	return nil
}
