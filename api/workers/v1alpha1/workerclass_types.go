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

package v1alpha1

import metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

// Bootstrap is the family that joins the worker to its tenant control plane.
// +kubebuilder:validation:Enum=talos;kairos
type Bootstrap string

const (
	BootstrapTalos  Bootstrap = "talos"
	BootstrapKairos Bootstrap = "kairos"
)

// WorkerImage identifies a public Kairos container disk and the exact release
// its kubelet and kubeadm carry.
type WorkerImage struct {
	// +required
	// +kubebuilder:validation:Pattern=`^v[0-9]+\.[0-9]+\.[0-9]+$`
	KubernetesVersion string `json:"kubernetesVersion"`
	// Reference carries no scheme or pull credential.
	// +required
	// +kubebuilder:validation:MaxLength=512
	// +kubebuilder:validation:Pattern=`^[^\s:/]+(:[0-9]+)?(/[^\s/]+)+$`
	// +kubebuilder:validation:XValidation:rule="self.matches('@sha256:[0-9a-f]{64}$')",message="reference must be pinned by sha256 digest"
	Reference string `json:"reference"`
}

// TalosSettings are a baseline, not schema defaults on a pool. Pointers keep
// an omitted setting distinct from an explicitly empty one.
type TalosSettings struct {
	// +optional
	Version *string `json:"version,omitempty"`
	// +optional
	SchematicID *string `json:"schematicID,omitempty"`
	// +optional
	ImageFactoryURL *string `json:"imageFactoryURL,omitempty"`
	// +optional
	ImageFactoryCA *string `json:"imageFactoryCA,omitempty"`
	// +optional
	InstallerRepository *string `json:"installerRepository,omitempty"`
	// RegistryMirrors is replaced as a unit by a pool's map; {} clears it.
	// +optional
	// +kubebuilder:validation:MaxProperties=64
	RegistryMirrors *map[string]RegistryMirror `json:"registryMirrors,omitempty"`
}

type RegistryMirror struct {
	// +optional
	// +kubebuilder:validation:MaxItems=32
	Endpoints *[]string `json:"endpoints,omitempty"`
	// +optional
	SkipFallback *bool `json:"skipFallback,omitempty"`
	// +optional
	OverridePath *bool `json:"overridePath,omitempty"`
}

type KernelModule struct {
	// +required
	// +kubebuilder:validation:Pattern=`^[a-z0-9_-]+$`
	Name string `json:"name"`
	// +optional
	Parameters []string `json:"parameters,omitempty"`
}

// FactorySource resolves its image from the effective Talos settings. It is
// not a declaration of fixed image bytes.
type FactorySource struct {
	// +optional
	Version string `json:"version,omitempty"`
	// +optional
	SchematicID string `json:"schematicID,omitempty"`
	// +optional
	ImageFactoryURL string `json:"imageFactoryURL,omitempty"`
}

// BuiltinSource identifies a fixed golden image, so its compatibility is
// explicit rather than following a later chart default.
type BuiltinSource struct {
	// +required
	// +kubebuilder:validation:Pattern=`^v[0-9]+\.[0-9]+\.[0-9]+(-[a-zA-Z0-9.-]+)?$`
	Version string `json:"version"`
	// +required
	// +kubebuilder:validation:Pattern=`^[a-z0-9]+$`
	SchematicID string `json:"schematicID"`
}

// TalosImageSource selects one substrate image source.
// +kubebuilder:validation:XValidation:rule="!(has(self.factory) && has(self.builtin))",message="select only one of factory or builtin"
type TalosImageSource struct {
	// +optional
	Factory *FactorySource `json:"factory,omitempty"`
	// +optional
	Builtin *BuiltinSource `json:"builtin,omitempty"`
}

type KubeVirtSource struct {
	// +optional
	OSImage *TalosImageSource `json:"osImage,omitempty"`
}

type ProxmoxSource struct {
	// TemplateTags name the existing template, not an image import. Its Talos
	// version and schematic are declared by the class's talos fields.
	// +required
	// +kubebuilder:validation:MinItems=1
	TemplateTags []string `json:"templateTags"`
}

type Substrates struct {
	// +optional
	KubeVirt *KubeVirtSource `json:"kubevirt,omitempty"`
	// +optional
	Proxmox *ProxmoxSource `json:"proxmox,omitempty"`
}

// WorkerClassSpec is immutable: publish another class to change its baseline.
// +kubebuilder:validation:XValidation:rule="self == oldSelf",message="spec is immutable; create a new WorkerClass"
// +kubebuilder:validation:XValidation:rule="self.bootstrap == 'kairos' || !has(self.image)",message="image is allowed only with bootstrap kairos"
// +kubebuilder:validation:XValidation:rule="self.bootstrap == 'talos' || (!has(self.talos) && !has(self.kernelModules) && !has(self.substrates))",message="talos, kernelModules and substrates require bootstrap talos"
// +kubebuilder:validation:XValidation:rule="!has(self.substrates) || !has(self.substrates.proxmox) || (has(self.talos) && has(self.talos.version) && has(self.talos.schematicID))",message="a fixed Proxmox template requires declared talos.version and talos.schematicID"
type WorkerClassSpec struct {
	// +required
	Bootstrap Bootstrap `json:"bootstrap"`
	// +optional
	Image *WorkerImage `json:"image,omitempty"`
	// +optional
	Talos *TalosSettings `json:"talos,omitempty"`
	// Nil inherits the automatic NVIDIA module rule; an empty list disables it.
	// +optional
	KernelModules *[]KernelModule `json:"kernelModules,omitempty"`
	// +optional
	Substrates *Substrates `json:"substrates,omitempty"`
}

// WorkerClass supplies a worker bootstrap and image baseline. Only the
// managed-Kubernetes package installs this kind and its controller.
// +kubebuilder:object:root=true
// +kubebuilder:resource:scope=Cluster
// +kubebuilder:metadata:annotations="helm.sh/resource-policy=keep"
// +kubebuilder:validation:XValidation:rule="!(self.metadata.name in ['talos', 'kairos']) || (self.spec.bootstrap == self.metadata.name && !has(self.spec.image) && !has(self.spec.talos) && !has(self.spec.kernelModules) && !has(self.spec.substrates))",message="built-in classes carry only bootstrap equal to their name"
// +kubebuilder:printcolumn:name="Bootstrap",type="string",JSONPath=".spec.bootstrap"
// +kubebuilder:printcolumn:name="Kubernetes",type="string",JSONPath=".spec.image.kubernetesVersion"
// +kubebuilder:printcolumn:name="Age",type="date",JSONPath=".metadata.creationTimestamp"
type WorkerClass struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`
	// +required
	Spec WorkerClassSpec `json:"spec"`
}

// WorkerClassList contains WorkerClass resources.
// +kubebuilder:object:root=true
type WorkerClassList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []WorkerClass `json:"items"`
}
