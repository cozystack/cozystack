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
	"testing"

	"github.com/cozystack/cozystack/internal/gpu"
)

// Setup may be called before any reconciler exists: it must succeed and
// touch nothing, so it accepts a nil manager.
func TestSetupWithoutReconcilersIsANoOp(t *testing.T) {
	if err := Setup(nil, gpu.Config{Enabled: true}); err != nil {
		t.Fatalf("Setup: %v", err)
	}
}
