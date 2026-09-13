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

{{/*
monitoring.metricsStorages: the metrics storages the system chart renders for
this instance, as a YAML list; mirrors the helper of the same name there. An
entry's `enabled` wins; unset, the root instance renders every entry and a
tenant instance only the first one.
*/}}
{{- define "monitoring.metricsStorages" -}}
{{- $out := list -}}
{{- $root := include "monitoring.isRoot" . -}}
{{- range $i, $s := .Values.metricsStorages -}}
{{-   $enabled := dig "enabled" nil $s -}}
{{-   if kindIs "invalid" $enabled -}}
{{-     $enabled = or (eq $root "true") (eq $i 0) -}}
{{-   end -}}
{{-   if $enabled -}}
{{-     $out = append $out $s -}}
{{-   end -}}
{{- end -}}
{{- toYaml $out -}}
{{- end -}}
