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
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"

	gpuv1alpha1 "github.com/cozystack/cozystack/api/gpu/v1alpha1"
)

func TestSchemeRegistersWrittenKinds(t *testing.T) {
	for _, obj := range []runtime.Object{&gpuv1alpha1.GPUNode{}, &corev1.Node{}, &corev1.Pod{}} {
		if _, _, err := scheme.ObjectKinds(obj); err != nil {
			t.Errorf("%T: %v", obj, err)
		}
	}
}

func TestParseFlags(t *testing.T) {
	tests := []struct {
		name    string
		args    []string
		env     string
		wantErr bool
		want    options
	}{
		{
			name: "node name from the downward API",
			env:  "gpu-node-1",
			want: options{nodeName: "gpu-node-1", sysfs: "/sys", socket: "/var/lib/kubelet/pod-resources/kubelet.sock", interval: 30 * time.Second, probeAddr: ":8081"},
		},
		{
			name: "flags override",
			args: []string{"--node-name=n2", "--sysfs=/host/sys", "--interval=1m"},
			want: options{nodeName: "n2", sysfs: "/host/sys", socket: "/var/lib/kubelet/pod-resources/kubelet.sock", interval: time.Minute, probeAddr: ":8081"},
		},
		{name: "no node name", wantErr: true},
		{name: "interval below a second", env: "n", args: []string{"--interval=100ms"}, wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("NODE_NAME", tt.env)
			got, err := parseFlags(tt.args)
			if (err != nil) != tt.wantErr {
				t.Fatalf("err = %v, wantErr %v", err, tt.wantErr)
			}
			if !tt.wantErr && got != tt.want {
				t.Errorf("options = %+v, want %+v", got, tt.want)
			}
		})
	}
}
