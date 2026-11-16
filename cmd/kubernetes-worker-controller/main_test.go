package main

import (
	"testing"

	"k8s.io/apimachinery/pkg/runtime/schema"
)

func TestWorkerReadKindsRegistered(t *testing.T) {
	scheme, err := workerScheme()
	if err != nil {
		t.Fatal(err)
	}
	for _, gvk := range []schema.GroupVersionKind{
		{Version: "v1", Kind: "Secret"},
		{Version: "v1", Kind: "ConfigMap"},
		{Group: "cozystack.io", Version: "v1alpha1", Kind: "Package"},
		{Group: "cozystack.io", Version: "v1alpha1", Kind: "PackageSource"},
		{Group: "source.toolkit.fluxcd.io", Version: "v1", Kind: "ExternalArtifact"},
		{Group: "source.extensions.fluxcd.io", Version: "v1beta1", Kind: "ArtifactGenerator"},
	} {
		if _, err := scheme.New(gvk); err != nil {
			t.Error(err)
		}
	}
}

func TestOptions(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(*options)
		want   bool
	}{
		{"defaults", func(*options) {}, true},
		{"empty default", func(o *options) { o.defaultClass = "" }, false},
		{"invalid default", func(o *options) { o.defaultClass = "Bad/Class" }, false},
		{"empty TLS directory", func(o *options) { o.certDir = "" }, false},
		{"invalid port", func(o *options) { o.port = 0 }, false},
		{"port above the range", func(o *options) { o.port = 65536 }, false},
		{"highest port", func(o *options) { o.port = 65535 }, true},
		{"both cleanup names", func(o *options) { o.removeMutating, o.removeValidating = "m", "v" }, true},
		{"only the mutating cleanup name", func(o *options) { o.removeMutating = "m" }, false},
		{"only the validating cleanup name", func(o *options) { o.removeValidating = "v" }, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			o := defaultOptions()
			tc.mutate(&o)
			if err := o.validate(); (err == nil) != tc.want {
				t.Fatalf("validation=%v want valid=%v", err, tc.want)
			}
		})
	}
}
