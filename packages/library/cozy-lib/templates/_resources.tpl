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
  A sanitized resource map is a dict with resource-name to resource-quantity.
  All resources are returned with equal **requests** and **limits**, except for
  **cpu**, whose *request* is reduced by the CPU-allocation ratio obtained from
  `cozy-lib.resources.cpuAllocationRatio`.

  The template now expects **one flat map** as input (no nested `requests:` /
  `limits:` sections).  Each value in that map is taken as the *limit* for the
  corresponding resource.  Usage example:

      {{ include "cozy-lib.resources.sanitize" list (.Values.resources $) }}

  Example input:
  ==============
  cpu: "2"
  memory: 256Mi
  devices.com/nvidia: "1"

  Example output (cpuAllocationRatio = 10):
  =========================================
  limits:
    cpu: "2"
    memory: 256Mi
    devices.com/nvidia: "1"
  requests:
    cpu: 200m                 # 2 / 10
    memory: 256Mi             # = limit
    devices.com/nvidia: "1"   # = limit
*/}}
{{- define "cozy-lib.resources.sanitize" }}
{{-   $cpuAllocationRatio := include "cozy-lib.resources.cpuAllocationRatio" . | float64 }}
{{-   $args := index . 0 }}
{{-   $output := dict "requests" dict "limits" dict }}
{{-   if or (hasKey $args "limits") (hasKey $args "requests") }}
{{-     fail "ERROR: A flat map of resources expected, not nested `requests:` or `limits:` sections." -}}
{{-   end }}
{{-   range $k, $v := $args }}
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
