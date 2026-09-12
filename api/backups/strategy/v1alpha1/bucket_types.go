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
			&Bucket{},
			&BucketList{},
		)
		return nil
	})
}

const (
	BucketStrategyKind = "Bucket"
)

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Cluster

// Bucket defines a backup strategy for the apps.cozystack.io/Bucket
// application (SeaweedFS-backed S3 object storage provisioned through COSI).
// The engine has no operator-native Backup CRD, so the driver copies objects
// S3-to-S3: each BackupJob runs a one-shot mirror Job that reads the source
// bucket and writes to the platform cozy-backups bucket under a per-release
// prefix; each RestoreJob mirrors that prefix back into a target bucket
// (in-place, or a differently-named copy). The driver provisions the COSI
// BucketAccess it needs to read the source and write the target - the
// application chart is never touched.
type Bucket struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   BucketSpec   `json:"spec,omitempty"`
	Status BucketStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// BucketList contains a list of Bucket backup strategies.
type BucketList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []Bucket `json:"items"`
}

// BucketSpec specifies the desired S3-mirror backup strategy.
type BucketSpec struct {
	// Template carries the destination (cozy-backups) coordinates and the
	// mirror Job knobs. String fields support Helm-style Go templating with
	// two top-level values:
	//   .Application - the application object (apps.cozystack.io/Bucket)
	//   .Parameters  - the parameters from the matched BackupClassStrategy.
	//                  These values MUST NOT carry credentials. Route S3
	//                  access keys through the *SecretKeyRef fields.
	Template BucketTemplate `json:"template"`
}

// BucketTemplate describes where backups are written and how the mirror Job
// is shaped. The source coordinates are not carried here: the driver reads
// them from the COSI BucketClaim/BucketAccess of the application being backed
// up.
type BucketTemplate struct {
	// Destination is the S3 target the source objects are mirrored to (the
	// platform cozy-backups bucket for the default strategy).
	Destination BucketDestination `json:"destination"`

	// Image is the container image running the mirror. It must carry the
	// backupstrategy-controller binary so the Job can invoke its `s3-mirror`
	// subcommand; the default strategy sets it to the controller image.
	// +kubebuilder:validation:MinLength=1
	Image string `json:"image"`

	// AppEndpointOverride overrides the S3 endpoint of the application-side
	// bucket (the source on backup, the target on restore) that the mirror
	// connects to, instead of the endpoint advertised in the COSI BucketInfo.
	// It is an escape hatch for environments where the advertised endpoint is
	// not reachable from in-cluster Pods (for example a CI cluster whose COSI
	// endpoint is an external ingress placeholder) - point it at the in-cluster
	// S3 Service there. Leave empty in production so the BucketInfo endpoint is
	// used. Templating is supported.
	// +optional
	AppEndpointOverride string `json:"appEndpointOverride,omitempty"`

	// ServerSideCopy asks the mirror to attempt an S3 CopyObject (bytes stay
	// inside the object store) before falling back to a streamed client-side
	// copy. It only helps when source and destination share one endpoint and
	// a single credential is authorized on both buckets; left unset (false)
	// the mirror always streams, which works regardless of the S3 identity
	// model.
	// +optional
	ServerSideCopy bool `json:"serverSideCopy,omitempty"`

	// Resources sets requests/limits on the mirror container so one tenant's
	// backup cannot starve a node. Templating is not applied to this field.
	// +optional
	Resources *corev1.ResourceRequirements `json:"resources,omitempty"`
}

// BucketDestination mirrors the S3 target of the copy. Bucket, Endpoint,
// Prefix and Region support templating; credentials are referenced by Secret
// name/key and never templated into a value.
type BucketDestination struct {
	// Bucket is the destination S3 bucket name. Templating is supported.
	// +kubebuilder:validation:MinLength=1
	Bucket string `json:"bucket"`

	// Endpoint is the destination S3 endpoint. A scheme is optional; the
	// mirror prepends https:// when absent. Templating is supported.
	// +kubebuilder:validation:MinLength=1
	Endpoint string `json:"endpoint"`

	// Prefix is prepended to every object key written for this release, so
	// several releases can share one destination bucket. The driver appends
	// a per-backup segment under it. Templating is supported; the default
	// strategy sets it to "<namespace>/<name>/".
	// +optional
	Prefix string `json:"prefix,omitempty"`

	// Region is the S3 region name. Templating is supported.
	// +optional
	Region string `json:"region,omitempty"`

	// AccessKeyIDSecretKeyRef references a Secret key holding the destination
	// access key id. Templating is supported on Name.
	AccessKeyIDSecretKeyRef BucketSecretKeySelector `json:"accessKeyIdSecretKeyRef"`

	// SecretAccessKeySecretKeyRef references a Secret key holding the
	// destination secret access key. Templating is supported on Name.
	SecretAccessKeySecretKeyRef BucketSecretKeySelector `json:"secretAccessKeySecretKeyRef"`

	// TLS configures how the mirror reaches the destination endpoint.
	// +optional
	TLS *BucketTLS `json:"tls,omitempty"`
}

// BucketSecretKeySelector selects one key of a Secret in the application's
// namespace. Both Name and Key are templatable (the strategy is rendered via
// internal/template, which walks every string in the marshalled template).
type BucketSecretKeySelector struct {
	// Name is the Secret name. Templating is supported.
	Name string `json:"name"`

	// Key is the key within the Secret holding the value. Templating is
	// supported.
	Key string `json:"key"`
}

// BucketTLS controls TLS verification for an S3 endpoint. The COSI endpoint
// projected by the platform stores a bare host, and cozystack's SeaweedFS
// serves TLS with a self-signed CA on the in-cluster Service, so a mirror
// pointed at the in-cluster endpoint needs either the CA or an explicit skip.
type BucketTLS struct {
	// InsecureSkipVerify disables certificate verification. Use it only for a
	// self-signed in-cluster endpoint that no CA bundle is plumbed for.
	// +optional
	InsecureSkipVerify bool `json:"insecureSkipVerify,omitempty"`

	// CASecretKeyRef references a Secret carrying a PEM CA bundle the mirror
	// should trust. Templating is supported on Name and Key.
	// +optional
	CASecretKeyRef *BucketSecretKeySelector `json:"caSecretKeyRef,omitempty"`
}

// BucketStatus reports observed state for the strategy CR.
type BucketStatus struct {
	// Conditions holds the latest available observations.
	// +optional
	// +listType=map
	// +listMapKey=type
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}
