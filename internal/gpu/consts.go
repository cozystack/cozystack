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

	// LabelDeviceReference marks a VMI template, and through KubeVirt its
	// virt-launcher pod, as carrying device references. The webhooks select
	// on it, so only such VMIs and pods can be blocked by their outage.
	LabelDeviceReference = Group + "/device-reference"
	// AnnotationDevices holds the VMI's device references as a JSON list;
	// model.ParseDeviceRefs reads it.
	AnnotationDevices = Group + "/devices"
	// VMIGPUNamePrefix starts the name of every gpus entry the webhook adds
	// to a VMI, so a reinvocation recognises its own entries.
	VMIGPUNamePrefix = "gpudev-"

	// LabelTemplateDevice, valued LabelValueTrue, marks a GPUDevice the
	// webhook created from a deviceTemplate entry. Tenants cannot set it. It
	// does not carry the VMI name, which can exceed a label value's 63
	// characters: the device is named by model.TemplateDeviceName, and its
	// VMI is status.attachedTo.vmi.
	LabelTemplateDevice = Group + "/template-device"

	// LabelUsageMonitor is the reserved selector key of the controller-owned
	// WorkloadMonitor. Admission refuses it on every object in a tenant
	// namespace, so the monitor matches nothing and only the controller
	// writes GPU usage records.
	LabelUsageMonitor = Group + "/usage-monitor"
	// UsageMonitorName is the controller-owned WorkloadMonitor in each
	// tenant namespace that holds a GPUDevice.
	UsageMonitorName = "gpu-devices"
	// WorkloadKind is status.kind of a GPUDevice usage record.
	WorkloadKind = "gpu-device"

	// FinalizerClass keeps a deleted GPUClass until its last Bound,
	// Attached or Lost device is gone.
	FinalizerClass = Group + "/class-protection"
	// FinalizerDevice keeps a deleted GPUDevice, its binding and its usage
	// record until no VMI uses it.
	FinalizerDevice = Group + "/device-protection"

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
