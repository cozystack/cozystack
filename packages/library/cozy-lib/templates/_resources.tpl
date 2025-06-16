{{- define "cozy-lib.resources.defaultCpuAllocationRatio" }}
{{-   `10` }}
{{- end }}

{{- define "cozy-lib.resources.cpuAllocationRatio" }}
{{-   include "cozy-lib.loadCozyConfig" . }}
{{-   $cozyConfig := index . 1 "cozyConfig" }}
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
    {{- mulf (float64 $value) $unit | toString -}}
{{- end -}}

{{- /*
  A sanitized resource map is a dict with resource-name => resource-quantity.
  If not in such a form, requests are used, then limits. All resources are set
  to have equal requests and limits, except CPU, where the limit is increased
  by a factor of the CPU allocation ratio. The template expects to receive a
  dict {"requests":{...}, "limits":{...}} as input, e.g.
  {{ include "cozy-lib.resources.sanitize" .Values.resources }}.
  Example input:
  ==============
  limits:
    cpu: "1"
    memory: 1024Mi
  requests:
    cpu: "2"
    memory: 512Mi
  memory: 256Mi
  devices.com/nvidia: "1"

  Example output:
  ===============
  limits:
    devices.com/nvidia: "1" # only present in top level key
    memory: 256Mi # value from top level key has priority over all others
    cpu: "2" # value from .requests.cpu has priority over .limits.cpu
  requests:
    cpu: 200m # .limits.cpu divided by CPU allocation ratio
    devices.com/nvidia: "1" # .requests == .limits
    memory: 256Mi # .requests == .limits
*/}}
{{- define "cozy-lib.resources.sanitize" }}
{{-   $cpuAllocationRatio := include "cozy-lib.resources.cpuAllocationRatio" . | float64 }}
{{-   $sanitizedMap := dict }}
{{-   $args := index . 0 }}
{{-   if hasKey $args "limits" }}
{{-     range $k, $v := $args.limits }}
{{-       $_ := set $sanitizedMap $k $v }}
{{-     end }}
{{-   end }}
{{-   if hasKey $args "requests" }}
{{-     range $k, $v := $args.requests }}
{{-       $_ := set $sanitizedMap $k $v }}
{{-     end }}
{{-   end }}
{{-   range $k, $v := $args }}
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
{{-       $_ := set $output.limits $k $v }}
{{-     end }}
{{-   end }}
{{-   $output | toYaml }}
{{- end  }}
