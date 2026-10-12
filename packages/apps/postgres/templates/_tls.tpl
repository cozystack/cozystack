{{/*
postgres.tls.enabled resolves the tri-state tls.enabled field to a plain
string "true" or "false" so callers can use it with `eq`:

  {{- $tlsEnabled := (include "postgres.tls.enabled" .) | eq "true" -}}

Tri-state semantics:
  - tls.enabled explicitly set  → use that value
  - tls.enabled unset (null)    → auto-on when external is true
*/}}
{{- define "postgres.tls.enabled" -}}
{{- if kindIs "invalid" .Values.tls.enabled -}}
  {{- .Values.external | default false | toString -}}
{{- else -}}
  {{- .Values.tls.enabled | toString -}}
{{- end -}}
{{- end -}}

{{/*
postgres.tls.required resolves the tri-state tls.required field to "true" or
"false". Unset follows external, so an externally published instance refuses
plaintext sessions by default. Set explicitly, it wins in either direction.
*/}}
{{- define "postgres.tls.required" -}}
{{- if kindIs "invalid" .Values.tls.required -}}
  {{- .Values.external | default false | toString -}}
{{- else -}}
  {{- .Values.tls.required | toString -}}
{{- end -}}
{{- end -}}
