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

package model

import (
	"reflect"
	"slices"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/utils/ptr"

	cozyv1alpha1 "github.com/cozystack/cozystack/api/v1alpha1"
)

const (
	h100Name = corev1.ResourceName("nvidia.com/GH100_H100")
	l40sName = corev1.ResourceName("nvidia.com/NVIDIA_L40S-24C")
)

func TestEffectiveMaxCount(t *testing.T) {
	tests := []struct {
		name      string
		spec      cozyv1alpha1.GPUClassSpec
		want      int32
		wantLimit bool
	}{
		{"vgpu unset defaults to 1", cozyv1alpha1.GPUClassSpec{Mode: cozyv1alpha1.GPUModeVGPU}, 1, true},
		{"vgpu set", cozyv1alpha1.GPUClassSpec{Mode: cozyv1alpha1.GPUModeVGPU, MaxCount: ptr.To[int32](2)}, 2, true},
		{"passthrough unset is no limit", cozyv1alpha1.GPUClassSpec{Mode: cozyv1alpha1.GPUModePassthrough}, 0, false},
		{"passthrough set", cozyv1alpha1.GPUClassSpec{Mode: cozyv1alpha1.GPUModePassthrough, MaxCount: ptr.To[int32](4)}, 4, true},
	}
	for _, tc := range tests {
		got, limited := EffectiveMaxCount(&tc.spec)
		if got != tc.want || limited != tc.wantLimit {
			t.Errorf("%s: EffectiveMaxCount = (%d, %v), want (%d, %v)", tc.name, got, limited, tc.want, tc.wantLimit)
		}
	}
}

func TestClassFrom(t *testing.T) {
	seconds := int64(300)
	class := &cozyv1alpha1.GPUClass{
		ObjectMeta: metav1.ObjectMeta{Name: "h100", UID: "h100-uid", CreationTimestamp: metav1.Unix(1000, 0)},
		Status:     cozyv1alpha1.GPUClassStatus{OwnedResourceName: string(h100Name)},
		Spec: cozyv1alpha1.GPUClassSpec{
			Mode: cozyv1alpha1.GPUModePassthrough,
			Tolerations: []cozyv1alpha1.GPUClassToleration{
				{Key: dedicatedTaint, Operator: corev1.TolerationOpExists, Effect: corev1.TaintEffectNoSchedule},
				{Key: "example.com/maintenance", Operator: corev1.TolerationOpEqual, Value: "planned", Effect: corev1.TaintEffectNoExecute, TolerationSeconds: &seconds},
			},
			Source: cozyv1alpha1.GPUClassSource{DevicePlugin: &cozyv1alpha1.DevicePluginSource{ResourceName: string(h100Name)}},
		},
	}
	want := Class{Name: "h100", UID: "h100-uid", Created: metav1.Unix(1000, 0), Mode: cozyv1alpha1.GPUModePassthrough, ResourceName: h100Name, OwnedResourceName: h100Name, Tolerations: append(slices.Clone(tolerateDedicated),
		corev1.Toleration{Key: "example.com/maintenance", Operator: corev1.TolerationOpEqual, Value: "planned", Effect: corev1.TaintEffectNoExecute, TolerationSeconds: &seconds})}
	if got := ClassFrom(class); !reflect.DeepEqual(got, want) {
		t.Errorf("ClassFrom = %+v, want %+v", got, want)
	}
}

