{{- /*
  kafka.tls.enabled — tri-state TLS decision for the external listener (port 9094).

  Resolution order:
    1. If tls.enabled is explicitly set (true or false), that value wins.
    2. If tls.enabled is absent (nil / not provided), TLS inherits from .Values.external.

  Null detection uses `kindIs "invalid"` which is the canonical Helm pattern for
  distinguishing an absent/nil value from an explicit false.

  Note: this helper only returns the TLS flag value. Whether the external listener
  is rendered at all is controlled by .Values.external in the caller.
*/ -}}
{{- define "kafka.tls.enabled" -}}
{{- if kindIs "invalid" .Values.tls.enabled -}}
  {{- .Values.external | default false -}}
{{- else -}}
  {{- .Values.tls.enabled -}}
{{- end -}}
{{- end -}}
