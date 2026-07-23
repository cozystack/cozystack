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

package dbautoscaler

import (
	"context"
	"encoding/json"
	"fmt"

	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"

	autoscalingv1alpha1 "github.com/cozystack/cozystack/api/autoscaling/v1alpha1"
)

// ProjectedMarkerAnnotation is the managed-by marker as it appears on the backing
// Flux HelmRelease: the aggregated apps API prefixes every projected Application
// annotation with "apps.cozystack.io-" (pkg/registry/apps/application/rest.go).
const ProjectedMarkerAnnotation = "apps.cozystack.io-" + autoscalingv1alpha1.ManagedByAnnotation

// ReplicasOwnershipValidator rejects writes to a DHA-managed database's replicas
// value from anyone other than the autoscaler.
//
// It is registered against the backing Flux HelmRelease, NOT the aggregated
// apps.cozystack.io API: kube-apiserver does not run admission webhooks for
// aggregated APIServices (it proxies those requests to the extension server), so
// a webhook on apps.cozystack.io would never fire. The HelmRelease is a Flux CRD
// served by kube-apiserver, where admission runs and where a force-applying
// GitOps writer (the design's named competing-writer case) writes directly.
// Replicas live in the HelmRelease at spec.values.replicas.
//
// Legitimate writes that flow through the apps API — the autoscaler's own patch
// and a tenant's kubectl edit — reach the HelmRelease as the extension server's
// ServiceAccount, so AllowedUsers lists both the operator and that server. A
// direct HelmRelease write from any other identity (Flux) is rejected.
type ReplicasOwnershipValidator struct {
	// AllowedUsers may change a managed replicas value: the operator SA and the
	// apps-API extension-server SA.
	AllowedUsers []string
	// MarkerAnnotation is the managed-by marker key on the HelmRelease.
	MarkerAnnotation string
	// ReplicasPath is the nested path to the replica count in the object
	// (HelmRelease: spec.values.replicas).
	ReplicasPath []string
}

// admissionObject is the minimal shape we read from the (Old)Object raw bytes.
type admissionObject struct {
	Metadata struct {
		Annotations map[string]string `json:"annotations"`
	} `json:"metadata"`
	Spec map[string]any `json:"spec"`
}

func (v *ReplicasOwnershipValidator) markerKey() string {
	if v.MarkerAnnotation != "" {
		return v.MarkerAnnotation
	}
	return ProjectedMarkerAnnotation
}

func (v *ReplicasOwnershipValidator) replicasPath() []string {
	if len(v.ReplicasPath) > 0 {
		return v.ReplicasPath
	}
	return []string{"values", "replicas"}
}

func (v *ReplicasOwnershipValidator) allowed(user string) bool {
	for _, u := range v.AllowedUsers {
		if u == user {
			return true
		}
	}
	return false
}

// evaluateOwnership is the pure admission decision. allowed=false means the
// write must be rejected.
func evaluateOwnership(oldRaw, newRaw []byte, username, markerKey string, replicasPath, allowedUsers []string) (allowed bool, msg string) {
	var oldObj, newObj admissionObject
	// A create (no old object) or an unparsable body is allowed: the webhook only
	// guards updates to a managed replicas value it can read with certainty.
	if len(oldRaw) == 0 {
		return true, ""
	}
	if err := json.Unmarshal(oldRaw, &oldObj); err != nil {
		return true, ""
	}
	if err := json.Unmarshal(newRaw, &newObj); err != nil {
		return true, ""
	}

	// Only guard objects that a DHA currently manages.
	marker := oldObj.Metadata.Annotations[markerKey]
	if marker == "" {
		return true, ""
	}

	oldReplicas, oldOK := nestedNumber(oldObj.Spec, replicasPath)
	newReplicas, newOK := nestedNumber(newObj.Spec, replicasPath)
	// If replicas is unchanged (or unreadable on either side), allow.
	if !oldOK || !newOK || oldReplicas == newReplicas {
		return true, ""
	}

	// The autoscaler and the apps-API extension server may change replicas.
	for _, u := range allowedUsers {
		if u == username {
			return true, ""
		}
	}

	return false, fmt.Sprintf(
		"replicas of this database is managed by DatabaseHorizontalAutoscaler %q; "+
			"delete the DHA to regain manual control (attempted change %v -> %v by %q)",
		marker, oldReplicas, newReplicas, username)
}

// nestedNumber navigates spec by path and reads a numeric leaf as float64.
func nestedNumber(spec map[string]any, path []string) (float64, bool) {
	var cur any = spec
	for _, k := range path {
		m, ok := cur.(map[string]any)
		if !ok {
			return 0, false
		}
		cur, ok = m[k]
		if !ok {
			return 0, false
		}
	}
	return numericValue(cur)
}

// numericValue reads a JSON number (float64) or integer as float64.
func numericValue(v any) (float64, bool) {
	switch n := v.(type) {
	case float64:
		return n, true
	case int64:
		return float64(n), true
	case int:
		return float64(n), true
	default:
		return 0, false
	}
}

// Handle implements admission.Handler.
func (v *ReplicasOwnershipValidator) Handle(_ context.Context, req admission.Request) admission.Response {
	allowed, msg := evaluateOwnership(
		req.OldObject.Raw, req.Object.Raw,
		req.UserInfo.Username, v.markerKey(), v.replicasPath(), v.AllowedUsers)
	if allowed {
		return admission.Allowed("")
	}
	return admission.Denied(msg)
}
