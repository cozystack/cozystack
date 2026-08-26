// SPDX-License-Identifier: Apache-2.0

// Package migrationcontroller reconciles the forklift.cozystack.io API group:
// VMImportSource connections and the VMImportTask operations that run over them.
//
// Forklift, KubeVirt and CDI objects are handled as unstructured rather than
// through generated types. That is deliberate: those projects' Go modules are
// heavy transitive dependencies, Cozystack drives only a handful of their
// fields, and Forklift in particular is an optional package that may not be
// installed at all — an unstructured client degrades to a clean NotFound
// instead of forcing the scheme to carry types for CRDs that are absent.
package migrationcontroller

import (
	"strings"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
)

// Forklift's API group. Cozystack pins the operator package to v2.11.x, which
// serves v1beta1 for every kind used here.
const (
	forkliftGroup   = "forklift.konveyor.io"
	forkliftVersion = "v1beta1"
)

// Forklift kinds this controller creates or reads.
var (
	providerGVK   = forkliftGVK("Provider")
	planGVK       = forkliftGVK("Plan")
	migrationGVK  = forkliftGVK("Migration")
	networkMapGVK = forkliftGVK("NetworkMap")
	storageMapGVK = forkliftGVK("StorageMap")
)

// KubeVirt and CDI kinds this controller reads and cleans up. The
// VirtualMachine Forklift creates is never started: it is read for the source
// VM's shape (CPU, memory, firmware, disk order) and then discarded, which is
// why this controller needs no inventory client of its own.
var (
	virtualMachineGVK = schema.GroupVersionKind{Group: "kubevirt.io", Version: "v1", Kind: "VirtualMachine"}
	dataVolumeGVK     = schema.GroupVersionKind{Group: "cdi.kubevirt.io", Version: "v1beta1", Kind: "DataVolume"}
)

// Cozystack application kinds the controller produces. These are projections
// over HelmReleases: creating one through the aggregated API is exactly what a
// tenant does in the dashboard, so the controller takes the same path rather
// than writing HelmReleases behind the projection's back.
var (
	vmDiskGVK     = appsGVK("VMDisk")
	vmInstanceGVK = appsGVK("VMInstance")
)

func forkliftGVK(kind string) schema.GroupVersionKind {
	return schema.GroupVersionKind{Group: forkliftGroup, Version: forkliftVersion, Kind: kind}
}

func appsGVK(kind string) schema.GroupVersionKind {
	return schema.GroupVersionKind{Group: "apps.cozystack.io", Version: "v1alpha1", Kind: kind}
}

// newForkliftObject returns an empty unstructured object of the given kind,
// ready to Get into or to fill in and Create.
func newObject(gvk schema.GroupVersionKind) *unstructured.Unstructured {
	u := &unstructured.Unstructured{}
	u.SetGroupVersionKind(gvk)
	return u
}

// sourceProviderName is the name of the Forklift Provider that stands for a
// VMImportSource. It is derived from the Source rather than random so a
// reconcile after a controller restart finds the same object.
func sourceProviderName(sourceName string) string {
	return "vmimport-" + sourceName
}

// destinationProviderName is the name of the Forklift Provider that stands for
// this cluster. Forklift models the local cluster as an "openshift"-type
// provider with an empty URL; the type name is Forklift's, not a statement
// about what Cozystack runs on.
func destinationProviderName(sourceName string) string {
	return "vmimport-" + sourceName + "-destination"
}

// credentialsSecretName is the name of the Secret the controller projects a
// Source's credentials into for Forklift to consume.
func credentialsSecretName(sourceName string) string {
	return "vmimport-" + sourceName + "-credentials"
}

// planName is the name of the Forklift Plan that migrates one source VM for a
// task. One Plan per VM keeps a single VM's failure from stalling its siblings.
func planName(taskName, vmID string) string {
	return taskName + "-" + sanitizeName(vmID)
}

// mapName is the name of the Network/StorageMap a task owns. Both maps are
// per-task: their content is identical for every VM in the task.
func mapName(taskName string) string {
	return "vmimport-" + taskName
}

// sanitizeName turns a provider-side identifier (`vm-1234`, and on other
// providers a UUID or a path) into something usable as a Kubernetes name
// segment. Managed-object references are already lowercase alphanumerics with
// dashes, but the API must not depend on that.
func sanitizeName(in string) string {
	out := make([]rune, 0, len(in))
	for _, r := range in {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '-':
			out = append(out, r)
		case r >= 'A' && r <= 'Z':
			out = append(out, r+('a'-'A'))
		default:
			out = append(out, '-')
		}
	}
	// A leading or trailing dash is not a valid name segment.
	trimmed := strings.Trim(string(out), "-")
	if trimmed == "" {
		return "unnamed"
	}
	if len(trimmed) > 40 {
		trimmed = strings.Trim(trimmed[:40], "-")
	}
	return trimmed
}

// ownerRef returns a controller owner reference for the given object. Every
// piece of migration scaffolding carries one, so deleting the owning Source or
// Task garbage-collects it; the imported VMDisks and VMInstances deliberately
// carry none, which is what makes them outlive the task that produced them.
func ownerRef(gvk schema.GroupVersionKind, name string, uid types.UID) metav1.OwnerReference {
	controller := true
	blockOwnerDeletion := true
	return metav1.OwnerReference{
		APIVersion:         gvk.GroupVersion().String(),
		Kind:               gvk.Kind,
		Name:               name,
		UID:                uid,
		Controller:         &controller,
		BlockOwnerDeletion: &blockOwnerDeletion,
	}
}