func TestConflicts(t *testing.T) {
	older, newer := metav1.Unix(1000, 0), metav1.Unix(2000, 0)
	tests := []struct {
		name    string
		classes []Class
		want    []string
	}{
		{name: "distinct names", classes: []Class{{Name: "h100", ResourceName: h100Name}, {Name: "l40s-24c", ResourceName: l40sName}}},
		{
			name:    "the newer class conflicts",
			classes: []Class{{Name: "h100", Created: older, ResourceName: h100Name}, {Name: "h100-alt", Created: newer, ResourceName: h100Name}, {Name: "l40s-24c", Created: newer, ResourceName: l40sName}},
			want:    []string{"h100-alt"},
		},
		{
			name:    "the newer class conflicts whatever the list order",
			classes: []Class{{Name: "h100-alt", Created: newer, ResourceName: h100Name}, {Name: "h100", Created: older, ResourceName: h100Name}},
			want:    []string{"h100-alt"},
		},
		{
			name:    "creation time decides before the name",
			classes: []Class{{Name: "a", Created: newer, ResourceName: h100Name}, {Name: "z", Created: older, ResourceName: h100Name}},
			want:    []string{"a"},
		},
		{
			name:    "the name breaks a tie in creation time",
			classes: []Class{{Name: "b", Created: older, ResourceName: h100Name}, {Name: "a", Created: older, ResourceName: h100Name}},
			want:    []string{"b"},
		},
		{
			name:    "three classes on one name",
			classes: []Class{{Name: "c", Created: newer, ResourceName: h100Name}, {Name: "a", Created: older, ResourceName: h100Name}, {Name: "b", Created: newer, ResourceName: h100Name}},
			want:    []string{"b", "c"},
		},
		{name: "classes without a resource name do not conflict", classes: []Class{{Name: "a"}, {Name: "b"}}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			for i := range tc.classes {
				tc.classes[i].UID = types.UID(tc.classes[i].Name)
			}
			if got := Conflicts(tc.classes); !got.Equal(sets.New(tc.want...)) {
				t.Errorf("Conflicts = %v, want %v", sets.List(got), tc.want)
			}
		})
	}
}

func nameClass(name string, created int64, requested, owned corev1.ResourceName) Class {
	return Class{Name: name, UID: types.UID(name + "-uid"), Created: metav1.Unix(created, 0), ResourceName: requested, OwnedResourceName: owned}
}

func TestResourceNameIncumbent(t *testing.T) {
	owner := nameClass("owner", 2000, h100Name, h100Name)
	contender := nameClass("older-edited", 1000, h100Name, l40sName)
	for _, classes := range [][]Class{{owner, contender}, {contender, owner}} {
		if got := Conflicts(classes); !got.Equal(sets.New(contender.Name)) {
			t.Errorf("Conflicts = %v, want only %s", sets.List(got), contender.Name)
		}
	}
}

func TestResourceNameReservationSurvivesUnrelatedStatus(t *testing.T) {
	owner := &cozyv1alpha1.GPUClass{
		ObjectMeta: metav1.ObjectMeta{Name: "owner", UID: "owner-uid", CreationTimestamp: metav1.Unix(2000, 0), Generation: 1},
		Spec: cozyv1alpha1.GPUClassSpec{
			Mode:   cozyv1alpha1.GPUModeVGPU,
			Source: cozyv1alpha1.GPUClassSource{DevicePlugin: &cozyv1alpha1.DevicePluginSource{ResourceName: string(h100Name)}},
		},
		Status: cozyv1alpha1.GPUClassStatus{OwnedResourceName: string(h100Name)},
	}
	contender := nameClass("older-edited", 1000, h100Name, l40sName)
	for _, ready := range []metav1.ConditionStatus{metav1.ConditionFalse, metav1.ConditionUnknown, metav1.ConditionTrue} {
		updated := owner.DeepCopy()
		updated.Generation++
		updated.Spec.License = &cozyv1alpha1.GPUClassLicense{SecretName: "updated-license"}
		updated.Status.Conditions = []metav1.Condition{
			{Type: "Ready", Status: ready, ObservedGeneration: owner.Generation},
			{Type: cozyv1alpha1.GPUClassResourceNameConflict, Status: metav1.ConditionTrue, ObservedGeneration: owner.Generation},
		}
		classes := []Class{contender, ClassFrom(updated)}
		if got := Conflicts(classes); !got.Equal(sets.New(contender.Name)) {
			t.Errorf("Ready=%s: conflicts = %v", ready, sets.List(got))
		}
		if got := ConfirmedResourceNames(classes); got[updated.UID] != h100Name || len(got) != 1 {
			t.Errorf("Ready=%s: confirmed = %v", ready, got)
		}
	}
}

func TestResourceNameAcknowledgement(t *testing.T) {
	a := nameClass("a", 1000, h100Name, "")
	b := nameClass("b", 2000, h100Name, "")
	for _, classes := range [][]Class{{a, b}, {b, a}} {
		if got := ResourceNameCandidates(classes); got[h100Name] != a.UID {
			t.Errorf("candidates = %v, want %s", got, a.UID)
		}
		if got := ConfirmedResourceNames(classes); len(got) != 0 {
			t.Errorf("unacknowledged classes offered: %v", got)
		}
	}
	a.OwnedResourceName = h100Name
	if got := ConfirmedResourceNames([]Class{b, a}); got[a.UID] != h100Name || len(got) != 1 {
		t.Errorf("confirmed = %v, want acknowledged owner", got)
	}
}

