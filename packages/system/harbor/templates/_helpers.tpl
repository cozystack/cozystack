{{- /*
  Spread for the data tier: the cluster-wide
  scheduling.globalAppTopologySpreadConstraints when the platform sets one, as
  the other applications do, otherwise a soft zone spread. Per-node spreading
  is left to the anti-affinity CloudNativePG and the Redis operator apply by
  default. Every entry is scoped to the given pod labels.

  Takes (list <root> <matchLabels>).
*/}}
{{- define "harbor.topologySpreadConstraints" -}}
{{- $root := index . 0 }}
{{- $selector := dict "matchLabels" (index . 1) }}
{{- $raw := dig "scheduling" "globalAppTopologySpreadConstraints" "" ($root.Values._cluster | default dict) }}
{{- $constraints := list }}
{{- if $raw }}
{{- range (get ($raw | fromYaml) "topologySpreadConstraints" | default list) }}
{{- $constraints = append $constraints (omit . "labelSelector") }}
{{- end }}
{{- end }}
{{- if not $constraints }}
{{- $constraints = list (dict "maxSkew" 1 "topologyKey" "topology.kubernetes.io/zone" "whenUnsatisfiable" "ScheduleAnyway") }}
{{- end }}
{{- $scoped := list }}
{{- range $constraints }}
{{- $scoped = append $scoped (merge (dict "labelSelector" $selector) .) }}
{{- end }}
{{- toYaml $scoped }}
{{- end }}

