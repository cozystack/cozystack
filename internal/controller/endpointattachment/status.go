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

package endpointattachment

import (
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	cozyv1alpha1 "github.com/cozystack/cozystack/api/localsdn/v1alpha1"
)

// provisionedMessageSuffix is appended to every Provisioned=True message so
// that a green attachment is not read as a reachability guarantee: the CNI's
// own policy objects (VPC gateways, SecurityGroups, NetworkPolicies) still
// gate the path, and this controller cannot see them.
const provisionedMessageSuffix = "; the address is bound and the Service carries it, end-to-end reachability additionally depends on the CNI's own policy objects"

func setCondition(ea *cozyv1alpha1.EndpointAttachment, condType string, status metav1.ConditionStatus, reason, message string) {
	meta.SetStatusCondition(&ea.Status.Conditions, metav1.Condition{
		Type:               condType,
		Status:             status,
		Reason:             reason,
		Message:            message,
		ObservedGeneration: ea.Generation,
	})
}

func setResolved(ea *cozyv1alpha1.EndpointAttachment, reason, message string) {
	setCondition(ea, cozyv1alpha1.ConditionResolved, metav1.ConditionTrue, reason, message)
}

func setUnresolved(ea *cozyv1alpha1.EndpointAttachment, reason, message string) {
	setCondition(ea, cozyv1alpha1.ConditionResolved, metav1.ConditionFalse, reason, message)
}

func setProvisioned(ea *cozyv1alpha1.EndpointAttachment, message string) {
	setCondition(ea, cozyv1alpha1.ConditionProvisioned, metav1.ConditionTrue, cozyv1alpha1.ReasonProvisioned, message+provisionedMessageSuffix)
}

func setUnprovisioned(ea *cozyv1alpha1.EndpointAttachment, reason, message string) {
	setCondition(ea, cozyv1alpha1.ConditionProvisioned, metav1.ConditionFalse, reason, message)
}

// projectPhase derives status.phase from the conditions. It is never stored
// independently: Detached is "the endpoint went away after an address was
// held", which the Provisioned reason records.
func projectPhase(ea *cozyv1alpha1.EndpointAttachment) {
	resolved := meta.IsStatusConditionTrue(ea.Status.Conditions, cozyv1alpha1.ConditionResolved)
	provisioned := meta.IsStatusConditionTrue(ea.Status.Conditions, cozyv1alpha1.ConditionProvisioned)
	switch {
	case resolved && provisioned:
		ea.Status.Phase = cozyv1alpha1.EndpointAttachmentAttached
	case !resolved && conditionReason(ea, cozyv1alpha1.ConditionProvisioned) == cozyv1alpha1.ReasonEndpointDetached:
		ea.Status.Phase = cozyv1alpha1.EndpointAttachmentDetached
	default:
		ea.Status.Phase = cozyv1alpha1.EndpointAttachmentPending
	}
}

func conditionReason(ea *cozyv1alpha1.EndpointAttachment, condType string) string {
	if c := meta.FindStatusCondition(ea.Status.Conditions, condType); c != nil {
		return c.Reason
	}
	return ""
}
