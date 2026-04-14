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

package lineagecontrollerwebhook

import (
	"context"
	"net/http"
	"testing"

	admissionv1 "k8s.io/api/admission/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"
)

func TestDeletionProtectionWebhook_Handle(t *testing.T) {
	log.SetLogger(zap.New(zap.UseDevMode(true)))
	ctx := context.Background()

	handler := &DeletionProtectionWebhook{}

	tests := []struct {
		name        string
		req         admission.Request
		wantAllowed bool
		wantCode    int32
	}{
		{
			name: "deny DELETE on protected namespace",
			req: admission.Request{
				AdmissionRequest: admissionv1.AdmissionRequest{
					Operation: admissionv1.Delete,
					Name:      "tenant-root",
					Kind: metav1.GroupVersionKind{
						Group:   "",
						Version: "v1",
						Kind:    "Namespace",
					},
				},
			},
			wantAllowed: false,
			wantCode:    http.StatusForbidden,
		},
		{
			name: "deny DELETE on protected HelmRelease",
			req: admission.Request{
				AdmissionRequest: admissionv1.AdmissionRequest{
					Operation: admissionv1.Delete,
					Name:      "tenant-root",
					Namespace: "tenant-root",
					Kind: metav1.GroupVersionKind{
						Group:   "helm.toolkit.fluxcd.io",
						Version: "v2",
						Kind:    "HelmRelease",
					},
				},
			},
			wantAllowed: false,
			wantCode:    http.StatusForbidden,
		},
		{
			name: "deny DELETE on protected CRD",
			req: admission.Request{
				AdmissionRequest: admissionv1.AdmissionRequest{
					Operation: admissionv1.Delete,
					Name:      "packages.cozystack.io",
					Kind: metav1.GroupVersionKind{
						Group:   "apiextensions.k8s.io",
						Version: "v1",
						Kind:    "CustomResourceDefinition",
					},
				},
			},
			wantAllowed: false,
			wantCode:    http.StatusForbidden,
		},
		{
			name: "deny DELETE on protected LinstorCluster",
			req: admission.Request{
				AdmissionRequest: admissionv1.AdmissionRequest{
					Operation: admissionv1.Delete,
					Name:      "linstorcluster",
					Namespace: "cozy-linstor",
					Kind: metav1.GroupVersionKind{
						Group:   "piraeus.io",
						Version: "v1",
						Kind:    "LinstorCluster",
					},
				},
			},
			wantAllowed: false,
			wantCode:    http.StatusForbidden,
		},
		{
			name: "allow non-DELETE operation",
			req: admission.Request{
				AdmissionRequest: admissionv1.AdmissionRequest{
					Operation: admissionv1.Update,
					Name:      "tenant-root",
					Kind: metav1.GroupVersionKind{
						Group:   "",
						Version: "v1",
						Kind:    "Namespace",
					},
				},
			},
			wantAllowed: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resp := handler.Handle(ctx, tt.req)

			if resp.Allowed != tt.wantAllowed {
				t.Errorf("Allowed = %v, want %v", resp.Allowed, tt.wantAllowed)
			}
			if !tt.wantAllowed && resp.Result != nil && resp.Result.Code != tt.wantCode {
				t.Errorf("Code = %d, want %d", resp.Result.Code, tt.wantCode)
			}
		})
	}
}

func TestKindToResourceArg(t *testing.T) {
	tests := []struct {
		kind      string
		namespace string
		want      string
	}{
		{"Namespace", "", "namespace"},
		{"ConfigMap", "cozy-system", "configmap -n cozy-system"},
		{"HelmRelease", "tenant-root", "helmrelease.helm.toolkit.fluxcd.io -n tenant-root"},
		{"CustomResourceDefinition", "", "crd"},
		{"LinstorCluster", "cozy-linstor", "linstorcluster.piraeus.io -n cozy-linstor"},
		{"ClusterIssuer", "", "clusterissuer.cert-manager.io"},
		{"OCIRepository", "cozy-system", "ocirepository.source.toolkit.fluxcd.io -n cozy-system"},
		{"Unknown", "ns", "Unknown -n ns"},
		{"Unknown", "", "Unknown"},
	}

	for _, tt := range tests {
		t.Run(tt.kind, func(t *testing.T) {
			got := kindToResourceArg(tt.kind, tt.namespace)
			if got != tt.want {
				t.Errorf("kindToResourceArg(%q, %q) = %q, want %q", tt.kind, tt.namespace, got, tt.want)
			}
		})
	}
}
