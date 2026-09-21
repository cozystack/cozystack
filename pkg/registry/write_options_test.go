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

package registry

import (
	"reflect"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
)

func TestClientWriteOptionsPreserveAPISemantics(t *testing.T) {
	grace := int64(7)
	orphan := true
	propagation := metav1.DeletePropagationForeground
	uid := types.UID("expected-uid")
	rv := "42"

	create := &metav1.CreateOptions{
		TypeMeta:        metav1.TypeMeta{APIVersion: "meta.k8s.io/v1", Kind: "CreateOptions"},
		DryRun:          []string{metav1.DryRunAll},
		FieldManager:    "create-manager",
		FieldValidation: "Strict",
	}
	update := &metav1.UpdateOptions{
		TypeMeta:        metav1.TypeMeta{APIVersion: "meta.k8s.io/v1", Kind: "UpdateOptions"},
		DryRun:          []string{metav1.DryRunAll},
		FieldManager:    "update-manager",
		FieldValidation: "Warn",
	}
	deleteOptions := &metav1.DeleteOptions{
		TypeMeta:           metav1.TypeMeta{APIVersion: "meta.k8s.io/v1", Kind: "DeleteOptions"},
		GracePeriodSeconds: &grace,
		Preconditions:      &metav1.Preconditions{UID: &uid, ResourceVersion: &rv},
		PropagationPolicy:  &propagation,
		DryRun:             []string{metav1.DryRunAll},
	}
	createExpected := create.DeepCopy()
	updateExpected := update.DeepCopy()
	deleteExpected := deleteOptions.DeepCopy()

	tests := []struct {
		name string
		got  any
		want any
	}{
		{name: "create", got: ClientCreateOptions(create).AsCreateOptions(), want: createExpected},
		{name: "update", got: ClientUpdateOptions(update).AsUpdateOptions(), want: updateExpected},
		{name: "delete", got: ClientDeleteOptions(deleteOptions).AsDeleteOptions(), want: deleteExpected},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if !reflect.DeepEqual(tt.got, tt.want) {
				t.Fatalf("converted options differ:\n got: %#v\nwant: %#v", tt.got, tt.want)
			}
		})
	}
	if !reflect.DeepEqual(create, createExpected) || !reflect.DeepEqual(update, updateExpected) || !reflect.DeepEqual(deleteOptions, deleteExpected) {
		t.Fatalf("conversion mutated API options: create=%#v update=%#v delete=%#v", create, update, deleteOptions)
	}

	orphanDelete := &metav1.DeleteOptions{OrphanDependents: &orphan, DryRun: []string{metav1.DryRunAll}}
	orphanExpected := orphanDelete.DeepCopy()
	if got := ClientDeleteOptions(orphanDelete).AsDeleteOptions(); !reflect.DeepEqual(got, orphanExpected) {
		t.Fatalf("raw-only delete option lost: got %#v, want %#v", got, orphanExpected)
	}
	if !reflect.DeepEqual(orphanDelete, orphanExpected) {
		t.Fatalf("raw-only delete conversion mutated input: got %#v, want %#v", orphanDelete, orphanExpected)
	}
}

func TestClientWriteOptionConversionsPreserveSharedFields(t *testing.T) {
	update := &metav1.UpdateOptions{
		DryRun:          []string{metav1.DryRunAll},
		FieldManager:    "update-manager",
		FieldValidation: "Strict",
	}
	created := ClientCreateOptionsFromUpdate(update).AsCreateOptions()
	if !reflect.DeepEqual(created, &metav1.CreateOptions{
		DryRun:          update.DryRun,
		FieldManager:    update.FieldManager,
		FieldValidation: update.FieldValidation,
	}) {
		t.Fatalf("force-create conversion lost update options: %#v", created)
	}
	if got := CreateOptionsFromUpdate(nil); !reflect.DeepEqual(got, &metav1.CreateOptions{}) {
		t.Fatalf("nil force-create options: %#v", got)
	}

}

func TestClientWriteOptionsAcceptNil(t *testing.T) {
	if got := ClientCreateOptions(nil).AsCreateOptions(); !reflect.DeepEqual(got, &metav1.CreateOptions{}) {
		t.Fatalf("nil create options: %#v", got)
	}
	if got := ClientUpdateOptions(nil).AsUpdateOptions(); !reflect.DeepEqual(got, &metav1.UpdateOptions{}) {
		t.Fatalf("nil update options: %#v", got)
	}
	if got := ClientDeleteOptions(nil).AsDeleteOptions(); !reflect.DeepEqual(got, &metav1.DeleteOptions{}) {
		t.Fatalf("nil delete options: %#v", got)
	}
}
