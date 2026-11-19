{{- /*
  Same condition as monitoring.tracingCentral in packages/system/monitoring,
  which renders the collector this chart accounts for; the two must agree.
*/ -}}
{{- define "monitoring.tracingCentral" -}}
{{- if and (ne .Release.Namespace "tenant-root") (eq (toString (dig "tracingCentral" false (.Values._namespace | default dict))) "true") -}}true{{- end -}}
{{- end -}}

{{- /* Same condition as monitoring.tracingCentralStore in packages/system/monitoring,
       which renders the vmauth this chart accounts for. */}}
{{- define "monitoring.tracingCentralStore" -}}
{{- if and (eq .Release.Namespace "tenant-root") (eq (toString .Values.tracingCentralHost) "true") -}}
{{-   range .Values.tracingStorages -}}
{{-     if and (eq .name "generic") (eq (.mode | default "cluster") "cluster") }}true{{ end -}}
{{-   end -}}
{{- end -}}
{{- end -}}
