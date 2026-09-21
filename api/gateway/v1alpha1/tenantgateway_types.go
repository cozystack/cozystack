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

// A comment block separated from its declaration by a blank line is
// detached on purpose. controller-gen turns an attached comment into
// the CRD description, which kubectl explain prints to whoever is
// filling the spec in, so rationale meant for whoever next edits the
// validation markers goes in a detached block: it stays beside the
// code without shipping to every cluster.

// CertMode selects how the per-tenant Gateway sources TLS certificates.
// +kubebuilder:validation:Enum=http01;dns01;existingSecret;edge
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

	// CertModeExistingSecret references a pre-existing wildcard TLS
	// Secret instead of issuing anything. No Issuer and no Certificate
	// are minted; the wildcard/apex listeners reference the operator-
	// supplied Secret named in Spec.WildcardSecretRef directly. Used
	// when the operator already holds a wildcard cert (purchased, or
	// issued by a corporate CA). The Secret must live in the
	// TenantGateway's own namespace.
	CertModeExistingSecret CertMode = "existingSecret"

	// CertModeEdge is for a GatewayClass whose provider terminates TLS
	// upstream of the Gateway (a Cloudflare Tunnel ends TLS at the
	// Cloudflare edge and reaches the Gateway over the tunnel). The
	// Gateway carries the apex, its wildcard and every inheriting child
	// apex as plain HTTP listeners; no Issuer, no Certificate and no
	// http->https redirect route are minted, and
	// Spec.TLSPassthroughServices is not rendered because a TLS
	// listener cannot be served by an HTTP-only edge.
	CertModeEdge CertMode = "edge"
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

// TLSPassthroughListener declares one layer-4 TLS-passthrough listener
// on the tenant Gateway. Unlike the layer-7 terminate listeners the
// controller renders for published HTTP apps, a passthrough listener
// does not terminate TLS: the ClientHello SNI selects the backend and
// the raw TLS stream is forwarded to the TLSRoute-named service, so a
// native database protocol that speaks its own TLS reaches clients
// without the Gateway ever holding the private key. Each listener owns
// a distinct native port and a per-engine SNI hostname.
type TLSPassthroughListener struct {
	// Name identifies the listener within the tenant Gateway. It
	// becomes the rendered Gateway listener name "tls-<name>" and the
	// sectionName a TLSRoute attaches to. Must be a DNS-1123 label
	// (lowercase alphanumerics and '-', starting and ending with an
	// alphanumeric) and unique across the list.
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=63
	// +kubebuilder:validation:Pattern=`^[a-z0-9]([-a-z0-9]*[a-z0-9])?$`
	// +required
	Name string `json:"name"`

	// Port is the TCP port this passthrough listener is published on,
	// on the tenant Gateway's shared address. Set it to the engine's
	// native port (e.g. 5432 for PostgreSQL). Must be 1..65535, unique
	// across the list, and neither 80 nor 443 — the Gateway's own http
	// (80) and TLS-terminate (443) listeners already own those ports.
	// It is not an access boundary: on the Cilium version this
	// platform pins, the backend answers this listener's SNI on every
	// port the Gateway exposes, so the hostname is what gates reach.
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:validation:Maximum=65535
	// +required
	Port int32 `json:"port"`

	// The pattern is Gateway API's own Hostname pattern, so a value this
	// field accepts is one the rendered listener accepts. Without it a
	// typo passes the apex suffix test, reaches etcd, and the render
	// aborts on it.

	// Hostname is the SNI the listener matches on the incoming
	// ClientHello. It routes the raw TLS stream by SNI, so give each
	// engine its own per-engine subdomain (e.g.
	// "postgres.foo.example.com") or a left-most-label wildcard (e.g.
	// "*.db.foo.example.com"). Must be an exact RFC 1123 hostname or a
	// wildcard hostname, and must fall within the tenant apex — equal to
	// Apex or a subdomain of it — since every listener on the tenant
	// Gateway is constrained to the apex.
	//
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=253
	// +kubebuilder:validation:Pattern=`^(\*\.)?[a-z0-9]([-a-z0-9]*[a-z0-9])?(\.[a-z0-9]([-a-z0-9]*[a-z0-9])?)*$`
	// +required
	Hostname string `json:"hostname"`
}

