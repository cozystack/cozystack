/*
Copyright 2024 The Cozystack Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package v1alpha1

import (
	"testing"

	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
)

func TestApplicationSchedulingClass(t *testing.T) {
	cases := []struct {
		name string
		spec *apiextensionsv1.JSON
		want string
	}{
		{
			name: "nil spec",
			spec: nil,
			want: "",
		},
		{
			name: "empty raw",
			spec: &apiextensionsv1.JSON{Raw: nil},
			want: "",
		},
		{
			name: "spec without schedulingClass",
			spec: &apiextensionsv1.JSON{Raw: []byte(`{"replicas": 3}`)},
			want: "",
		},
		{
			name: "spec with empty schedulingClass",
			spec: &apiextensionsv1.JSON{Raw: []byte(`{"schedulingClass": ""}`)},
			want: "",
		},
		{
			name: "spec with schedulingClass",
			spec: &apiextensionsv1.JSON{Raw: []byte(`{"schedulingClass": "co-region-prod"}`)},
			want: "co-region-prod",
		},
		{
			name: "spec with schedulingClass and other fields",
			spec: &apiextensionsv1.JSON{Raw: []byte(`{"replicas": 2, "schedulingClass": "gpu-nodes", "size": "10Gi"}`)},
			want: "gpu-nodes",
		},
		{
			name: "invalid JSON returns empty",
			spec: &apiextensionsv1.JSON{Raw: []byte(`{not valid json`)},
			want: "",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			app := Application{Spec: tc.spec}
			got := app.SchedulingClass()
			if got != tc.want {
				t.Errorf("SchedulingClass() = %q, want %q", got, tc.want)
			}
		})
	}
}
