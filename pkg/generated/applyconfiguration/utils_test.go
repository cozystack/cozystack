/*
Copyright 2025 The Cozystack Authors.

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

package applyconfiguration

import (
	"testing"

	v1alpha1 "github.com/cozystack/cozystack/pkg/apis/apps/v1alpha1"
	appsv1alpha1 "github.com/cozystack/cozystack/pkg/generated/applyconfiguration/apps/v1alpha1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

func TestForKind_Application(t *testing.T) {
	gvk := v1alpha1.SchemeGroupVersion.WithKind("Application")
	got := ForKind(gvk)
	if got == nil {
		t.Fatal("ForKind returned nil for Application GVK")
	}
	if _, ok := got.(*appsv1alpha1.ApplicationApplyConfiguration); !ok {
		t.Fatalf("ForKind returned %T, want *ApplicationApplyConfiguration", got)
	}
}

func TestForKind_ApplicationStatus(t *testing.T) {
	gvk := v1alpha1.SchemeGroupVersion.WithKind("ApplicationStatus")
	got := ForKind(gvk)
	if got == nil {
		t.Fatal("ForKind returned nil for ApplicationStatus GVK")
	}
	if _, ok := got.(*appsv1alpha1.ApplicationStatusApplyConfiguration); !ok {
		t.Fatalf("ForKind returned %T, want *ApplicationStatusApplyConfiguration", got)
	}
}

func TestForKind_Unknown(t *testing.T) {
	gvk := schema.GroupVersionKind{Group: "unknown.io", Version: "v1", Kind: "Unknown"}
	got := ForKind(gvk)
	if got != nil {
		t.Fatalf("ForKind returned %T for unknown GVK, want nil", got)
	}
}

func TestNewTypeConverter(t *testing.T) {
	scheme := runtime.NewScheme()
	tc := NewTypeConverter(scheme)
	if tc == nil {
		t.Fatal("NewTypeConverter returned nil")
	}
}
