/*
Copyright 2025 The Cozystack Authors.

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
	"strings"
	"testing"

	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func TestNewGatewayManagerFlags(t *testing.T) {
	reader := fake.NewClientBuilder().Build()
	for _, tc := range []struct {
		address, secret, wantErr string
	}{
		{"ssl:ovn-nb.cozy-kubeovn.svc:6641", "cozy-kubeovn/kube-ovn-tls", ""},
		{"ssl:192.0.2.11:6641,ssl:192.0.2.12:6641", "cozy-kubeovn/kube-ovn-tls", ""},
		{"tcp:[::1]:6641", "cozy-kubeovn/kube-ovn-tls", ""},
		{"ssl:ovn-nb:6641", "kube-ovn-tls", "want <namespace>/<name>"},
		{"ssl:ovn-nb:6641", "a/b/c", "want <namespace>/<name>"},
		{"ssl:ovn-nb:6641", "/kube-ovn-tls", "want <namespace>/<name>"},
		{"unix:/var/run/ovn/ovnnb_db.sock", "cozy-kubeovn/kube-ovn-tls", "want ssl:<host>:<port>"},
		{"ssl:ovn-nb", "cozy-kubeovn/kube-ovn-tls", "missing port"},
		{" , ", "cozy-kubeovn/kube-ovn-tls", "no OVSDB address"},
	} {
		_, err := newGatewayManager(reader, tc.address, tc.secret)
		switch {
		case tc.wantErr == "" && err != nil:
			t.Errorf("%q %q: %v", tc.address, tc.secret, err)
		case tc.wantErr != "" && (err == nil || !strings.Contains(err.Error(), tc.wantErr)):
			t.Errorf("%q %q: error %v, want one with %q", tc.address, tc.secret, err, tc.wantErr)
		}
	}
}
