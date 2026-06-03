package fluxshardoperator

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

// The operator never decodes HelmRelease specs or statuses: it watches and
// patches metadata only (labels, deletionTimestamp), so the cache holds tiny
// metadata stubs instead of full objects and does not decode the
// helm-controller status-patch firehose.

// HelmReleaseGVK is the GroupVersionKind of Flux HelmReleases.
var HelmReleaseGVK = schema.GroupVersionKind{
	Group:   "helm.toolkit.fluxcd.io",
	Version: "v2",
	Kind:    "HelmRelease",
}

// NamespaceGVK is the GroupVersionKind of core Namespaces.
var NamespaceGVK = schema.GroupVersionKind{Version: "v1", Kind: "Namespace"}

// HelmReleaseMeta returns a PartialObjectMetadata typed as a HelmRelease.
func HelmReleaseMeta() *metav1.PartialObjectMetadata {
	obj := &metav1.PartialObjectMetadata{}
	obj.SetGroupVersionKind(HelmReleaseGVK)
	return obj
}

// HelmReleaseMetaList returns a PartialObjectMetadataList typed as a
// HelmReleaseList.
func HelmReleaseMetaList() *metav1.PartialObjectMetadataList {
	list := &metav1.PartialObjectMetadataList{}
	list.SetGroupVersionKind(HelmReleaseGVK.GroupVersion().WithKind("HelmReleaseList"))
	return list
}

// NamespaceMeta returns a PartialObjectMetadata typed as a Namespace.
func NamespaceMeta() *metav1.PartialObjectMetadata {
	obj := &metav1.PartialObjectMetadata{}
	obj.SetGroupVersionKind(NamespaceGVK)
	return obj
}

// NamespaceMetaList returns a PartialObjectMetadataList typed as a
// NamespaceList.
func NamespaceMetaList() *metav1.PartialObjectMetadataList {
	list := &metav1.PartialObjectMetadataList{}
	list.SetGroupVersionKind(NamespaceGVK.GroupVersion().WithKind("NamespaceList"))
	return list
}
