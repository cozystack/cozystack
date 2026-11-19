// SPDX-License-Identifier: Apache-2.0
package v1alpha1

import metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object

// Option is a read-only, virtual resource that serves a named list of
// dropdown options for the dashboard. metadata.name is the source name
// (e.g. "gpu", "instancetype"); the items are computed on read by the
// apiserver from cluster state, so tenants need no direct access to the
// underlying resources.
type Option struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec OptionSpec `json:"spec,omitempty"`
}

// OptionSpec holds the computed option items for a source.
type OptionSpec struct {
	Items []OptionItem `json:"items,omitempty"`
}

// OptionItem is a single selectable option.
type OptionItem struct {
	// Value is the string written into the target field.
	Value string `json:"value"`
	// Label is an optional human-friendly title; defaults to Value in the UI.
	Label string `json:"label,omitempty"`
	// Description is optional helper text (e.g. availability counts or disk size).
	Description string `json:"description,omitempty"`
	// Default marks the preselected option (e.g. the default StorageClass); the
	// UI auto-selects it when the field has no value yet.
	Default bool `json:"default,omitempty"`
}

// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object

type OptionList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []Option `json:"items"`
}
