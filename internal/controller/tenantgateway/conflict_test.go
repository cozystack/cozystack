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

package tenantgateway

import (
	"errors"
	"fmt"
	"testing"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

// TestRetryableRouteWrite pins the classification the Reconcile wrapper
// requeues on: a conflict or a vanished route is a race the next pass
// outlives, anything else is a write the apiserver will refuse forever,
// and one fatal leaf in a joined batch must fail the whole batch rather
// than let a permanent refusal ride along on a transient one's retry.
func TestRetryableRouteWrite(t *testing.T) {
	conflict := apierrors.NewConflict(schema.GroupResource{Group: "gateway.networking.k8s.io", Resource: "httproutes"}, "stuck", errors.New("the object has been modified"))
	notFound := apierrors.NewNotFound(schema.GroupResource{Group: "gateway.networking.k8s.io", Resource: "httproutes"}, "gone")
	invalid := apierrors.NewInvalid(schema.GroupKind{Group: "gateway.networking.k8s.io", Kind: "HTTPRoute"}, "stuck", nil)

	for _, tc := range []struct {
		name string
		err  error
		want bool
	}{
		{"nil is nothing to retry", nil, false},
		{"conflict", conflict, true},
		{"not found", notFound, true},
		{"wrapped conflict", fmt.Errorf("update status of HTTPRoute tenant-foo/stuck: %w", conflict), true},
		{"join of two races", errors.Join(
			fmt.Errorf("update status of HTTPRoute tenant-foo/stuck: %w", conflict),
			fmt.Errorf("update status of HTTPRoute tenant-foo/gone: %w", notFound),
		), true},
		{"invalid", invalid, false},
		{"plain error", errors.New("connection refused"), false},
		{"join with one fatal leaf", errors.Join(
			fmt.Errorf("update status of HTTPRoute tenant-foo/stuck: %w", conflict),
			fmt.Errorf("update status of HTTPRoute tenant-foo/full: %w", invalid),
		), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := retryableRouteWrite(tc.err); got != tc.want {
				t.Errorf("retryableRouteWrite(%v) = %v, want %v", tc.err, got, tc.want)
			}
		})
	}
}
