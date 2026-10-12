{{- /*
  Spread for the request-path components: the cluster-wide
  scheduling.globalAppTopologySpreadConstraints when the platform sets one, as
  the other applications do, otherwise a soft zone and node spread. Any
  labelSelector is dropped because the upstream chart appends its own
  release/app/component selector to every entry, and a second one would be a
  duplicate key in the rendered Deployment.
*/}}
{{- define "harbor.topologySpreadConstraints" -}}
{{- $raw := dig "scheduling" "globalAppTopologySpreadConstraints" "" (.Values._cluster | default dict) }}
{{- $constraints := list }}
{{- if $raw }}
{{- range (get ($raw | fromYaml) "topologySpreadConstraints" | default list) }}
{{- $constraints = append $constraints (omit . "labelSelector") }}
{{- end }}
{{- end }}
{{- if not $constraints }}
{{- $constraints = list
      (dict "maxSkew" 1 "topologyKey" "topology.kubernetes.io/zone" "whenUnsatisfiable" "ScheduleAnyway")
      (dict "maxSkew" 1 "topologyKey" "kubernetes.io/hostname" "whenUnsatisfiable" "ScheduleAnyway") }}
{{- end }}
{{- toYaml $constraints }}
{{- end }}

{{- /*
  Replica count and placement for one stateless component of the upstream chart.
  Nothing beyond `replicas` is rendered for a single replica, so an install that
  keeps the default renders the same pod template as before.

  Upstream defaults podDisruptionBudget.minAvailable to 1 and renders whichever
  of minAvailable and maxUnavailable is truthy; the API server rejects a PDB
  carrying both, so minAvailable is zeroed to leave maxUnavailable alone.
*/}}
{{- define "harbor.replicaPlacement" -}}
{{- $replicas := int (index . 0) }}
replicas: {{ $replicas }}
{{- if gt $replicas 1 }}
podDisruptionBudget:
  enabled: true
  minAvailable: 0
  maxUnavailable: 1
topologySpreadConstraints:
{{- include "harbor.topologySpreadConstraints" (index . 1) | nindent 0 }}
{{- end }}
{{- end }}
