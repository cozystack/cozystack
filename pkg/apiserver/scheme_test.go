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

package apiserver

import (
	"testing"

	appsfuzzer "github.com/cozystack/cozystack/pkg/apis/apps/fuzzer"
	corefuzzer "github.com/cozystack/cozystack/pkg/apis/core/fuzzer"
	sdnfuzzer "github.com/cozystack/cozystack/pkg/apis/sdn/fuzzer"
	sdnv1alpha1 "github.com/cozystack/cozystack/pkg/apis/sdn/v1alpha1"
	"k8s.io/apimachinery/pkg/api/apitesting/roundtrip"
)

func TestRoundTripTypes(t *testing.T) {
	roundtrip.RoundTripTestForScheme(t, Scheme, appsfuzzer.Funcs)
	roundtrip.RoundTripTestForScheme(t, Scheme, corefuzzer.Funcs)
	roundtrip.RoundTripTestForScheme(t, Scheme, sdnfuzzer.Funcs)
}

// TestSchemeRecognizesSDNTypes guards against the SecurityGroup roundtrip
// silently going vacuous: the kinds must be registered through the AddToScheme
// path that the apiserver Scheme and the roundtrip helper use, not only at
// server start. If this fails, the roundtrip above is exercising nothing.
func TestSchemeRecognizesSDNTypes(t *testing.T) {
	for _, kind := range []string{"SecurityGroup", "SecurityGroupList"} {
		gvk := sdnv1alpha1.SchemeGroupVersion.WithKind(kind)
		if !Scheme.Recognizes(gvk) {
			t.Errorf("Scheme does not recognize %s — SecurityGroup serialization is untested", gvk)
		}
	}
}
