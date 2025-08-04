// SPDX-License-Identifier: Apache-2.0
package v1alpha1

import metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object

type TenantSecret struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	// Same semantics as core/v1 Secret.
	Type       string            `json:"type,omitempty"`
	Data       map[string][]byte `json:"data,omitempty"`
	StringData map[string]string `json:"stringData,omitempty"` // write-only hint
}

// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object

type TenantSecretList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []TenantSecret `json:"items"`
}
