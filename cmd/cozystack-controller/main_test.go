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

package main

import (
	"testing"

	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/apiutil"

	internalv1alpha1 "github.com/cozystack/cozystack/api/internalapi/v1alpha1"
)

// TestSchemeRegistersReconciledKinds pins that the manager scheme init() builds
// resolves the kinds the CA-extraction controller reconciles For(). A kind
// missing here is not a build error — it fails at runtime: SetupWithManager
// cannot resolve the GVK for its field index or its For() source, returns an
// error, and main() calls os.Exit(1), taking the whole binary (and every other
// controller) down with it. The scheme is the package-level var init() populates.
func TestSchemeRegistersReconciledKinds(t *testing.T) {
	for name, obj := range map[string]runtime.Object{
		"TenantProjection":     &internalv1alpha1.TenantProjection{},
		"TenantProjectionList": &internalv1alpha1.TenantProjectionList{},
	} {
		if _, err := apiutil.GVKForObject(obj, scheme); err != nil {
			t.Errorf("manager scheme does not register %s, so the CA-extraction controller cannot start: %v", name, err)
		}
	}
}
