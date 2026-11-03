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

// MaxVMIUnits bounds tenant-controlled per-unit admission expansion. It is
// an implementation safety limit, not a class capacity or tenant budget.
const MaxVMIUnits = 1024

const (
	// LabelGPUReference, valued LabelValueTrue, marks a VMI template with
	// class entries; KubeVirt copies it to the VMI and its virt-launcher
	// pod. The webhooks select on it, so only such VMIs and pods can be
	// blocked by their outage.
	LabelGPUReference = "cozystack.io/gpu-reference"
	// AnnotationGPUs holds the class entries of a VMI template as a JSON
	// list of {"className", "count"}.
	AnnotationGPUs = "cozystack.io/gpus"
	// VMIGPUNamePrefix starts the name of every gpus entry the VMI webhook
	// writes, so a reinvocation recognises its own entries.
	VMIGPUNamePrefix = "gpu-"

	// LabelNamespaceVGPU is the effective vGPU entitlement the tenant chart
	// writes on every tenant namespace. Only LabelValueTrue entitles.
	LabelNamespaceVGPU = "namespace.cozystack.io/vgpu"
	LabelValueTrue     = "true"

	// QuotaKeyDomain is the domain of every GPU budget key in a tenant's
	// resourceQuotas, after DRA's <class>.deviceclass.resource.k8s.io/devices.
	QuotaKeyDomain = "gpuclass.cozystack.io"
	// QuotaKeyTotal is the budget over every class; a class's own key is
	// model.ClassQuotaKey.
	QuotaKeyTotal = QuotaKeyDomain + "/devices"

	// LicenseSecretNamespace holds the hoster's license Secrets that
	// GPUClass.spec.license.secretName names.
	LicenseSecretNamespace = "cozy-system"
	// LabelGPUClass names the class of a license copy. The controller
	// touches only Secrets carrying it.
	LabelGPUClass = "cozystack.io/gpuclass"
	// LicenseTokenKey is the token key of the hoster's Secret and of each
	// copy; LicenseGriddConfKey is the copy's gridd.conf with the class's
	// FeatureType.
	LicenseTokenKey     = "client_configuration_token.tok"
	LicenseGriddConfKey = "gridd.conf"
	// LicenseVolumeLabel is the volume label of the license disk a VMI of
	// a licensed vGPU class gets; guest images find the disk by it.
	LicenseVolumeLabel = "GPULICENSE"
)

// LicenseSecretName is the name of a class's license copy in an entitled
// namespace.
func LicenseSecretName(class string) string {
	return "gpuclass-" + class + "-license"
}
