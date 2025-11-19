{{/*
Get IP-addresses of master nodes
*/}}
{{- define "cozystack.master-node-ips" -}}
{{- $nodes := lookup "v1" "Node" "" "" -}}
{{- $ips := list -}}
{{- range $node := $nodes.items -}}
  {{- if eq (index $node.metadata.labels "node-role.kubernetes.io/control-plane") "" -}}
    {{- range $address := $node.status.addresses -}}
      {{- if eq $address.type "InternalIP" -}}
        {{- $ips = append $ips $address.address -}}
        {{- break -}}
      {{- end -}}
    {{- end -}}
  {{- end -}}
{{- end -}}
{{ join "," $ips }}
{{- end -}}

{{/*
Render a template file with tpl and trim
Usage: {{ include "cozystack.render-file" (list . "bundles/system/bundle-full.yaml") }}
*/}}
{{- define "cozystack.render-file" -}}
{{- $ := index . 0 }}
{{- $filePath := index . 1 }}
---
{{ trim (tpl ($.Files.Get $filePath) $) }}
{{- end -}}

{{/*
Render all files matching a glob pattern
Usage: {{ include "cozystack.render-glob" (list . "bundles/system/cozyrds/*.yaml") }}
*/}}
{{- define "cozystack.render-glob" -}}
{{- $ := index . 0 }}
{{- $pattern := index . 1 }}
{{- range $path, $_ := $.Files.Glob $pattern }}
---
{{ $.Files.Get $path }}
{{- end }}
{{- end -}}
