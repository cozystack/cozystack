{{- define "monitoring.topologySpreadConstraints" -}}
{{- $root := index . 0 -}}
{{- $selector := dict "matchLabels" (dict "app.kubernetes.io/name" (index . 1) "app.kubernetes.io/instance" (index . 2)) -}}
{{- $global := list -}}
{{- with ($root.Values._cluster | default dict).scheduling -}}
{{-   with get . "globalAppTopologySpreadConstraints" -}}
{{-     $global = dig "topologySpreadConstraints" (list) (fromYaml .) -}}
{{-   end -}}
{{- end -}}
{{- $constraints := list -}}
{{- range $global -}}
{{-   $constraints = append $constraints (ternary . (merge (dict "labelSelector" $selector) .) (hasKey . "labelSelector")) -}}
{{- end -}}
{{- if not $constraints -}}
{{-   $default := dict "maxSkew" 1 "topologyKey" "kubernetes.io/hostname" "whenUnsatisfiable" (index . 3) "labelSelector" $selector -}}
{{-   if eq (index . 3) "DoNotSchedule" -}}
{{-     $_ := set $default "nodeTaintsPolicy" "Ignore" -}}
{{-   end -}}
{{-   $constraints = list $default -}}
{{- end -}}
{{- toYaml (dict "topologySpreadConstraints" $constraints) -}}
{{- end -}}
