{{/* vim: set filetype=mustache: */}}

{{/*
  Resource presets follow a cloud instance-type naming convention: <series>.<size>

  Series (CPU:Memory ratio):
    t1 — Tiny       (1:0.5) e.g. t1.small = 1 CPU / 512Mi
    c1 — Compute    (1:1)   e.g. c1.small = 1 CPU / 1Gi
    s1 — Standard   (1:2)   e.g. s1.small = 1 CPU / 2Gi
    u1 — Universal  (1:4)   e.g. u1.small = 1 CPU / 4Gi
    m1 — Memory     (1:8)   e.g. m1.small = 1 CPU / 8Gi

  Sizes (CPU): nano 250m, micro 500m, small 1, medium 2, large 4,
               xlarge 8, 2xlarge 16, 4xlarge 32.

  Ephemeral storage is 2Gi for all presets.

  Legacy flat names (nano, micro, small, medium, large, xlarge, 2xlarge)
  are retained as backward-compatibility aliases with their original
  resource values. They are deprecated and will be removed in a future
  release; migrate to instance-type names. See migration table in
  docs/operations/resource-presets.md.
*/}}

{{- define "cozy-lib.resources.unsanitizedPreset" }}

{{-   $presets := dict
        "t1.nano"    (dict "cpu" "250m" "memory" "128Mi" "ephemeral-storage" "2Gi")
        "t1.micro"   (dict "cpu" "500m" "memory" "256Mi" "ephemeral-storage" "2Gi")
        "t1.small"   (dict "cpu" "1"    "memory" "512Mi" "ephemeral-storage" "2Gi")
        "t1.medium"  (dict "cpu" "2"    "memory" "1Gi"   "ephemeral-storage" "2Gi")
        "t1.large"   (dict "cpu" "4"    "memory" "2Gi"   "ephemeral-storage" "2Gi")
        "t1.xlarge"  (dict "cpu" "8"    "memory" "4Gi"   "ephemeral-storage" "2Gi")
        "t1.2xlarge" (dict "cpu" "16"   "memory" "8Gi"   "ephemeral-storage" "2Gi")
        "t1.4xlarge" (dict "cpu" "32"   "memory" "16Gi"  "ephemeral-storage" "2Gi")

        "c1.nano"    (dict "cpu" "250m" "memory" "256Mi" "ephemeral-storage" "2Gi")
        "c1.micro"   (dict "cpu" "500m" "memory" "512Mi" "ephemeral-storage" "2Gi")
        "c1.small"   (dict "cpu" "1"    "memory" "1Gi"   "ephemeral-storage" "2Gi")
        "c1.medium"  (dict "cpu" "2"    "memory" "2Gi"   "ephemeral-storage" "2Gi")
        "c1.large"   (dict "cpu" "4"    "memory" "4Gi"   "ephemeral-storage" "2Gi")
        "c1.xlarge"  (dict "cpu" "8"    "memory" "8Gi"   "ephemeral-storage" "2Gi")
        "c1.2xlarge" (dict "cpu" "16"   "memory" "16Gi"  "ephemeral-storage" "2Gi")
        "c1.4xlarge" (dict "cpu" "32"   "memory" "32Gi"  "ephemeral-storage" "2Gi")

        "s1.nano"    (dict "cpu" "250m" "memory" "512Mi" "ephemeral-storage" "2Gi")
        "s1.micro"   (dict "cpu" "500m" "memory" "1Gi"   "ephemeral-storage" "2Gi")
        "s1.small"   (dict "cpu" "1"    "memory" "2Gi"   "ephemeral-storage" "2Gi")
        "s1.medium"  (dict "cpu" "2"    "memory" "4Gi"   "ephemeral-storage" "2Gi")
        "s1.large"   (dict "cpu" "4"    "memory" "8Gi"   "ephemeral-storage" "2Gi")
        "s1.xlarge"  (dict "cpu" "8"    "memory" "16Gi"  "ephemeral-storage" "2Gi")
        "s1.2xlarge" (dict "cpu" "16"   "memory" "32Gi"  "ephemeral-storage" "2Gi")
        "s1.4xlarge" (dict "cpu" "32"   "memory" "64Gi"  "ephemeral-storage" "2Gi")

        "u1.nano"    (dict "cpu" "250m" "memory" "1Gi"   "ephemeral-storage" "2Gi")
        "u1.micro"   (dict "cpu" "500m" "memory" "2Gi"   "ephemeral-storage" "2Gi")
        "u1.small"   (dict "cpu" "1"    "memory" "4Gi"   "ephemeral-storage" "2Gi")
        "u1.medium"  (dict "cpu" "2"    "memory" "8Gi"   "ephemeral-storage" "2Gi")
        "u1.large"   (dict "cpu" "4"    "memory" "16Gi"  "ephemeral-storage" "2Gi")
        "u1.xlarge"  (dict "cpu" "8"    "memory" "32Gi"  "ephemeral-storage" "2Gi")
        "u1.2xlarge" (dict "cpu" "16"   "memory" "64Gi"  "ephemeral-storage" "2Gi")
        "u1.4xlarge" (dict "cpu" "32"   "memory" "128Gi" "ephemeral-storage" "2Gi")

        "m1.nano"    (dict "cpu" "250m" "memory" "2Gi"   "ephemeral-storage" "2Gi")
        "m1.micro"   (dict "cpu" "500m" "memory" "4Gi"   "ephemeral-storage" "2Gi")
        "m1.small"   (dict "cpu" "1"    "memory" "8Gi"   "ephemeral-storage" "2Gi")
        "m1.medium"  (dict "cpu" "2"    "memory" "16Gi"  "ephemeral-storage" "2Gi")
        "m1.large"   (dict "cpu" "4"    "memory" "32Gi"  "ephemeral-storage" "2Gi")
        "m1.xlarge"  (dict "cpu" "8"    "memory" "64Gi"  "ephemeral-storage" "2Gi")
        "m1.2xlarge" (dict "cpu" "16"   "memory" "128Gi" "ephemeral-storage" "2Gi")
        "m1.4xlarge" (dict "cpu" "32"   "memory" "256Gi" "ephemeral-storage" "2Gi")
}}

