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
	"fmt"
	"net/http"

	admissionv1 "k8s.io/api/admission/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"
)

const (
	DeletionProtectedLabel = "cozystack.io/deletion-protected"
)

// DeletionProtectionWebhook denies DELETE requests on resources that carry
// the cozystack.io/deletion-protected label. The objectSelector on the
// ValidatingWebhookConfiguration ensures this handler is only called for
// labeled resources.
type DeletionProtectionWebhook struct{}

func (h *DeletionProtectionWebhook) Handle(ctx context.Context, req admission.Request) admission.Response {
	logger := log.FromContext(ctx).WithValues(
		"kind", req.Kind.Kind,
		"namespace", req.Namespace,
		"name", req.Name,
		"operation", req.Operation,
	)

	if req.Operation != admissionv1.Delete {
		return admission.Allowed("not a DELETE operation")
	}

	var identifier string
	if req.Namespace != "" {
		identifier = fmt.Sprintf("%s %s/%s", req.Kind.Kind, req.Namespace, req.Name)
	} else {
		identifier = fmt.Sprintf("%s %s", req.Kind.Kind, req.Name)
	}

	msg := fmt.Sprintf(
		"deletion of %s is protected by cozystack. "+
			"To force-delete, first remove the label: "+
			"kubectl label %s %s cozystack.io/deletion-protected-",
		identifier,
		kindToResourceArg(req.Kind.Kind, req.Namespace),
		req.Name,
	)

	logger.Info("DENIED deletion of protected resource", "resource", identifier)

	return admission.Response{
		AdmissionResponse: admissionv1.AdmissionResponse{
			Allowed: false,
			Result: &metav1.Status{
				Status:  metav1.StatusFailure,
				Message: msg,
				Reason:  metav1.StatusReasonForbidden,
				Code:    http.StatusForbidden,
			},
		},
	}
}

func kindToResourceArg(kind, namespace string) string {
	switch kind {
	case "Namespace":
		return "namespace"
	case "ConfigMap":
		return "configmap -n " + namespace
	case "HelmRelease":
		return "helmrelease.helm.toolkit.fluxcd.io -n " + namespace
	case "CustomResourceDefinition":
		return "crd"
	case "LinstorCluster":
		return "linstorcluster.piraeus.io -n " + namespace
	case "ClusterIssuer":
		return "clusterissuer.cert-manager.io"
	case "OCIRepository":
		return "ocirepository.source.toolkit.fluxcd.io -n " + namespace
	default:
		if namespace != "" {
			return kind + " -n " + namespace
		}
		return kind
	}
}