func TestResourceNameTransition(t *testing.T) {
	a := nameClass("a", 1000, l40sName, h100Name)
	b := nameClass("b", 2000, h100Name, "")
	classes := []Class{a, b}
	if got := ResourceNameCandidates(classes); got[h100Name] != a.UID || got[l40sName] != a.UID {
		t.Fatalf("pending rename candidates = %v", got)
	}
	if got := ConfirmedResourceNames(classes); len(got) != 0 {
		t.Errorf("old reservation offers a new name: %v", got)
	}
	a.ResourceName = h100Name
	if got := ConfirmedResourceNames([]Class{b, a}); got[a.UID] != h100Name {
		t.Errorf("unacknowledged ABA lost the retained name: %v", got)
	}
	a.ResourceName, a.OwnedResourceName = l40sName, l40sName
	b.OwnedResourceName = h100Name
	a.ResourceName = h100Name
	classes = []Class{a, b}
	if got := Conflicts(classes); !got.Equal(sets.New(a.Name)) {
		t.Errorf("acknowledged ABA conflicts = %v, want returning class", got)
	}
	if got := ConfirmedResourceNames(classes); got[b.UID] != h100Name || len(got) != 1 {
		t.Errorf("acknowledged ABA confirmed = %v", got)
	}
}

func TestResourceNameDuplicateReservations(t *testing.T) {
	a := nameClass("a", 1000, h100Name, h100Name)
	b := nameClass("b", 2000, l40sName, h100Name)
	for _, classes := range [][]Class{{a, b}, {b, a}} {
		if got := ResourceNameCandidates(classes); got[h100Name] != "" {
			t.Errorf("ambiguous name acquired a candidate: %v", got)
		}
		if got := ConfirmedResourceNames(classes); len(got) != 0 {
			t.Errorf("ambiguous reservation offered: %v", got)
		}
		if got := Conflicts(classes); !got.Has(a.Name) {
			t.Errorf("duplicate old-name reservation did not block its requester: %v", got)
		}
	}
}

func TestResourceNameAmbiguitySurvivesConditionUpdates(t *testing.T) {
	var objects []*cozyv1alpha1.GPUClass
	for i, name := range []string{"older", "newer"} {
		objects = append(objects, &cozyv1alpha1.GPUClass{
			ObjectMeta: metav1.ObjectMeta{Name: name, UID: types.UID(name), CreationTimestamp: metav1.Unix(int64(1000+i), 0)},
			Spec: cozyv1alpha1.GPUClassSpec{Source: cozyv1alpha1.GPUClassSource{
				DevicePlugin: &cozyv1alpha1.DevicePluginSource{ResourceName: string(h100Name)},
			}},
			Status: cozyv1alpha1.GPUClassStatus{OwnedResourceName: string(h100Name)},
		})
	}
	for pass := range 3 {
		classes := []Class{ClassFrom(objects[pass%2]), ClassFrom(objects[(pass+1)%2])}
		if got := Conflicts(classes); !got.Equal(sets.New("older", "newer")) {
			t.Errorf("pass %d: conflicts = %v", pass, got)
		}
		if got := ConfirmedResourceNames(classes); len(got) != 0 {
			t.Errorf("pass %d: ambiguous name offered: %v", pass, got)
		}
		for _, obj := range objects {
			obj.Status.Conditions = []metav1.Condition{{Type: cozyv1alpha1.GPUClassResourceNameConflict, Status: metav1.ConditionTrue, ObservedGeneration: obj.Generation}}
		}
	}
}

func TestResourceNameIdentity(t *testing.T) {
	old := nameClass("a", 1000, h100Name, h100Name)
	recreated := nameClass("a", 3000, h100Name, "")
	recreated.UID = "new-uid"
	b := nameClass("b", 2000, h100Name, h100Name)
	got := ConfirmedResourceNames([]Class{recreated, b})
	if got[old.UID] != "" || got[recreated.UID] != "" || got[b.UID] != h100Name {
		t.Errorf("recreated identity inherited ownership: %v", got)
	}
	missingUID := nameClass("invalid", 1000, h100Name, h100Name)
	missingUID.UID = ""
	if got := ConfirmedResourceNames([]Class{missingUID}); len(got) != 0 {
		t.Errorf("missing identity confirmed: %v", got)
	}
}
