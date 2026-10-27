/*
Copyright 2025.

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

// Package v1alpha1 contains API Schema definitions for the  v1alpha1 API group.
//
// Fields that embed a large upstream struct, a PodTemplateSpec above all, are
// marked Schemaless with PreserveUnknownFields. backupstrategy-controller
// renders these CRDs from templates, so Helm stores each one twice in every
// release revision, and a revision is one Secret capped at 1 MiB; with the
// upstream schemas inlined the revision that renders the default strategies
// no longer fits and that upgrade fails. The Go types stay typed, and the
// apiserver validates the content when a driver creates the workload from it.
// hack/strategy-crd-schemaless.bats fails on a strategy CRD that inlines a
// pod template schema.
//
// +kubebuilder:object:generate=true
// +groupName=strategy.backups.cozystack.io
package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

var (
	GroupVersion  = schema.GroupVersion{Group: "strategy.backups.cozystack.io", Version: "v1alpha1"}
	SchemeBuilder = runtime.NewSchemeBuilder(addGroupVersion)
	AddToScheme   = SchemeBuilder.AddToScheme
)

func addGroupVersion(scheme *runtime.Scheme) error {
	metav1.AddToGroupVersion(scheme, GroupVersion)
	return nil
}
