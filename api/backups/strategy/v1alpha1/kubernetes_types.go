// SPDX-License-Identifier: Apache-2.0
// Package v1alpha1 defines strategy.backups.cozystack.io API types.
//
// Group: strategy.backups.cozystack.io
// Version: v1alpha1
package v1alpha1

import (
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
)

func init() {
	SchemeBuilder.Register(func(s *runtime.Scheme) error {
		s.AddKnownTypes(GroupVersion,
			&Kubernetes{},
			&KubernetesList{},
		)
		return nil
	})
}

const (
	KubernetesStrategyKind = "Kubernetes"
)

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Cluster

// Kubernetes defines a backup strategy for a tenant Kubernetes application.
// A tenant cluster keeps its state in two places the platform can reach: the
// cluster's key prefix in the tenant's shared etcd DataStore, and host-side
// objects (control-plane PKI Secrets, the application HelmReleases, and the
// DataVolumes that back the tenant's PersistentVolumes). The strategy captures
// the first with a one-shot Job and the second with Velero. The two are taken
// one after another, so a backup is crash-consistent at best.
type Kubernetes struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   KubernetesSpec   `json:"spec,omitempty"`
	Status KubernetesStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// KubernetesList contains a list of Kubernetes backup strategies.
type KubernetesList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []Kubernetes `json:"items"`
}

// KubernetesSpec specifies the desired behavior of a Kubernetes backup strategy.
type KubernetesSpec struct {
	// Template holds a PodTemplateSpec for the Job that dumps the cluster's
	// key prefix from its etcd DataStore to object storage, writes it back on
	// restore, and deletes the object on cleanup. The Job runs in the
	// application namespace. Helm-like Go templates are supported in every
	// string field and are rendered against:
	//   - `.Release.Name` and `.Release.Namespace` — the application's name and
	//     namespace; `.Release.Prefix` is `kubernetes-`.
	//   - `.Mode` — `"backup"`, `"restore"` or `"cleanup"`.
	//   - `.ObjectKey` — the object-storage key of this backup's dump.
	//   - `.Datastore.Prefix` — the cluster's key prefix, without slashes.
	//   - `.Datastore.Endpoints` — comma-separated etcd client URLs.
	//   - `.Datastore.CertificateSecret` — the Secret holding the cluster's
	//     etcd client certificate (`ca.crt`, `server.crt`, `server.key`).
	//   - `.Parameters` — the resolved BackupClass strategy parameters.
	//
	// Stored schemaless for the same CRD-size reason as redis_types.go.
	//
	// +kubebuilder:validation:Schemaless
	// +kubebuilder:pruning:PreserveUnknownFields
	Template corev1.PodTemplateSpec `json:"template"`

	// Velero holds the Velero Backup and Restore specs for the host-side
	// objects and volumes. Rendered against `.Application` and `.Parameters`,
	// like the Velero strategy.
	//
	// +kubebuilder:validation:Schemaless
	// +kubebuilder:pruning:PreserveUnknownFields
	Velero VeleroTemplate `json:"velero"`

	// CertificateWarningThreshold is how close to expiry a control-plane
	// certificate may be before a backup records a warning. Defaults to 720h.
	// +optional
	CertificateWarningThreshold *metav1.Duration `json:"certificateWarningThreshold,omitempty"`
}

type KubernetesStatus struct {
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}