// The rules below are enforced at admission because renderGateway is
// the first thing that renders: a spec the apiserver accepts and the
// renderer refuses aborts the Issuer, the certificates and every route
// status behind it. The controller keeps its own copy of them, because
// it and the CRD roll out separately and it cannot assume the object it
// reads was validated by the version it ships with.
//
// Which rules are left to the controller, and why each one is, is in
// packages/extra/gateway/README.md. One trap belongs here: hostname
// overlap needs wildcard-aware matching across two lists, and its
// estimated CEL cost pushed the CRD past the apiserver's install-time
// budget while every per-request test stayed green.
//
// The budget rests on three bounds, and they do not weigh the same,
// measured by removing each in turn and re-running the estimator:
// without MaxLength on apex the hostname-containment rule runs 3.9x
// over budget, without MaxItems on tlsPassthroughServices the
// name-collision rule runs 6.5x over, and without MaxItems on
// tlsPassthroughListeners the distinct-port rule alone runs over 100x
// over and takes the whole-schema cost total with it. So the list
// counts are the bounds to defend, not the string length.
// TestCRDPassesInstallTimeValidation is what catches that.

// TenantGatewaySpec describes the desired state of a per-tenant Gateway.
//
// The rules on tlsPassthroughListeners are enforced both at admission
// and in the controller; a few have no admission form and surface as
// Ready=False instead. The reasoning is in
// packages/extra/gateway/README.md.
// +kubebuilder:validation:XValidation:rule="!has(self.tlsPassthroughListeners) || self.tlsPassthroughListeners.all(l, l.port != 80 && l.port != 443)",message="tlsPassthroughListeners: ports 80 and 443 are reserved for the Gateway's own http and TLS-terminate listeners; use the engine's native port"
// +kubebuilder:validation:XValidation:rule="!has(self.tlsPassthroughListeners) || self.tlsPassthroughListeners.all(l, self.tlsPassthroughListeners.filter(o, o.port == l.port).size() == 1)",message="tlsPassthroughListeners: each listener must occupy a distinct port"
// +kubebuilder:validation:XValidation:rule="!has(self.tlsPassthroughListeners) || self.tlsPassthroughListeners.all(l, l.hostname == self.apex || l.hostname.endsWith('.' + self.apex))",message="tlsPassthroughListeners: hostname must equal the tenant apex or be a subdomain of it"
// +kubebuilder:validation:XValidation:rule="!has(self.tlsPassthroughListeners) || !has(self.tlsPassthroughServices) || self.tlsPassthroughListeners.all(l, !(l.name in self.tlsPassthroughServices))",message="tlsPassthroughListeners: name collides with a tlsPassthroughServices entry; both render a tls-<name> Gateway listener"
// +kubebuilder:validation:XValidation:rule="!has(self.tlsPassthroughListeners) || size(self.tlsPassthroughListeners) == 0 || !has(self.certMode) || self.certMode == 'http01'",message="tlsPassthroughListeners: supported with certMode http01 only; dns01 and existingSecret serve the tenant from one wildcard terminate listener that the pinned Cilium cannot keep apart from a passthrough listener under the same apex, and edge terminates TLS upstream and renders no TLS listener to sit beside"
type TenantGatewaySpec struct {
	// MaxLength is the DNS ceiling, so it rejects nothing resolvable,
	// and it is not cosmetic: it is one of the three bounds the
	// install-time CEL cost budget rests on, and not the heavy one —
	// the measured weights are in the comment above the type. Why the
	// field carries no format pattern is in
	// packages/extra/gateway/README.md.

	// Apex is the tenant's apex hostname. The Gateway listeners are
	// constrained to this apex and its subdomains, and the
	// controller-owned http-to-https redirect route declares this apex
	// and its wildcard as its hostnames, so an empty value renders a
	// route the apiserver rejects on its hostname schema.
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=253
	// +required
	Apex string `json:"apex"`

	// CertMode selects between HTTP-01 per-listener Certificates, a
	// wildcard cert via DNS-01, an operator-supplied wildcard Secret,
	// and edge (no certificates: TLS ends at the class provider's edge).
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

	// WildcardSecretRef names a pre-existing TLS Secret in the
	// TenantGateway's own namespace used for the wildcard and apex
	// listeners when CertMode=existingSecret. Required in that mode;
	// ignored otherwise. The Secret must be of type kubernetes.io/tls
	// and cover the tenant apex (and *.apex). Cross-namespace refs are
	// intentionally unsupported to avoid requiring a ReferenceGrant.
	// +optional
	WildcardSecretRef *corev1.LocalObjectReference `json:"wildcardSecretRef,omitempty"`

	// AttachedNamespaces lists namespace names that are allowed to
	// attach HTTPRoute or TLSRoute to this tenant's Gateway. The
	// publishing tenant namespace is implicit. The controller grants
	// access by stamping the namespace.cozystack.io/gateway label on
	// each listed namespace. Writing it requires patch on Namespace,
	// which tenants do not hold. The label reaches the HTTPS-terminate
	// listeners and the port-443 passthrough listeners; the port-80
	// listener and the native-port listeners from
	// tlsPassthroughListeners select on kubernetes.io/metadata.name
	// instead, so listing a namespace here does not let it attach to a
	// database port.
	// +optional
	AttachedNamespaces []string `json:"attachedNamespaces,omitempty"`

	// The bounds are what the CEL cost estimator needs, as on Apex: the
	// name-collision rule scans this list, and an unbounded list of
	// unbounded strings puts the estimate over the per-CRD budget. The
	// count is the Gateway API listener cap less the mandatory port-80
	// listener and less one published hostname, so a tenant filling the
	// list can still publish an app;
	// TestPassthroughServiceCapFitsGatewayAPI derives that sum from the
	// schema, so moving the number fails there rather than in a
	// cluster. Why a repeated entry is left to the controller instead
	// of a listType=set marker is in packages/extra/gateway/README.md.
	//
	// The item length is the 253 characters Gateway API allows a
	// SectionName less the four of the tls- prefix, which is as far as
	// the schema reaches on its own: the hostname also depends on the
	// apex, so the composed value is checked by the controller and
	// reported on status. The item pattern is a DNS-1123 subdomain
	// rather than a single label because SectionName takes
	// dot-separated labels: "s3.storage" renders tls-s3.storage and a
	// hostname that works, while the label form would refuse it and
	// catch nothing the subdomain form misses.

	// TLSPassthroughServices names services exposed via TLS-passthrough
	// (mode: Passthrough listeners). Each service gets a dedicated
	// listener; HTTPRoutes attach to TLS-terminate listeners instead.
	// Not rendered when CertMode=edge.
	//
	// An entry becomes both the listener name tls-<svc> and the listener
	// hostname <svc>.<apex>, and Gateway API bounds each at 253
	// characters. An entry that overflows either one renders a listener
	// the apiserver refuses, and that refusal takes the whole Gateway
	// with it rather than the one listener.
	// +kubebuilder:validation:MaxItems=62
	// +kubebuilder:validation:items:MaxLength=249
	// +kubebuilder:validation:items:Pattern=`^[a-z0-9]([-a-z0-9]*[a-z0-9])?(\.[a-z0-9]([-a-z0-9]*[a-z0-9])?)*$`
	// +optional
	TLSPassthroughServices []string `json:"tlsPassthroughServices,omitempty"`

	// The cap bounds what THIS field contributes to the Gateway's 64
	// listener slots; it does not by itself guarantee the total fits.
	// The rendered count is the port-80 listener plus one per published
	// hostname, one per tlsPassthroughServices entry, and one per entry
	// here — so a tenant can exceed 64 with far fewer than 62 of these.
	// The controller checks the assembled total and fails with a named
	// budget; this cap only keeps a single field from consuming the
	// whole allowance. It also bounds the cost estimate for the CEL
	// rules above.
	//
	// The number itself is 64 less the port-80 listener and less one
	// published hostname, so a tenant filling this field to the brim can
	// still publish one app. TestPassthroughListenerCapFitsGatewayAPI
	// renders that sum reading maxItems from the schema rather than from
	// a literal, so moving this number fails there and not in a cluster.

	// TLSPassthroughListeners declares layer-4 TLS-passthrough
	// listeners on the tenant Gateway. The controller renders one
	// "tls-<name>" listener per entry — on the entry's native Port,
	// mode Passthrough, matching the entry's per-engine SNI Hostname —
	// alongside the layer-7 terminate listeners. This lets a database
	// engine present its own certificate directly to clients while
	// sharing the tenant Gateway's address. It is independent of
	// TLSPassthroughServices, the passthrough-on-443 form; the
	// two do not replace each other.
	//
	// The listener is all this field creates. Nothing is routed until a
	// TLSRoute attaches to it by sectionName "tls-<name>" and names a
	// backend Service, and the backend must present its own certificate
	// for the listener's Hostname — a passthrough listener never
	// terminates TLS, so the Gateway neither holds nor issues that
	// certificate. Until such a route attaches and its backend
	// resolves, the entry publishes the port and answers nothing on it:
	// a passthrough listener carrying no forwarding route contributes
	// no filter chain, so a hostname an HTTPS-terminate listener
	// already serves goes on being served there.
	// +optional
	// +listType=map
	// +listMapKey=name
	// +kubebuilder:validation:MaxItems=62
	TLSPassthroughListeners []TLSPassthroughListener `json:"tlsPassthroughListeners,omitempty"`

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
