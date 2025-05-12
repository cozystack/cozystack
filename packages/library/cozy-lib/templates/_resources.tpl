{{- /*
  A sanitized resource map is a dict with resource-name => resource-quantity.
  If not in such a form, requests are used, then limits. All resources are set
  to have equal requests and limits, except CPU, that has only requests. The
  template expects to receive a dict {"requests":{...}, "limits":{...}} as
  input, e.g. {{ include "cozy-lib.resources.sanitize" .Values.resources }}.
  Example input:
  ==============
  limits:
    cpu: 100m
    memory: 1024Mi
  requests:
    cpu: 200m
    memory: 512Mi
  memory: 256Mi
  devices.com/nvidia: "1"

  Example output:
  ===============
  limits:
    devices.com/nvidia: "1"
    memory: 256Mi
  requests:
    cpu: 200m
    devices.com/nvidia: "1"
    memory: 256Mi
*/}}
{{- define "cozy-lib.resources.sanitize" }}
{{-   $sanitizedMap := dict }}
{{-   if hasKey . "limits" }}
{{-     range $k, $v := .limits }}
{{-       $_ := set $sanitizedMap $k $v }}
{{-     end }}
{{-   end }}
{{-   if hasKey . "requests" }}
{{-     range $k, $v := .requests }}
{{-       $_ := set $sanitizedMap $k $v }}
{{-     end }}
{{-   end }}
{{-   range $k, $v := . }}
{{-     if not (or (eq $k "requests") (eq $k "limits")) }}
{{-       $_ := set $sanitizedMap $k $v }}
{{-     end }}
{{-   end }}
{{-   $output := dict "requests" dict "limits" dict }}
{{-   range $k, $v := $sanitizedMap }}
{{-     $_ := set $output.requests $k $v }}
{{-     if not (eq $k "cpu") }}
{{-       $_ := set $output.limits $k $v }}
{{-     end }}
{{-   end }}
{{-   $output | toYaml }}
{{- end  }}
