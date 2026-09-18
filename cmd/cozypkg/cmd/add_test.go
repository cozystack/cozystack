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

package cmd

import (
	"testing"

	cozyv1alpha1 "github.com/cozystack/cozystack/api/v1alpha1"
	"github.com/cozystack/cozystack/internal/marketplace/collision"
	"github.com/cozystack/cozystack/internal/marketplace/tapconst"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestPrivilegedComponents(t *testing.T) {
	ps := &cozyv1alpha1.PackageSource{
		Spec: cozyv1alpha1.PackageSourceSpec{
			Variants: []cozyv1alpha1.Variant{
				{
					Name: "default",
					Components: []cozyv1alpha1.Component{
						{Name: "app", Path: "apps/app"},
						{Name: "risky", Path: "apps/risky", Install: &cozyv1alpha1.ComponentInstall{Privileged: true}},
					},
				},
				{
					Name:       "safe",
					Components: []cozyv1alpha1.Component{{Name: "app", Path: "apps/app"}},
				},
			},
		},
	}

	if got := collision.PrivilegedInstallComponents(ps, "default"); len(got) != 1 || got[0] != "risky" {
		t.Errorf("default variant privileged = %v, want [risky]", got)
	}
	if got := collision.PrivilegedInstallComponents(ps, "safe"); len(got) != 0 {
		t.Errorf("safe variant should have no privileged components, got %v", got)
	}
	if got := collision.PrivilegedInstallComponents(ps, "nonexistent"); len(got) != 0 {
		t.Errorf("unknown variant should return none, got %v", got)
	}
}

func TestIsTapAutoRegistration(t *testing.T) {
	autoReg := &cozyv1alpha1.Package{ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{tapconst.Label: "true"}}}
	if !isTapAutoRegistration(autoReg) {
		t.Error("a tap-labelled empty-variant Package is an auto-registration")
	}
	userDefault := &cozyv1alpha1.Package{Spec: cozyv1alpha1.PackageSpec{Variant: "default"}}
	if isTapAutoRegistration(userDefault) {
		t.Error("a user's Package (no tap label) is not an auto-registration")
	}
	pinned := &cozyv1alpha1.Package{
		ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{tapconst.Label: "true"}},
		Spec:       cozyv1alpha1.PackageSpec{Variant: "full"},
	}
	if isTapAutoRegistration(pinned) {
		t.Error("a tap Package already pinned to a variant is not re-openable")
	}
}
