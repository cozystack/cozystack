{{/*
redis.tls.secretName — wraps cozy-lib.tls.secretName with redis-specific suffix.
Returns either the user-provided tls.secretName or "<release>-redis-tls".
*/}}
{{- define "redis.tls.secretName" -}}
{{- include "cozy-lib.tls.secretName" (dict
    "Release"    .Release
    "suffix"     "redis-tls"
    "secretName" .Values.tls.secretName
) -}}
{{- end -}}

{{/*
redis.tls.dnsNames — DNS SANs for the Redis TLS certificate.
Covers the chart's TLS-only ClusterIP services (<release>-master for writes,
<release>-replicas for reads) and the external LoadBalancer service across
short, namespaced, and FQDN forms. The operator-managed rfs-* service is
intentionally excluded because it serves plaintext on 6379 only.
The external-lb variants are always included so toggling .Values.external
later does not require certificate reissue.
*/}}
{{- define "redis.tls.dnsNames" -}}
- {{ .Release.Name }}-master
- {{ .Release.Name }}-master.{{ .Release.Namespace }}
- {{ .Release.Name }}-master.{{ .Release.Namespace }}.svc
- {{ .Release.Name }}-master.{{ .Release.Namespace }}.svc.cluster.local
- {{ .Release.Name }}-replicas
- {{ .Release.Name }}-replicas.{{ .Release.Namespace }}
- {{ .Release.Name }}-replicas.{{ .Release.Namespace }}.svc
- {{ .Release.Name }}-replicas.{{ .Release.Namespace }}.svc.cluster.local
- {{ .Release.Name }}-external-lb
- {{ .Release.Name }}-external-lb.{{ .Release.Namespace }}
- {{ .Release.Name }}-external-lb.{{ .Release.Namespace }}.svc
- {{ .Release.Name }}-external-lb.{{ .Release.Namespace }}.svc.cluster.local
{{- end -}}
