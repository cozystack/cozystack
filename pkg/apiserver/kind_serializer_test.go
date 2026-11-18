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
	"bytes"
	"encoding/json"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/runtime/serializer"

	appsv1alpha1 "github.com/cozystack/cozystack/pkg/apis/apps/v1alpha1"
	"github.com/cozystack/cozystack/pkg/config"
)

// newTwoKindCodecs registers Postgres before Redis on a fresh scheme, so
// Postgres is the kind a stock conversion falls back to.
func newTwoKindCodecs(t *testing.T) serializer.CodecFactory {
	t.Helper()
	s := runtime.NewScheme()
	metav1.AddToGroupVersion(s, appsv1alpha1.SchemeGroupVersion)
	if err := appsv1alpha1.RegisterDynamicTypes(s, &config.ResourceConfig{Resources: []config.Resource{
		{Application: config.ApplicationConfig{Kind: "Postgres"}},
		{Application: config.ApplicationConfig{Kind: "Redis"}},
	}}); err != nil {
		t.Fatal(err)
	}
	return serializer.NewCodecFactory(s)
}

func encodedKind(t *testing.T, enc runtime.Encoder, obj runtime.Object) string {
	t.Helper()
	var b bytes.Buffer
	if err := enc.Encode(obj, &b); err != nil {
		t.Fatal(err)
	}
	var m struct {
		Kind string `json:"kind"`
	}
	if err := json.Unmarshal(b.Bytes(), &m); err != nil {
		t.Fatal(err)
	}
	return m.Kind
}

func TestKindPreservingSerializer_Encode(t *testing.T) {
	codecs := newTwoKindCodecs(t)
	info, ok := runtime.SerializerInfoForMediaType(codecs.SupportedMediaTypes(), runtime.ContentTypeJSON)
	if !ok {
		t.Fatal("no JSON serializer")
	}
	enc := kindPreservingSerializer{codecs}.EncoderForVersion(info.Serializer, appsv1alpha1.SchemeGroupVersion)
	gv := appsv1alpha1.SchemeGroupVersion.String()

	for _, tc := range []struct {
		name string
		obj  runtime.Object
		want string
	}{
		{"object keeps its kind", &appsv1alpha1.Application{TypeMeta: metav1.TypeMeta{APIVersion: gv, Kind: "Redis"}}, "Redis"},
		{"list keeps its kind", &appsv1alpha1.ApplicationList{TypeMeta: metav1.TypeMeta{APIVersion: gv, Kind: "RedisList"}}, "RedisList"},
		// Without a kind on the object there is nothing to keep, so the
		// stock choice stands; the storages always stamp one.
		{"object without a kind falls back to the stock choice", &appsv1alpha1.Application{}, "Postgres"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := encodedKind(t, enc, tc.obj); got != tc.want {
				t.Fatalf("kind = %q, want %q", got, tc.want)
			}
		})
	}
}

// The output differs from the wrapped encoder's, so a serialization cache keyed
// by encoder identifier must not share entries between them.
func TestKindPreservingSerializer_IdentifierDiffersFromWrapped(t *testing.T) {
	codecs := newTwoKindCodecs(t)
	info, _ := runtime.SerializerInfoForMediaType(codecs.SupportedMediaTypes(), runtime.ContentTypeJSON)
	gv := appsv1alpha1.SchemeGroupVersion
	wrapped := codecs.EncoderForVersion(info.Serializer, gv).Identifier()
	if got := (kindPreservingSerializer{codecs}).EncoderForVersion(info.Serializer, gv).Identifier(); got == wrapped {
		t.Fatalf("identifier %q equals the wrapped encoder's", got)
	}
}

func TestExactKindVersioner(t *testing.T) {
	apps := appsv1alpha1.SchemeGroupVersion
	postgres, redis := apps.WithKind("Postgres"), apps.WithKind("Redis")
	kinds := []schema.GroupVersionKind{postgres, redis}
	other := schema.GroupVersion{Group: "example.com", Version: "v1"}

	for _, tc := range []struct {
		name     string
		want     schema.GroupVersionKind
		fallback runtime.GroupVersioner
		wantGVK  schema.GroupVersionKind
		wantOK   bool
	}{
		{"picks the object's kind", redis, apps, redis, true},
		{"defers when the target is another group version", redis, other, schema.GroupVersionKind{}, false},
		{"defers when the kind is not registered for the type", apps.WithKind("Kafka"), apps, postgres, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := exactKindVersioner{want: tc.want, fallback: tc.fallback}.KindForGroupVersionKinds(kinds)
			if got != tc.wantGVK || ok != tc.wantOK {
				t.Fatalf("got %v, %v; want %v, %v", got, ok, tc.wantGVK, tc.wantOK)
			}
		})
	}
}
