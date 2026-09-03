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
)

// WrapPreservingStatus prefixes a backing API error with context while keeping
// its machine-readable Status intact. The endpoint handlers resolve the wire
// answer through ErrorToAPIStatus, which type-switches on the APIStatus
// interface and never unwraps, so a fmt.Errorf-wrapped backend rejection
// reaches the client as a generic 500 with an empty reason even though
// errors.Is/As still see the cause. Rebuilding the StatusError keeps the
// original code and reason (409 Conflict, 403 Forbidden, ...) on the wire.
// Non-status errors fall back to plain wrapping.
func WrapPreservingStatus(msg string, err error) error {
	var status apierrors.APIStatus
	if errors.As(err, &status) {
		st := status.Status()
		st.Message = fmt.Sprintf("%s: %s", msg, st.Message)
		return &apierrors.StatusError{ErrStatus: st}
	}
	return fmt.Errorf("%s: %w", msg, err)
}
