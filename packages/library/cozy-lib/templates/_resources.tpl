{{- define "cozy-lib.resources.defaultCpuAllocationRatio" }}
{{-   `10` }}
{{- end }}

{{- define "cozy-lib.resources.cpuAllocationRatio" }}
{{-   $cozyConfig := lookup "v1" "ConfigMap" "cozy-system" "cozystack" }}
{{-   if not $cozyConfig }}
{{-     include "cozy-lib.resources.defaultCpuAllocationRatio" . }}
{{-   else }}
{{-     dig "data" "cpu-allocation-ratio" (include "cozy-lib.resources.defaultCpuAllocationRatio" dict) $cozyConfig }}
{{-   end }}
{{- end }}

{{- define "cozy-lib.resources.toFloat" -}}
    {{- $value := . -}}
    {{- $unit := 1.0 -}}
    {{- if typeIs "string" . -}}
        {{- $base2 := dict "Ki" 0x1p10 "Mi" 0x1p20 "Gi" 0x1p30 "Ti" 0x1p40 "Pi" 0x1p50 "Ei" 0x1p60 -}}
        {{- $base10 := dict "m" 1e-3 "k" 1e3 "M" 1e6 "G" 1e9 "T" 1e12 "P" 1e15 "E" 1e18 -}}
        {{- range $k, $v := merge $base2 $base10 -}}
            {{- if hasSuffix $k $ -}}
                {{- $value = trimSuffix $k $ -}}
                {{- $unit = $v -}}
            {{- end -}}
        {{- end -}}
    {{- end -}}
    {{- mulf (float64 $value) $unit -}}
{{- end -}}

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
{{-   $cpuAllocationRatio := include "cozy-lib.resources.cpuAllocationRatio" dict | float64 }}
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
{{-     if not (eq $k "cpu") }}
{{-       $_ := set $output.requests $k $v }}
{{-       $_ := set $output.limits $k $v }}
{{-     else }}
{{-       $vcpuRequestF64 := (include "cozy-lib.resources.toFloat" $v) | float64 }}
{{-       $cpuRequestF64 := divf $vcpuRequestF64 $cpuAllocationRatio }}
{{-       $_ := set $output.requests $k ($cpuRequestF64 | toString) }}
{{-     end }}
{{-   end }}
{{-   $output | toYaml }}
{{- end  }}
