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

package gpu

const (
	// Group is the API group of the GPU kinds and the prefix of every key below.
	Group = "gpu.cozystack.io"

	// LabelClaimReference marks a VMI template, and through KubeVirt its
	// virt-launcher pod, as carrying claim references. The webhooks select
	// on it, so only such VMIs and pods can be blocked by their outage.
	LabelClaimReference = Group + "/claim-reference"
	// AnnotationClaims holds the VMI's claim references as a JSON list;
	// model.ParseClaimRefs reads it.
	AnnotationClaims = Group + "/claims"
	// VMIGPUNamePrefix starts the name of every gpus entry the webhook adds
	// to a VMI, so a reinvocation recognises its own entries.
	VMIGPUNamePrefix = "gpuclaim-"

	// SchedulingGateClaims holds a virt-launcher pod of a VMI with claim
	// references until the controller has placed the VMI's claims on one
	// node and pinned the pod there.
	SchedulingGateClaims = Group + "/claims"

	// LabelTemplateClaim, valued LabelValueTrue, marks a GPUClaim the
	// controller created from a claimTemplate entry. Tenants cannot set it.
	// It does not carry the VMI name, which can exceed a label value's 63
	// characters: the claim is named by model.TemplateClaimName, and its
	// VMI is status.attachedTo.vmi.
	LabelTemplateClaim = Group + "/template-claim"

	// WorkloadKind is status.kind of a GPUClaim usage record. The record
	// is owned by its GPUClaim through an ownerReference and carries no
	// WorkloadMonitor label.
	WorkloadKind = "gpu-claim"

	// FinalizerClass keeps a deleted GPUClass until its last Bound,
	// Attached or Lost claim is gone.
	FinalizerClass = Group + "/class-protection"
	// FinalizerClaim keeps a deleted GPUClaim, its binding and its usage
	// record until no VMI uses it.
	FinalizerClaim = Group + "/claim-protection"

	// LabelNamespaceVGPU is the effective vGPU entitlement the tenant chart
	// writes on every tenant namespace. Only LabelValueTrue entitles.
	LabelNamespaceVGPU = "namespace.cozystack.io/vgpu"
	LabelValueTrue     = "true"

	// QuotaKeyPrefix starts every GPU budget key in a tenant's
	// resourceQuotas: QuotaKeyPrefix+<class> per class and QuotaKeyTotal
	// across classes.
	QuotaKeyPrefix = Group + "/"
	// QuotaTotal is the reserved class name of the cross-class budget.
	QuotaTotal    = "total"
	QuotaKeyTotal = QuotaKeyPrefix + QuotaTotal

	// SettingsConfigMap is written by the controller and read by the
	// ValidatingAdmissionPolicies (as paramRef) and by cozystack-api.
	SettingsConfigMap = "cozystack-gpu-settings"
	// SettingsNamespace is the namespace of SettingsConfigMap.
	SettingsNamespace = "cozy-system"
	// SettingsKeyResourceNames is the effective GPU resource-name set,
	// sorted and comma-separated; ConfigMap values cannot hold a list, and a
	// resource name cannot contain a comma.
	SettingsKeyResourceNames = "resourceNames"
	// SettingsKeyRawResourceNames is RawNamesAllowed or RawNamesForbidden.
	SettingsKeyRawResourceNames = "rawResourceNames"

	// ConditionLostCapacity is set on a GPUClass and a GPUNode whose node
	// has lost capacity for longer than the grace period.
	ConditionLostCapacity = "LostCapacity"
	// ConditionResourceNameConflict is set on two classes that share a
	// resource name on a node they both select, and on that GPUNode. The
	// node is not used for binding.
	ConditionResourceNameConflict = "ResourceNameConflict"
)

// RawNamesPolicy is the hoster's gpu.rawResourceNames setting.
type RawNamesPolicy string

const (
	RawNamesAllowed   RawNamesPolicy = "allowed"
	RawNamesForbidden RawNamesPolicy = "forbidden"
)
