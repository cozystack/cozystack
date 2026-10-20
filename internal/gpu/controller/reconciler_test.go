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

package controller

import (
	"errors"
	"testing"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

func TestAConflictJoinedWithAnotherErrorIsNotAConflict(t *testing.T) {
	conflict := apierrors.NewConflict(schema.GroupResource{Resource: "gpuclaims"}, "a", nil)
	if !isConflict([]error{conflict, errors.Join(conflict, nil)}) {
		t.Fatal("conflicts alone must retry quickly")
	}
	if isConflict([]error{errors.Join(conflict, errors.New("forbidden"))}) {
		t.Fatal("a real error next to a conflict was taken for a conflict")
	}
}
