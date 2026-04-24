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
  - secretName (required) - TLS secret name to create (can be same as name)
  - dnsNames   (required) - list of DNS SANs
  - issuerRef  (required) - dict with "name" and "kind" (e.g. ClusterIssuer, Issuer)
  - duration   (optional) - certificate duration, defaults to 8760h (1 year)
  - renewBefore(optional) - renewal window, defaults to 720h (30 days)
  - usages     (optional) - list of key usages, defaults to ["server auth"]
*/}}
{{- define "cozy-lib.tls.certificate" -}}
apiVersion: cert-manager.io/v1
kind: Certificate
metadata:
  name: {{ .name }}
  labels:
    app.kubernetes.io/instance: {{ .Release.Name }}
    app.kubernetes.io/managed-by: {{ .Release.Service }}
spec:
  secretName: {{ .secretName }}
  duration: {{ .duration | default "8760h" }}
  renewBefore: {{ .renewBefore | default "720h" }}
  privateKey:
    algorithm: RSA
    size: 2048
  usages:
    {{- if .usages }}
    {{- range .usages }}
    - {{ . }}
    {{- end }}
    {{- else }}
    - server auth
    {{- end }}
  dnsNames:
    {{- range .dnsNames }}
    - {{ . }}
    {{- end }}
  issuerRef:
    name: {{ .issuerRef.name }}
    kind: {{ .issuerRef.kind }}
    group: cert-manager.io
{{- end }}

{{/*
Return a TLS secret name — either the user-provided value or a generated default.
Useful in conditionals to decide whether to mount a TLS secret or render a Certificate CR.

Usage:
  {{- $tlsSecret := include "cozy-lib.tls.secretName" (dict "Release" .Release "suffix" "tls" "secretName" .Values.tls.secretName) }}

Parameters:
  - Release    (required) - Helm release object
  - suffix     (required) - suffix for generated name (e.g. "tls", "server-tls")
  - secretName (optional) - user-provided secret name; if set, returned as-is
*/}}
{{- define "cozy-lib.tls.secretName" -}}
{{- if .secretName -}}
{{-   .secretName -}}
{{- else -}}
{{-   printf "%s-%s" .Release.Name .suffix -}}
{{- end -}}
{{- end }}
