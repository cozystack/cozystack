{{/*
Copyright Broadcom, Inc. All Rights Reserved.
SPDX-License-Identifier: APACHE-2.0
*/}}

{{/* vim: set filetype=mustache: */}}

{{/*
Return a resource request/limit object based on a given preset.
These presets are for basic testing and not meant to be used in production
{{ include "cozy-lib.resources.preset" "nano" -}}
*/}}
{{- define "cozy-lib.resources.preset" -}}
{{-   $cpuAllocationRatio := include "cozy-lib.resources.cpuAllocationRatio" . | float64 }}
{{-   $args := index . 0 }}

{{-   $baseCPU := dict
        "nano"    (dict "requests" (dict "cpu" "100m" ))
        "micro"   (dict "requests" (dict "cpu" "250m" ))
        "small"   (dict "requests" (dict "cpu" "500m" ))
        "medium"  (dict "requests" (dict "cpu" "500m" ))
        "large"   (dict "requests" (dict "cpu" "1"    ))
        "xlarge"  (dict "requests" (dict "cpu" "2"    ))
        "2xlarge" (dict "requests" (dict "cpu" "4"    ))
}}
{{-   $baseMemory := dict
        "nano"    (dict "requests" (dict "memory" "128Mi" ))
        "micro"   (dict "requests" (dict "memory" "256Mi" ))
        "small"   (dict "requests" (dict "memory" "512Mi" ))
        "medium"  (dict "requests" (dict "memory" "1Gi"   ))
        "large"   (dict "requests" (dict "memory" "2Gi"   ))
        "xlarge"  (dict "requests" (dict "memory" "4Gi"   ))
        "2xlarge" (dict "requests" (dict "memory" "8Gi"   ))
}}

{{-   range $baseCPU }}
{{-     $_ := set . "limits" (dict "cpu" (include "cozy-lib.resources.toFloat" .requests.cpu | float64 | mulf $cpuAllocationRatio | toString)) }}
{{-   end }}
{{-   range $baseMemory }}
{{-     $_ := set . "limits" (dict "memory" .requests.memory) }}
{{-   end }}

{{- $presets := dict 
  "nano" (dict 
      "requests" (dict "ephemeral-storage" "50Mi")
      "limits" (dict "ephemeral-storage" "2Gi")
   )
  "micro" (dict 
      "requests" (dict "ephemeral-storage" "50Mi")
      "limits" (dict "ephemeral-storage" "2Gi")
   )
  "small" (dict 
      "requests" (dict "ephemeral-storage" "50Mi")
      "limits" (dict "ephemeral-storage" "2Gi")
   )
  "medium" (dict 
      "requests" (dict "ephemeral-storage" "50Mi")
      "limits" (dict "ephemeral-storage" "2Gi")
   )
  "large" (dict 
      "requests" (dict "ephemeral-storage" "50Mi")
      "limits" (dict "ephemeral-storage" "2Gi")
   )
  "xlarge" (dict 
      "requests" (dict "ephemeral-storage" "50Mi")
      "limits" (dict "ephemeral-storage" "2Gi")
   )
  "2xlarge" (dict 
      "requests" (dict "ephemeral-storage" "50Mi")
      "limits" (dict "ephemeral-storage" "2Gi")
   )
 }}
{{- $_ := merge $presets $baseCPU $baseMemory }}
{{- if hasKey $presets $args -}}
{{- index $presets $args | toYaml -}}
{{- else -}}
{{- printf "ERROR: Preset key '%s' invalid. Allowed values are %s" . (join "," (keys $presets)) | fail -}}
{{- end -}}
{{- end -}}
