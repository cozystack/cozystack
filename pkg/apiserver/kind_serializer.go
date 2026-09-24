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
	"io"

	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"

	appsv1alpha1 "github.com/cozystack/cozystack/pkg/apis/apps/v1alpha1"
)

// kindPreservingSerializer encodes apps objects under the kind they carry.
// Every apps kind is registered on the one Application Go type, and the
// stock versioning encoder converts through Scheme.ConvertToVersion, which
// picks the first kind registered for a type rather than the object's own.
// Only encoding goes through here. Two paths convert through the Scheme
// instead and still get the first registered kind: the result of a
// MutatingAdmissionPolicy, and the row objects of a Table requested with
// includeObject=Object. Admission webhooks and ValidatingAdmissionPolicy get
// the request kind set explicitly.
type kindPreservingSerializer struct {
	runtime.NegotiatedSerializer
}

func (s kindPreservingSerializer) EncoderForVersion(enc runtime.Encoder, gv runtime.GroupVersioner) runtime.Encoder {
	return kindPreservingEncoder{
		ns:      s.NegotiatedSerializer,
		enc:     enc,
		gv:      gv,
		Encoder: s.NegotiatedSerializer.EncoderForVersion(enc, gv),
	}
}

// kindPreservingEncoder must not implement runtime.EncoderWithAllocator: the
// watch handler prefers that method when present, and it would bypass Encode.
type kindPreservingEncoder struct {
	runtime.Encoder
	ns  runtime.NegotiatedSerializer
	enc runtime.Encoder
	gv  runtime.GroupVersioner
}

func (e kindPreservingEncoder) Encode(obj runtime.Object, w io.Writer) error {
	switch obj.(type) {
	case *appsv1alpha1.Application, *appsv1alpha1.ApplicationList:
		if gvk := obj.GetObjectKind().GroupVersionKind(); gvk.Kind != "" {
			return e.ns.EncoderForVersion(e.enc, exactKindVersioner{want: gvk, fallback: e.gv}).Encode(obj, w)
		}
	}
	return e.Encoder.Encode(obj, w)
}

// Identifier differs from the wrapped encoder's because the output does.
func (e kindPreservingEncoder) Identifier() runtime.Identifier {
	return "kindPreserving:" + e.Encoder.Identifier()
}

// exactKindVersioner picks want when the target group version would accept
// it, and otherwise defers to the versioner the request negotiated.
type exactKindVersioner struct {
	want     schema.GroupVersionKind
	fallback runtime.GroupVersioner
}

func (v exactKindVersioner) KindForGroupVersionKinds(kinds []schema.GroupVersionKind) (schema.GroupVersionKind, bool) {
	if target, ok := v.fallback.KindForGroupVersionKinds([]schema.GroupVersionKind{v.want}); ok && target == v.want {
		for _, k := range kinds {
			if k == v.want {
				return k, true
			}
		}
	}
	return v.fallback.KindForGroupVersionKinds(kinds)
}

func (v exactKindVersioner) Identifier() string {
	return "exactKind:" + v.want.String() + ":" + v.fallback.Identifier()
}
