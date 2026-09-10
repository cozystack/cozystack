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

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

// WrapPreservingStatus maps a backing API error to the requested resource.
// ErrorToAPIStatus does not unwrap errors, so the result must implement
// APIStatus directly to retain the backend's HTTP code and reason.
func WrapPreservingStatus(msg string, err error, resource schema.GroupResource, name string) error {
	msg = fmt.Sprintf("%s %q: %s", resource.String(), name, msg)
	var status apierrors.APIStatus
	if errors.As(err, &status) {
		backendStatus := status.Status()
		st := backendStatus.DeepCopy()
		if st.Details == nil {
			st.Details = &metav1.StatusDetails{}
		}
		st.Details.Group = resource.Group
		st.Details.Kind = resource.Resource
		st.Details.Name = name
		st.Message = fmt.Sprintf("%s: %s", msg, st.Message)
		return &apierrors.StatusError{ErrStatus: *st}
	}
	return fmt.Errorf("%s: %w", msg, err)
}
