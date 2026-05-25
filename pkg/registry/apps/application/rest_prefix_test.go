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

package application

import (
	"testing"

	helmv2 "github.com/fluxcd/helm-controller/api/v2"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/cozystack/cozystack/pkg/config"
)

func TestAppNameFromHelmRelease(t *testing.T) {
	tests := []struct {
		name       string
		prefix     string
		hrName     string
		hrLabels   map[string]string
		wantName   string
	}{
		{
			name:     "label takes precedence over prefix-strip",
			prefix:   "foo-bar-",
			hrName:   "foo-myapp",
			hrLabels: map[string]string{ApplicationNameLabel: "myapp"},
			wantName: "myapp",
		},
		{
			name:     "falls back to prefix-strip when label absent",
			prefix:   "foo-",
			hrName:   "foo-myapp",
			hrLabels: map[string]string{},
			wantName: "myapp",
		},
		{
			name:     "falls back to prefix-strip when labels nil",
			prefix:   "foo-",
			hrName:   "foo-myapp",
			hrLabels: nil,
			wantName: "myapp",
		},
		{
			name:     "empty label value falls back to prefix-strip",
			prefix:   "foo-",
			hrName:   "foo-myapp",
			hrLabels: map[string]string{ApplicationNameLabel: ""},
			wantName: "myapp",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := &REST{
				releaseConfig: config.ReleaseConfig{Prefix: tt.prefix},
			}
			hr := &helmv2.HelmRelease{
				ObjectMeta: metav1.ObjectMeta{
					Name:   tt.hrName,
					Labels: tt.hrLabels,
				},
			}
			got := r.appNameFromHelmRelease(hr)
			if got != tt.wantName {
				t.Errorf("appNameFromHelmRelease() = %q, want %q", got, tt.wantName)
			}
		})
	}
}
