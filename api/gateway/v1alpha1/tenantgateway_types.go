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

package v1alpha1

import (
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// CertMode selects how the per-tenant Gateway sources TLS certificates.
// +kubebuilder:validation:Enum=http01;dns01
type CertMode string

const (
	// CertModeHTTP01 issues per-listener Certificates via HTTP-01 ACME
	// against the tenant Gateway's `http` listener. Default. Each
	// published app gets its own listener and its own Certificate.
	CertModeHTTP01 CertMode = "http01"

	// CertModeDNS01 issues a single wildcard Certificate per tenant
	// (apex + *.apex) via DNS-01 ACME. Operator must configure a DNS
	// provider in Spec.DNS01.
	CertModeDNS01 CertMode = "dns01"
)

// IssuerName selects the Let's Encrypt environment the per-tenant
// Issuer points at. Operators running staging or non-production
// clusters should pick "letsencrypt-stage" to avoid eating into the
// production rate limits.
// +kubebuilder:validation:Enum=letsencrypt-prod;letsencrypt-stage
type IssuerName string

const (
	IssuerNameLetsEncryptProd  IssuerName = "letsencrypt-prod"
	IssuerNameLetsEncryptStage IssuerName = "letsencrypt-stage"
)

// DNS01Provider names a supported cert-manager DNS-01 solver.
// +kubebuilder:validation:Enum=cloudflare;route53;digitalocean;rfc2136
type DNS01Provider string

// DNS01Config configures the DNS-01 solver when CertMode=dns01. Only
// the field corresponding to Provider is read; others are ignored.
type DNS01Config struct {
	// Provider selects which DNS-01 solver block to render.
	// +kubebuilder:default=cloudflare
	Provider DNS01Provider `json:"provider,omitempty"`

	// Cloudflare config. Required when Provider=cloudflare.
	// +optional
	Cloudflare *CloudflareDNS01 `json:"cloudflare,omitempty"`

	// Route53 config. Required when Provider=route53.
	// +optional
	Route53 *Route53DNS01 `json:"route53,omitempty"`

	// DigitalOcean config. Required when Provider=digitalocean.
	// +optional
	DigitalOcean *DigitalOceanDNS01 `json:"digitalocean,omitempty"`

	// RFC2136 config. Required when Provider=rfc2136.
	// +optional
	RFC2136 *RFC2136DNS01 `json:"rfc2136,omitempty"`
}

// CloudflareDNS01 configures the cloudflare solver.
type CloudflareDNS01 struct {
	// APITokenSecretRef references a Secret holding a Cloudflare API
	// token with Zone:Read + Zone:DNS:Edit on the tenant's apex zone.
	// +required
	APITokenSecretRef corev1.SecretKeySelector `json:"apiTokenSecretRef"`
}

// Route53DNS01 configures the AWS Route53 solver.
type Route53DNS01 struct {
	// Region is the AWS region of the hosted zone.
	// +required
	Region string `json:"region"`

	// AccessKeyID is the IAM access key ID. Optional when running with
	// IRSA / instance profile.
	// +optional
	AccessKeyID string `json:"accessKeyID,omitempty"`

	// SecretAccessKeySecretRef references a Secret holding the IAM
	// secret access key. Optional when running with IRSA / instance
	// profile.
	// +optional
	SecretAccessKeySecretRef *corev1.SecretKeySelector `json:"secretAccessKeySecretRef,omitempty"`
}

// DigitalOceanDNS01 configures the DigitalOcean solver.
type DigitalOceanDNS01 struct {
	// TokenSecretRef references a Secret holding a DigitalOcean API
	// token with write access to the tenant's apex domain.
	// +required
	TokenSecretRef corev1.SecretKeySelector `json:"tokenSecretRef"`
}

// RFC2136DNS01 configures the BIND-style RFC 2136 dynamic-update solver.
type RFC2136DNS01 struct {
	// Nameserver is the host:port of the authoritative nameserver
	// accepting dynamic updates.
	// +required
	Nameserver string `json:"nameserver"`

	// TSIGKeyName names the TSIG key authorising the update.
	// +required
	TSIGKeyName string `json:"tsigKeyName"`

	// TSIGAlgorithm is the TSIG HMAC algorithm. Default HMACSHA256.
	// +kubebuilder:default=HMACSHA256
	// +optional
	TSIGAlgorithm string `json:"tsigAlgorithm,omitempty"`

	// TSIGSecretSecretRef references the Secret holding the TSIG key
	// material.
	// +required
	TSIGSecretSecretRef corev1.SecretKeySelector `json:"tsigSecretSecretRef"`
}

// TenantGatewaySpec describes the desired state of a per-tenant Gateway.
type TenantGatewaySpec struct {
	// Apex is the tenant's apex hostname. The Gateway listeners are
	// constrained to this apex and its subdomains.
	// +required
	Apex string `json:"apex"`

	// CertMode selects between HTTP-01 per-listener Certificates and
	// a wildcard cert via DNS-01.
	// +kubebuilder:default=http01
	CertMode CertMode `json:"certMode,omitempty"`

	// IssuerName picks the Let's Encrypt environment the controller
	// configures the per-tenant Issuer with. Defaults to
	// letsencrypt-prod. Set to letsencrypt-stage on dev clusters to
	// avoid the LE production rate limits.
	// +kubebuilder:default=letsencrypt-prod
	IssuerName IssuerName `json:"issuerName,omitempty"`

	// DNS01 configures the DNS-01 solver when CertMode=dns01. Ignored
	// otherwise. Required (provider + matching config block) when
	// CertMode=dns01.
	// +optional
	DNS01 *DNS01Config `json:"dns01,omitempty"`

	// AttachedNamespaces lists namespace names that are allowed to
	// attach HTTPRoute or TLSRoute to this tenant's Gateway. The
	// publishing tenant namespace is implicit. Selector is by built-in
	// kubernetes.io/metadata.name (kube-apiserver-written, unspoofable).
	// +optional
	AttachedNamespaces []string `json:"attachedNamespaces,omitempty"`

	// TLSPassthroughServices names services exposed via TLS-passthrough
	// (mode: Passthrough listeners). Each service gets a dedicated
	// listener; HTTPRoutes attach to TLS-terminate listeners instead.
	// +optional
	TLSPassthroughServices []string `json:"tlsPassthroughServices,omitempty"`

	// GatewayClassName names the GatewayClass to attach the rendered
	// Gateway to. Default cilium.
	// +kubebuilder:default=cilium
	// +optional
	GatewayClassName string `json:"gatewayClassName,omitempty"`
}

// TenantGatewayListenerStatus reports the observed state of a single
// listener on the tenant's Gateway.
type TenantGatewayListenerStatus struct {
	// Name is the listener's name (e.g. "https-harbor", "https-apex").
	Name string `json:"name"`

	// Hostname is the hostname this listener serves.
	// +optional
	Hostname string `json:"hostname,omitempty"`

	// Ready indicates the cert is issued and the Gateway has accepted
	// the listener.
	Ready bool `json:"ready"`

	// CertificateName names the cert-manager Certificate backing this
	// listener.
	// +optional
	CertificateName string `json:"certificateName,omitempty"`

	// Reason is a short machine-readable reason when Ready=false.
	// +optional
	Reason string `json:"reason,omitempty"`
}

// TenantGatewayStatus reports the observed state of the tenant's Gateway.
type TenantGatewayStatus struct {
	// ObservedGeneration mirrors the .metadata.generation reflected in
	// the latest reconciled state.
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`

	// Conditions describes the current state of the TenantGateway.
	// Standard condition types: Ready, Programmed.
	// +optional
	// +patchMergeKey=type
	// +patchStrategy=merge
	// +listType=map
	// +listMapKey=type
	Conditions []metav1.Condition `json:"conditions,omitempty" patchStrategy:"merge" patchMergeKey:"type"`

	// Listeners reports per-listener readiness and cert state.
	// +optional
	// +listType=map
	// +listMapKey=name
	Listeners []TenantGatewayListenerStatus `json:"listeners,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Namespaced,shortName=tgw
// +kubebuilder:printcolumn:name="Apex",type="string",JSONPath=".spec.apex"
// +kubebuilder:printcolumn:name="Mode",type="string",JSONPath=".spec.certMode"
// +kubebuilder:printcolumn:name="Ready",type="string",JSONPath=`.status.conditions[?(@.type=="Ready")].status`
// +kubebuilder:printcolumn:name="Age",type="date",JSONPath=".metadata.creationTimestamp"

// TenantGateway is the Schema for the tenantgateways API.
// It expresses the operator-facing intent for a tenant's per-namespace
// Gateway; the cozystack-controller reconciles the actual Gateway and
// per-listener Certificate resources from this CR.
type TenantGateway struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   TenantGatewaySpec   `json:"spec,omitempty"`
	Status TenantGatewayStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// TenantGatewayList contains a list of TenantGateway.
type TenantGatewayList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []TenantGateway `json:"items"`
}

func init() {
	SchemeBuilder.Register(&TenantGateway{}, &TenantGatewayList{})
}
