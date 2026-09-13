{{/*
monitoring.isRoot: "true" for the platform's own Monitoring (tenant-root, or
cozy-monitoring on older layouts), "" for a tenant's. Mirrors the helper of
the same name in the system/monitoring chart this application renders: the
root instance keeps the HA shape, a tenant instance defaults to one replica
of everything, and the WorkloadMonitors and dashboard grants here must say
what that chart renders.
*/}}
{{- define "monitoring.isRoot" -}}
{{- if or (eq .Release.Namespace "tenant-root") (eq .Release.Namespace "cozy-monitoring") -}}true{{- end -}}
{{- end -}}

{{/*
monitoring.replicas: (list $ value rootDefault tenantDefault). An explicit
value wins; otherwise the tier default.
*/}}
{{- define "monitoring.replicas" -}}
{{- $root := index . 0 -}}
{{- $value := index . 1 -}}
{{- if and (not (kindIs "invalid" $value)) (ne (toString $value) "") -}}
{{- int $value -}}
{{- else if include "monitoring.isRoot" $root -}}
{{- index . 2 -}}
{{- else -}}
{{- index . 3 -}}
{{- end -}}
{{- end -}}

{{/*
monitoring.alertaEnabled: "true" when Alerta is rendered. Explicit
alerta.enabled wins; otherwise on for the root instance, off for a tenant.
*/}}
{{- define "monitoring.alertaEnabled" -}}
{{- $v := dig "enabled" nil (.Values.alerta | default dict) -}}
{{- if kindIs "invalid" $v -}}
{{- include "monitoring.isRoot" . -}}
{{- else if $v -}}true{{- end -}}
{{- end -}}
