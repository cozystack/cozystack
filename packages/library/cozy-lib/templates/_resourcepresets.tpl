{{/* vim: set filetype=mustache: */}}

{{/*
  Resource presets follow a cloud instance type naming convention: <series>.<size>

  Series (CPU:Memory ratio):
    s1 — Standard  (1:2)  e.g. s1.small = 1 CPU / 2Gi
    u1 — Universal (1:4)  e.g. u1.small = 1 CPU / 4Gi
    m1 — Memory    (1:8)  e.g. m1.small = 1 CPU / 8Gi

  Sizes: nano (250m), micro (500m), small (1), medium (2), large (4),
         xlarge (8), 2xlarge (16), 4xlarge (32)

  Ephemeral storage is 2Gi for all presets.
*/}}

{{- define "cozy-lib.resources.unsanitizedPreset" }}

{{-   $presets := dict
        "s1.nano"    (dict "cpu" "250m" "memory" "512Mi"  "ephemeral-storage" "2Gi")
        "s1.micro"   (dict "cpu" "500m" "memory" "1Gi"    "ephemeral-storage" "2Gi")
        "s1.small"   (dict "cpu" "1"    "memory" "2Gi"    "ephemeral-storage" "2Gi")
        "s1.medium"  (dict "cpu" "2"    "memory" "4Gi"    "ephemeral-storage" "2Gi")
        "s1.large"   (dict "cpu" "4"    "memory" "8Gi"    "ephemeral-storage" "2Gi")
        "s1.xlarge"  (dict "cpu" "8"    "memory" "16Gi"   "ephemeral-storage" "2Gi")
        "s1.2xlarge" (dict "cpu" "16"   "memory" "32Gi"   "ephemeral-storage" "2Gi")
        "s1.4xlarge" (dict "cpu" "32"   "memory" "64Gi"   "ephemeral-storage" "2Gi")

        "u1.nano"    (dict "cpu" "250m" "memory" "1Gi"    "ephemeral-storage" "2Gi")
        "u1.micro"   (dict "cpu" "500m" "memory" "2Gi"    "ephemeral-storage" "2Gi")
        "u1.small"   (dict "cpu" "1"    "memory" "4Gi"    "ephemeral-storage" "2Gi")
        "u1.medium"  (dict "cpu" "2"    "memory" "8Gi"    "ephemeral-storage" "2Gi")
        "u1.large"   (dict "cpu" "4"    "memory" "16Gi"   "ephemeral-storage" "2Gi")
        "u1.xlarge"  (dict "cpu" "8"    "memory" "32Gi"   "ephemeral-storage" "2Gi")
        "u1.2xlarge" (dict "cpu" "16"   "memory" "64Gi"   "ephemeral-storage" "2Gi")
        "u1.4xlarge" (dict "cpu" "32"   "memory" "128Gi"  "ephemeral-storage" "2Gi")

        "m1.nano"    (dict "cpu" "250m" "memory" "2Gi"    "ephemeral-storage" "2Gi")
        "m1.micro"   (dict "cpu" "500m" "memory" "4Gi"    "ephemeral-storage" "2Gi")
        "m1.small"   (dict "cpu" "1"    "memory" "8Gi"    "ephemeral-storage" "2Gi")
        "m1.medium"  (dict "cpu" "2"    "memory" "16Gi"   "ephemeral-storage" "2Gi")
        "m1.large"   (dict "cpu" "4"    "memory" "32Gi"   "ephemeral-storage" "2Gi")
        "m1.xlarge"  (dict "cpu" "8"    "memory" "64Gi"   "ephemeral-storage" "2Gi")
        "m1.2xlarge" (dict "cpu" "16"   "memory" "128Gi"  "ephemeral-storage" "2Gi")
        "m1.4xlarge" (dict "cpu" "32"   "memory" "256Gi"  "ephemeral-storage" "2Gi")
}}

{{-   if not (hasKey $presets .) -}}
{{-     printf "ERROR: Preset key '%s' invalid. Allowed values are %s" . (join "," (keys $presets)) | fail -}}
{{-   end }}
{{-   index $presets . | toYaml }}
{{- end }}

{{/*
  Return a resource request/limit object based on a given preset.
  {{- include "cozy-lib.resources.preset" list ("s1.nano" $) }}
*/}}
{{- define "cozy-lib.resources.preset" -}}
{{-   $cpuAllocationRatio := include "cozy-lib.resources.cpuAllocationRatio" . | float64 }}
{{-   $args := index . 0 }}
{{-   $global := index . 1 }}
{{-   $unsanitizedPreset := include "cozy-lib.resources.unsanitizedPreset" $args | fromYaml }}
{{-   include "cozy-lib.resources.sanitize" (list $unsanitizedPreset $global) }}
{{- end -}}
