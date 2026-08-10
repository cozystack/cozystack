/*
Copyright 2026 The Cozystack Authors.

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

// Package v1alpha1 contains API Schema definitions for the
// gateway.cozystack.io v1alpha1 API group.
// +kubebuilder:object:generate=true
// +groupName=gateway.cozystack.io
package v1alpha1

// A comment block separated from its declaration by a blank line is
// detached on purpose. controller-gen turns an attached comment into
// the CRD description, which kubectl explain prints to whoever is
// filling the spec in, so rationale meant for whoever next edits the
// validation markers goes in a detached block: it stays beside the
// code without shipping to every cluster.
//
// This note is itself detached, for the same reason: it is a
// convention for editors of this package, not documentation of the
// API it declares.

import (
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/scheme"
)

var (
	// GroupVersion is group version used to register these objects.
	GroupVersion = schema.GroupVersion{Group: "gateway.cozystack.io", Version: "v1alpha1"}

	// SchemeBuilder is used to add go types to the GroupVersionKind scheme.
	SchemeBuilder = &scheme.Builder{GroupVersion: GroupVersion}

	// AddToScheme adds the types in this group-version to the given scheme.
	AddToScheme = SchemeBuilder.AddToScheme
)
