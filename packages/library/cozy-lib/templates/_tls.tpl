{{/*
=============================================================================
 TLS trust-anchor helpers
=============================================================================

Per-app TLS in the Cozystack catalog issues a per-release, self-signed CA (via
cert-manager or the app operator). A tenant that connects to such an endpoint
needs the CA certificate (ca.crt) to verify the server — but nothing more. The
objects that hold ca.crt today also hold private keys:

  - the cert-manager CA secret  <release>-ca   carries the CA private key
    (tls.key) — full trust-chain compromise if leaked;
  - the cert-manager leaf secret <release>-tls  carries the server private key
    (tls.key) — server impersonation / MITM if leaked.

So any RBAC path that hands a tenant ca.crt by granting read on one of those
Secrets also hands over a private key. cozy-lib.tls.caCertSecret breaks that
coupling: it renders a canonical trust-anchor object that carries ONLY ca.crt,
labelled so tenants reach it through the core.cozystack.io/tenantsecrets API
that the base tenant roles already grant — never through a Secret that contains
tls.key.

Delivery mechanism (grounded in the platform RBAC, not invented here):

  - pkg/registry/core/tenantsecret/rest.go surfaces, under the virtual resource
    core.cozystack.io/tenantsecrets, exactly the namespace Secrets labelled
    internal.cozystack.io/tenantresource=true (TenantResourceLabelKey /
    TenantResourceLabelValue in pkg/apis/core/v1alpha1/tenantresource_types.go).
  - packages/system/cozystack-basics/templates/clusterroles.yaml grants
    get/list/watch on core.cozystack.io/tenantsecrets to tenant ServiceAccounts
    (cozy:tenant:base) and to use/admin/super-admin subjects (cozy:tenant:use:
    base). No grant on raw core/v1 secrets is involved, so attaching the label
    to a key-free object exposes the trust anchor without exposing any key.
  - internal/backupcontroller/credentials_projector.go relies on the same rule
    in reverse: it deliberately OMITS the label so its key-bearing projection
    is NOT promoted to a TenantSecret. This helper is the positive counterpart —
    a key-FREE object that is safe to promote.

This is the chart-side shape every per-app TLS PR converges on; population of
ca.crt stays the responsibility of whatever owns the PKI (the app operator, as
the redis-operator fork does with its CA-only Opaque Secret, or a cert-manager
chain resolved at the chart level). The helper itself is pure and value-driven
so it renders deterministically and refuses, by construction and by guard, to
carry a private key.
*/}}

{{/*
cozy-lib.tls.caCertSecret renders the canonical CA-only trust-anchor Secret.

Invoked with a single dict argument (named parameters, not the (arg, $) list
form, because it needs no global scope):

  {{ include "cozy-lib.tls.caCertSecret" (dict
       "name"      (printf "%s-ca-cert" .Release.Name)
       "namespace" .Release.Namespace
       "caCert"    $caCertPem
       "labels"    (dict "app.kubernetes.io/instance" .Release.Name)
  ) }}

Parameters:
  - name      (required) Secret name. Convention: "<release>-ca-cert".
  - caCert    (required) the CA certificate chain in PEM. Must contain a
              BEGIN CERTIFICATE PEM header and must NOT contain a
              BEGIN...PRIVATE KEY PEM header; the helper fails closed on either
              violation. Both checks are anchored to the PEM header line and are
              case-insensitive, so they neither false-positive on certificate
              body/comment text nor miss a lowercased hand-pasted header.
  - namespace (optional) metadata.namespace.
  - labels    (optional) extra labels merged onto the mandatory
              internal.cozystack.io/tenantresource label (which always wins).
  - annotations (optional) extra annotations.
*/}}
{{- define "cozy-lib.tls.caCertSecret" -}}
{{-   if not (kindIs "map" .) -}}
{{-     fail "cozy-lib.tls.caCertSecret: expected a single dict argument" -}}
{{-   end -}}
{{-   $name := default "" .name -}}
{{-   if eq $name "" -}}
{{-     fail "cozy-lib.tls.caCertSecret: name is required" -}}
{{-   end -}}
{{-   $caCert := default "" .caCert -}}
{{-   if eq (trim $caCert) "" -}}
{{-     fail "cozy-lib.tls.caCertSecret: caCert is required and must be a non-empty PEM" -}}
{{-   end -}}
{{- /* Anchor both guards to the PEM header form, case-insensitively. A free
       substring match would false-positive on a legitimate certificate whose
       subject/comment text happens to contain "PRIVATE KEY", and would miss a
       lowercase hand-pasted header. The (?i) header regex catches every key
       variant (PKCS#8, RSA, EC, ENCRYPTED, OPENSSH, DSA) and only a real
       BEGIN...PRIVATE KEY line, not arbitrary blob text. */ -}}
{{-   if regexMatch "(?i)-----BEGIN [A-Z0-9 ]*PRIVATE KEY-----" $caCert -}}
{{-     fail "cozy-lib.tls.caCertSecret: caCert must not contain private key material" -}}
{{-   end -}}
{{-   if not (regexMatch "(?i)-----BEGIN CERTIFICATE-----" $caCert) -}}
{{-     fail "cozy-lib.tls.caCertSecret: caCert must contain a PEM certificate (BEGIN CERTIFICATE)" -}}
{{-   end -}}
{{-   $labels := merge (dict "internal.cozystack.io/tenantresource" "true") (default (dict) .labels) -}}
apiVersion: v1
kind: Secret
metadata:
  name: {{ $name }}
{{-   with .namespace }}
  namespace: {{ . }}
{{-   end }}
  labels: {{- toYaml $labels | nindent 4 }}
{{-   with .annotations }}
  annotations: {{- toYaml . | nindent 4 }}
{{-   end }}
type: Opaque
stringData:
  ca.crt: {{ $caCert | quote }}
{{- end -}}
