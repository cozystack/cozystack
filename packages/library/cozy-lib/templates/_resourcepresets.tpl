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
{{-   $global := index . 1 }}

{{-   $baseCPU := dict
        "nano"    (dict "cpu" "100m" )
        "micro"   (dict "cpu" "250m" )
        "small"   (dict "cpu" "500m" )
        "medium"  (dict "cpu" "500m" )
        "large"   (dict "cpu" "1"    )
        "xlarge"  (dict "cpu" "2"    )
        "2xlarge" (dict "cpu" "4"    )
}}
{{-   $baseMemory := dict
        "nano"    (dict "memory" "128Mi" )
        "micro"   (dict "memory" "256Mi" )
        "small"   (dict "memory" "512Mi" )
        "medium"  (dict "memory" "1Gi"   )
        "large"   (dict "memory" "2Gi"   )
        "xlarge"  (dict "memory" "4Gi"   )
        "2xlarge" (dict "memory" "8Gi"   )
}}

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
{{- $flatDict := index $presets $args }}
{{- include "cozy-lib.resources.sanitize" (list $flatDict $global) }}
{{- else -}}
{{- printf "ERROR: Preset key '%s' invalid. Allowed values are %s" . (join "," (keys $presets)) | fail -}}
{{- end -}}
{{- end -}}
