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
Covers the external LoadBalancer service and the operator-managed master
service (rfs-<release>) across short, namespaced, and FQDN forms.
*/}}
{{- define "redis.tls.dnsNames" -}}
- {{ .Release.Name }}-external-lb
- {{ .Release.Name }}-external-lb.{{ .Release.Namespace }}
- {{ .Release.Name }}-external-lb.{{ .Release.Namespace }}.svc
- {{ .Release.Name }}-external-lb.{{ .Release.Namespace }}.svc.cluster.local
- rfs-{{ .Release.Name }}
- rfs-{{ .Release.Name }}.{{ .Release.Namespace }}
- rfs-{{ .Release.Name }}.{{ .Release.Namespace }}.svc
- rfs-{{ .Release.Name }}.{{ .Release.Namespace }}.svc.cluster.local
{{- end -}}
