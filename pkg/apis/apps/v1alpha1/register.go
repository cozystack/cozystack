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

// GroupOrDefault resolves the API group an application resource is served
// in: an explicit group from ApplicationDefinition spec.application.group
// wins, empty falls back to the built-in apps.cozystack.io group. All
// consumers of config.ApplicationConfig.Group must resolve through this
// helper so the back-compat default lives in one place.
func GroupOrDefault(group string) string {
	if group == "" {
		return GroupName
	}
	return group
}

// RegisterDynamicTypes adds per-tenant “Application” kinds that are only known
// at runtime from ApplicationDefinitions. Kinds land in the group their
// ApplicationDefinition selected (default apps.cozystack.io); for every
// non-default group encountered the group-version itself is registered too
// (meta types and version priority), mirroring what install.Install does for
// the default group at init time.
func RegisterDynamicTypes(scheme *runtime.Scheme, cfg *config.ResourceConfig) error {
	registeredGVs := map[schema.GroupVersion]bool{SchemeGroupVersion: true}
	for _, res := range cfg.Resources {
		kind := res.Application.Kind
		gv := schema.GroupVersion{
			Group:   GroupOrDefault(res.Application.Group),
			Version: SchemeGroupVersion.Version,
		}

		if !registeredGVs[gv] {
			metav1.AddToGroupVersion(scheme, gv)
			if err := scheme.SetVersionPriority(gv); err != nil {
				return err
			}
			registeredGVs[gv] = true
		}

		gvk := gv.WithKind(kind)
		scheme.AddKnownTypeWithName(gvk, &Application{})
		scheme.AddKnownTypeWithName(gvk.GroupVersion().WithKind(kind+"List"), &ApplicationList{})

		gvkInternal := schema.GroupVersion{Group: gv.Group, Version: runtime.APIVersionInternal}.WithKind(kind)
		scheme.AddKnownTypeWithName(gvkInternal, &Application{})
		scheme.AddKnownTypeWithName(gvkInternal.GroupVersion().WithKind(kind+"List"), &ApplicationList{})

		klog.V(1).Infof("Registered dynamic kind: %s in group %s", kind, gv.Group)
	}
	return nil
}
