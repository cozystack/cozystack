{{/*
TLS certificate helpers for cert-manager integration.
These helpers render cert-manager Certificate CRs for services that need TLS.
*/}}

{{/*
Render a cert-manager Certificate CR.

Usage:
  {{- include "cozy-lib.tls.certificate" (dict
    "Release" .Release
    "name" "my-redis-tls"
    "secretName" "my-redis-tls-secret"
    "dnsNames" (list "redis-svc" "redis-svc.ns.svc")
    "issuerRef" (dict "name" "selfsigned-cluster-issuer" "kind" "ClusterIssuer")
  ) }}

Parameters:
  - Release    (required) - Helm release object, used for labels
  - name       (required) - Certificate CR metadata.name
  - secretName (required) - TLS secret name to create (can be same as name). A caller-level wrapper can default this to name.
  - dnsNames   (required) - list of DNS SANs
  - extraDnsNames (optional) - additional DNS SANs to append (e.g. user-supplied custom domains)
  - issuerRef  (required) - dict with "name" and "kind" (e.g. ClusterIssuer, Issuer). The "group" field is always hardcoded to "cert-manager.io".
  - duration   (optional) - certificate duration, defaults to 8760h (1 year); empty strings are treated as unset. Non-empty values are passed through without validation.
  - renewBefore (optional) - renewal window, defaults to 720h (30 days)
  - usages     (optional) - list of key usages, defaults to ["server auth"] (not cert-manager's default of "digital signature, key encipherment"; this is a deliberate choice for TLS server certificates). An empty list also falls back to the default.
  Private key is hardcoded to RSA-2048 with rotationPolicy: Always; these are not configurable via this helper.
  extraDnsNames: nil is treated as an empty list (zero extra names appended).
*/}}
{{- define "cozy-lib.tls.certificate" -}}
{{- if not .Release -}}
{{-   fail "ERROR: \"Release\" is required for cozy-lib.tls.certificate. Pass the Helm release object via the context dict." -}}
{{- end -}}
{{- if not .name -}}
{{-   fail "ERROR: \"name\" is required for cozy-lib.tls.certificate. It sets the Certificate CR metadata.name." -}}
{{- end -}}
{{- if not .secretName -}}
{{-   fail "ERROR: \"secretName\" is required for cozy-lib.tls.certificate. Specify the TLS secret name to create." -}}
{{- end -}}
{{- if not .dnsNames -}}
{{-   fail "ERROR: \"dnsNames\" is required for cozy-lib.tls.certificate. Provide at least one DNS name as a list." -}}
{{- end -}}
{{- if not (kindIs "slice" .dnsNames) -}}
{{-   fail "ERROR: \"dnsNames\" must be a list for cozy-lib.tls.certificate. Got a string — wrap it in a list." -}}
{{- end -}}
{{- if and .extraDnsNames (not (kindIs "slice" .extraDnsNames)) -}}
{{-   fail "ERROR: \"extraDnsNames\" must be a list for cozy-lib.tls.certificate. Got a string — wrap it in a list." -}}
{{- end -}}
{{- if not .issuerRef -}}
{{-   fail "ERROR: \"issuerRef\" is required for cozy-lib.tls.certificate. Provide a dict with \"name\" and \"kind\"." -}}
{{- end -}}
{{- if not .issuerRef.name -}}
{{-   fail "ERROR: \"issuerRef.name\" is required for cozy-lib.tls.certificate. Specify the ClusterIssuer or Issuer name." -}}
{{- end -}}
{{- if not .issuerRef.kind -}}
{{-   fail "ERROR: \"issuerRef.kind\" is required for cozy-lib.tls.certificate. Specify \"ClusterIssuer\" or \"Issuer\"." -}}
{{- end -}}
{{- if not (or (eq .issuerRef.kind "Issuer") (eq .issuerRef.kind "ClusterIssuer")) -}}
{{-   fail "ERROR: issuerRef.kind must be \"Issuer\" or \"ClusterIssuer\"" -}}
{{- end -}}
apiVersion: cert-manager.io/v1
kind: Certificate
metadata:
  name: {{ .name | quote }}
  namespace: {{ .Release.Namespace | quote }}
  labels:
    app.kubernetes.io/instance: {{ .Release.Name | quote }}
    app.kubernetes.io/managed-by: {{ .Release.Service | quote }}
spec:
  secretName: {{ .secretName | quote }}
  duration: {{ ternary "8760h" .duration (empty .duration) | quote }}
  renewBefore: {{ ternary "720h" .renewBefore (empty .renewBefore) | quote }}
  privateKey:
    algorithm: RSA
    size: 2048
    rotationPolicy: Always
  usages:
    {{- if .usages }}
    {{- range .usages }}
    - {{ . | quote }}
    {{- end }}
    {{- else }}
    - "server auth"
    {{- end }}
  dnsNames:
    {{- range .dnsNames }}
    - {{ . | quote }}
    {{- end }}
    {{- range .extraDnsNames }}
    - {{ . | quote }}
    {{- end }}
  issuerRef:
    name: {{ .issuerRef.name | quote }}
    kind: {{ .issuerRef.kind | quote }}
    group: {{ "cert-manager.io" | quote }}
{{- end }}

{{/*
Convenience helper for chart consumers.
Return a TLS secret name — either the user-provided value or a generated default.
Useful in conditionals to decide whether to mount a TLS secret or render a Certificate CR.

Usage:
  {{- $tlsSecret := include "cozy-lib.tls.secretName" (dict "Release" .Release "suffix" "tls" "secretName" .Values.tls.secretName) }}

Parameters:
  - Release    (required) - Helm release object
  - suffix     (required when secretName is not provided) - suffix for generated name (e.g. "tls", "server-tls")
  - secretName (optional) - user-provided secret name; if set, returned as-is
*/}}
{{- define "cozy-lib.tls.secretName" -}}
{{- if not .Release -}}
{{-   fail "ERROR: \"Release\" is required for cozy-lib.tls.secretName. Pass the Helm release object via the context dict." -}}
{{- end -}}
{{- if not (empty .secretName) -}}
{{-   .secretName -}}
{{- else -}}
{{-   if not .suffix -}}
{{-     fail "ERROR: \"suffix\" is required for cozy-lib.tls.secretName when secretName is not provided. Provide a suffix for the generated secret name (e.g. \"tls\", \"server-tls\")." -}}
{{-   end -}}
{{-   printf "%s-%s" .Release.Name .suffix -}}
{{- end -}}
{{- end }}
