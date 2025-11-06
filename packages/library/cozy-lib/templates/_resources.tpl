{{- define "cozy-lib.resources.defaultCpuAllocationRatio" }}
{{-   `10` }}
{{- end }}
{{- define "cozy-lib.resources.defaultMemoryAllocationRatio" }}
{{-   `1` }}
{{- end }}
{{- define "cozy-lib.resources.defaultEphemeralStorageAllocationRatio" }}
{{-   `40` }}
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

{{- define "cozy-lib.resources.memoryAllocationRatio" }}
{{-   include "cozy-lib.loadCozyConfig" . }}
{{-   $cozyConfig := index . 1 "cozyConfig" }}
{{-   if not $cozyConfig }}
{{-     include "cozy-lib.resources.defaultMemoryAllocationRatio" . }}
{{-   else }}
{{-     dig "data" "memory-allocation-ratio" (include "cozy-lib.resources.defaultMemoryAllocationRatio" dict) $cozyConfig }}
{{-   end }}
{{- end }}

{{- define "cozy-lib.resources.ephemeralStorageAllocationRatio" }}
{{-   include "cozy-lib.loadCozyConfig" . }}
{{-   $cozyConfig := index . 1 "cozyConfig" }}
{{-   if not $cozyConfig }}
{{-     include "cozy-lib.resources.defaultEphemeralStorageAllocationRatio" . }}
{{-   else }}
{{-     dig "data" "ephemeral-storage-allocation-ratio" (include "cozy-lib.resources.defaultEphemeralStorageAllocationRatio" dict) $cozyConfig }}
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
{{-   $memoryAllocationRatio := include "cozy-lib.resources.memoryAllocationRatio" . | float64 }}
{{-   $ephemeralStorageAllocationRatio := include "cozy-lib.resources.ephemeralStorageAllocationRatio" . | float64 }}
{{-   $args := index . 0 }}
{{-   $output := dict "requests" dict "limits" dict }}
{{-   if or (hasKey $args "limits") (hasKey $args "requests") }}
{{-     fail "ERROR: A flat map of resources expected, not nested `requests:` or `limits:` sections." -}}
{{-   end }}
{{-   range $k, $v := $args }}
{{-     if eq $k "cpu" }}
{{-       $vcpuRequestF64 := (include "cozy-lib.resources.toFloat" $v) | float64 }}
{{-       $cpuRequestF64 := divf $vcpuRequestF64 $cpuAllocationRatio }}
{{-       $_ := set $output.requests $k ($cpuRequestF64 | toString) }}
{{-       $_ := set $output.limits $k ($v | toString) }}
{{-     else if eq $k "memory" }}
{{-       $vMemoryRequestF64 := (include "cozy-lib.resources.toFloat" $v) | float64 }}
{{-       $memoryRequestF64 := divf $vMemoryRequestF64 $memoryAllocationRatio }}
{{-       $_ := set $output.requests $k ($memoryRequestF64 | int | toString ) }}
{{-       $_ := set $output.limits $k ($v | toString) }}
{{-     else if eq $k "ephemeral-storage" }}
{{-       $vEphemeralStorageRequestF64 := (include "cozy-lib.resources.toFloat" $v) | float64 }}
{{-       $ephemeralStorageRequestF64 := divf $vEphemeralStorageRequestF64 $ephemeralStorageAllocationRatio }}
{{-       $_ := set $output.requests $k ($ephemeralStorageRequestF64 | int | toString) }}
{{-       $_ := set $output.limits $k ($v | toString) }}
{{-     else }}
{{-       $_ := set $output.requests $k $v }}
{{-       $_ := set $output.limits $k $v }}
{{-     end }}
{{-   end }}
{{-   $output | toYaml }}
{{- end  }}

{{- /*
  The defaultingSanitize helper takes a 3-element list as its argument:
  {{- include "cozy-lib.resources.defaultingSanitize" list (
      .Values.resourcesPreset
      .Values.resources
      $
  ) }}
  and returns the same result as
  {{- include "cozy-lib.resources.sanitize" list (
      .Values.resources
      $
  ) }}, however if cpu, memory, or ephemeral storage is not specified in
  .Values.resources, it is filled from the resource presets.

  Example input (cpuAllocationRatio = 10):
  ========================================
  resources:
    cpu: "1"
  resourcesPreset: "nano"

  Example output:
  ===============
  resources:
    limits:
      cpu: "1" # == user input
      ephemeral-storage: 2Gi # == default ephemeral storage limit
      memory: 128Mi # from "nano"
    requests:
      cpu: 100m # == 1 / 10
      ephemeral-storage: 50Mi # == default ephemeral storage request
      memory: 128Mi # memory request == limit
*/}}
{{- define "cozy-lib.resources.defaultingSanitize" }}
{{-   $preset := index . 0 }}
{{-   $resources := index . 1 }}
{{-   $global := index . 2 }}
{{-   $presetMap := include "cozy-lib.resources.unsanitizedPreset" $preset | fromYaml }}
{{-   $mergedMap := deepCopy (default (dict) $resources) | mergeOverwrite $presetMap }}
{{-   include "cozy-lib.resources.sanitize" (list $mergedMap $global) }}
{{- end }}

{{- /*
    javaHeap takes a .Values.resources and returns Java heap settings based on
    memory requests and limits. -Xmx is set to 75% of memory limits, -Xms is
    set to the lesser of requests or 25% of limits. Accepts only sanitized
    resource maps.
*/}}
{{- define "cozy-lib.resources.javaHeap" }}
{{-   $memoryRequestInt := include "cozy-lib.resources.toFloat" .requests.memory | float64 | int64 }}
{{-   $memoryLimitInt := include "cozy-lib.resources.toFloat" .limits.memory | float64 | int64 }}
{{- /* 4194304 is 4Mi */}}
{{-   $xmxMi := div (mul $memoryLimitInt 3) 4194304 }}
{{-   $xmsMi := min (div $memoryLimitInt 4194304) (div $memoryRequestInt 1048576) }}
{{-   printf `-Xms%dm -Xmx%dm` $xmsMi $xmxMi }}
{{- end }}

{{- define "cozy-lib.resources.flatten" -}}
{{- $out := dict -}}
{{- $res := include "cozy-lib.resources.sanitize" . | fromYaml -}}
{{- range $section, $values := $res }}
  {{- range $k, $v := $values }}
    {{- $key := printf "%s.%s" $section $k }}
    {{- if ne $key "limits.storage" }}
        {{- $_ := set $out $key $v }}
    {{- end }}
  {{- end }}
{{- end }}
{{- $out | toYaml }}
{{- end }}
