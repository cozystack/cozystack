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
	"errors"
	"fmt"
	"reflect"
	"testing"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/validation/field"
	"k8s.io/apiserver/pkg/endpoints/handlers/responsewriters"
)

func TestWrapPreservingStatusKeepsBackendErrorUnchanged(t *testing.T) {
	invalid := apierrors.NewInvalid(schema.GroupKind{Group: "helm.toolkit.fluxcd.io", Kind: "HelmRelease"}, "postgresql-example", field.ErrorList{
		field.Required(field.NewPath("spec", "chartRef"), "chart reference is required"),
	})
	for _, backend := range []*apierrors.StatusError{invalid, apierrors.NewTooManyRequests("try again later", 5), apierrors.NewBadRequest("invalid request")} {
		t.Run(string(backend.ErrStatus.Reason), func(t *testing.T) {
			before := backend.ErrStatus.DeepCopy()
			resource := schema.GroupResource{Group: "apps.cozystack.io", Resource: "postgresqls"}
			err := WrapPreservingStatus("failed to update HelmRelease", fmt.Errorf("backend: %w", backend), resource, "example")
			status := responsewriters.ErrorToAPIStatus(err)
			if status.Code != before.Code || status.Reason != before.Reason || status.Status != before.Status {
				t.Fatalf("wire status = %#v, lost backend status %#v", status, before)
			}
			if status.Details == nil || status.Details.Group != resource.Group || status.Details.Kind != resource.Resource || status.Details.Name != "example" {
				t.Fatalf("wire details = %#v, want the requested Application", status.Details)
			}
			if before.Details != nil && (!reflect.DeepEqual(status.Details.Causes, before.Details.Causes) || status.Details.RetryAfterSeconds != before.Details.RetryAfterSeconds) {
				t.Fatalf("wire details = %#v, lost backend causes or retry delay %#v", status.Details, before.Details)
			}
			if !reflect.DeepEqual(&backend.ErrStatus, before) {
				t.Fatalf("backend error was mutated: got %#v, want %#v", backend.ErrStatus, *before)
			}
		})
	}
}

func TestWrapPreservingStatusKeepsNonStatusCause(t *testing.T) {
	backend := errors.New("connection lost")
	err := WrapPreservingStatus("failed to update HelmRelease", backend, schema.GroupResource{Group: "apps.cozystack.io", Resource: "postgresqls"}, "example")
	if !errors.Is(err, backend) {
		t.Fatalf("wrapped error %v lost its cause", err)
	}
}