{{/*
  DEPRECATED legacy aliases. Kept for backward compatibility with existing
  HelmRelease and app CR values written before the instance-type rename.
  Each alias returns the exact resource values it had before the rename,
  so charts continue rendering identical resources without a migration.
  Migration 39 converts these to the equivalent instance-type names; new
  code should not introduce legacy names.
*/}}
{{-   $legacyAliases := dict
        "nano"    (dict "cpu" "250m" "memory" "128Mi" "ephemeral-storage" "2Gi")
        "micro"   (dict "cpu" "500m" "memory" "256Mi" "ephemeral-storage" "2Gi")
        "small"   (dict "cpu" "1"    "memory" "512Mi" "ephemeral-storage" "2Gi")
        "medium"  (dict "cpu" "1"    "memory" "1Gi"   "ephemeral-storage" "2Gi")
        "large"   (dict "cpu" "2"    "memory" "2Gi"   "ephemeral-storage" "2Gi")
        "xlarge"  (dict "cpu" "4"    "memory" "4Gi"   "ephemeral-storage" "2Gi")
        "2xlarge" (dict "cpu" "8"    "memory" "8Gi"   "ephemeral-storage" "2Gi")
}}
{{-   $presets := merge $presets $legacyAliases }}

{{-   if not (hasKey $presets .) -}}
{{-     $allowed := keys $presets | sortAlpha -}}
{{-     printf "ERROR: Preset key '%s' invalid. Allowed values are %s" . (join "," $allowed) | fail -}}
{{-   end }}
{{-   index $presets . | toYaml }}
{{- end }}
