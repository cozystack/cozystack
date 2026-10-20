{{- define "monitoring.topologySpreadConstraints" -}}
{{- $root := index . 0 -}}
{{- $selector := dict "matchLabels" (dict "app.kubernetes.io/name" (index . 1) "app.kubernetes.io/instance" (index . 2)) -}}
{{- $global := list -}}
{{- with ($root.Values._cluster | default dict).scheduling -}}
{{-   with get . "globalAppTopologySpreadConstraints" -}}
{{-     $parsed := fromYaml . -}}
{{-     $global = dig "topologySpreadConstraints" (list) $parsed -}}
{{-     if or (not (kindIs "slice" $global)) (not $global) -}}
{{-       fail (trim (printf "monitoring: _cluster.scheduling.globalAppTopologySpreadConstraints must be YAML with a non-empty topologySpreadConstraints list. %s" (get $parsed "Error"))) -}}
{{-     end -}}
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
