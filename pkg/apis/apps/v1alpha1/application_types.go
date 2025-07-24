/*
Copyright 2024 The Cozystack Authors.

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

package v1alpha1

import (
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object

// ApplicationList is a list of Application objects.
type ApplicationList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty" protobuf:"bytes,1,opt,name=metadata"`

	Items []Application `json:"items" protobuf:"bytes,2,rep,name=items"`
}

// ApplicationStatus is the status of a Application.
type ApplicationStatus struct {
	// Conditions holds the conditions for the Application.
	// +optional
	Version    string             `json:"version,omitempty"`
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// GetConditions returns the status conditions of the object.
func (in Application) GetConditions() []metav1.Condition {
	return in.Status.Conditions
}

// SetConditions sets the status conditions on the object.
func (in *Application) SetConditions(conditions []metav1.Condition) {
	in.Status.Conditions = conditions
}

// +genclient
// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object

// Application is an example type with a spec and a status.
type Application struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty" protobuf:"bytes,1,opt,name=metadata"`

	AppVersion string `json:"appVersion,omitempty" protobuf:"bytes,1,opt,name=version"`
	// +optional
	Spec   *apiextensionsv1.JSON `json:"spec,omitempty" protobuf:"bytes,2,opt,name=spec"`
	Status ApplicationStatus     `json:"status,omitempty" protobuf:"bytes,3,opt,name=status"`
}
