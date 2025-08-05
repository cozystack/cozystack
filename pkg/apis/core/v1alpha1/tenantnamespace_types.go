// SPDX-License-Identifier: Apache-2.0
// Copyright 2025 The Cozystack Authors.

// This file contains the cluster-scoped “TenantNamespace” resource.
// A TenantNamespace represents an existing Kubernetes Namespace whose
// *name* starts with the prefix “tenant-”.

package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object

// TenantNamespace is a thin wrapper around ObjectMeta.  It has no spec/status
// because it merely reflects an existing Namespace object.
type TenantNamespace struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`
}

// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object

// TenantNamespaceList is the list variant for TenantNamespace.
type TenantNamespaceList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []TenantNamespace `json:"items"`
}
