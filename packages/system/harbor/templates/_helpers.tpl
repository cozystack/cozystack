{{- /*
  Spread for the data tier: the cluster-wide
  scheduling.globalAppTopologySpreadConstraints when the platform sets one, as
  the other applications do, otherwise the defaults the caller passes. A
  constraint without a selector of its own gets the given pod labels, because
  kube-scheduler counts no pods for it otherwise; a selector the policy sets is
  kept, as in the keycloak chart.

  Takes (list <root> <matchLabels> <default constraints>).
*/}}
{{- define "harbor.topologySpreadConstraints" -}}
{{- $root := index . 0 }}
{{- $selector := dict "matchLabels" (index . 1) }}
{{- $constraints := index . 2 }}
{{- with dig "scheduling" "globalAppTopologySpreadConstraints" "" ($root.Values._cluster | default dict) }}
{{-   $policy := fromYaml . }}
{{-   $constraints = dig "topologySpreadConstraints" (list) $policy }}
{{-   if or (not (kindIs "slice" $constraints)) (not $constraints) }}
{{-     fail (trim (printf "harbor: _cluster.scheduling.globalAppTopologySpreadConstraints must be YAML with a non-empty topologySpreadConstraints list. %s" (get $policy "Error"))) }}
{{-   end }}
{{- end }}
{{- $scoped := list }}
{{- range $constraints }}
{{-   if kindIs "invalid" .labelSelector }}
{{-     $scoped = append $scoped (merge (dict "labelSelector" $selector) (omit . "labelSelector")) }}
{{-   else }}
{{-     $scoped = append $scoped . }}
{{-   end }}
{{- end }}
{{- toYaml $scoped }}
{{- end }}
