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
Apply component overrides to a package and return modified package as YAML
Usage: {{ include "cozystack.apply-package-overrides" (list . $package) }}
*/}}
{{- define "cozystack.apply-package-overrides" -}}
{{- $ := index . 0 }}
{{- $package := index . 1 }}
{{- $packageCopy := deepCopy $package }}
{{- $componentName := $packageCopy.name }}
{{- $component := index $.Values.components $componentName }}
{{- if $component }}
  {{- /* Apply enabled/disabled */}}
  {{- if hasKey $component "enabled" }}
    {{- if not $component.enabled }}
      {{- $_ := set $packageCopy "disabled" true }}
    {{- else if hasKey $packageCopy "disabled" }}
      {{- $_ := unset $packageCopy "disabled" }}
    {{- end }}
  {{- end }}
  {{- /* Apply values override */}}
  {{- if $component.values }}
    {{- if hasKey $packageCopy "values" }}
      {{- $_ := set $packageCopy "values" (deepCopy $packageCopy.values | mergeOverwrite (deepCopy $component.values)) }}
    {{- else }}
      {{- $_ := set $packageCopy "values" (deepCopy $component.values) }}
    {{- end }}
  {{- end }}
{{- end }}
{{- $packageCopy | toYaml }}
{{- end -}}

{{/*
Render a template file with tpl and trim, applying component overrides
Usage: {{ include "cozystack.render-file" (list . "bundles/system/bundle-full.yaml") }}
*/}}
{{- define "cozystack.render-file" -}}
{{- $ := index . 0 }}
{{- $filePath := index . 1 }}
{{- $rendered := trim (tpl ($.Files.Get $filePath) $) }}
{{- $bundle := $rendered | fromYaml }}
{{- if and $bundle $bundle.spec $bundle.spec.packages }}
{{- $modifiedPackages := list }}
{{- range $package := $bundle.spec.packages }}
  {{- $modifiedPackageYaml := include "cozystack.apply-package-overrides" (list $ $package) }}
  {{- $modifiedPackage := $modifiedPackageYaml | fromYaml }}
  {{- $modifiedPackages = append $modifiedPackages $modifiedPackage }}
{{- end }}
{{- $bundleCopy := deepCopy $bundle }}
{{- $_ := set $bundleCopy.spec "packages" $modifiedPackages }}
---
{{ $bundleCopy | toYaml }}
{{- else }}
---
{{ $rendered }}
{{- end }}
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

{{/*
Check if a component is enabled
Usage: {{ include "cozystack.component-enabled" (list . "metallb") }}
Returns: true if component is enabled, false otherwise
*/}}
{{- define "cozystack.component-enabled" -}}
{{- $ := index . 0 }}
{{- $componentName := index . 1 }}
{{- $component := index $.Values.components $componentName }}
{{- if $component }}
  {{- if hasKey $component "enabled" }}
    {{- $component.enabled }}
  {{- else }}
    {{- true }}
  {{- end }}
{{- else }}
  {{- true }}
{{- end }}
{{- end -}}

{{/*
Get component values override
Usage: {{ include "cozystack.component-values" (list . "cilium") }}
Returns: YAML string with component values or empty string
*/}}
{{- define "cozystack.component-values" -}}
{{- $ := index . 0 }}
{{- $componentName := index . 1 }}
{{- $component := index $.Values.components $componentName }}
{{- if $component }}
  {{- if $component.values }}
    {{- toYaml $component.values | nindent 2 }}
  {{- end }}
{{- end }}
{{- end -}}

{{/*
Merge component values into existing values
Usage: {{ include "cozystack.merge-component-values" (list . "cilium" (dict "key" "value")) }}
Returns: Merged values dictionary
*/}}
{{- define "cozystack.merge-component-values" -}}
{{- $ := index . 0 }}
{{- $componentName := index . 1 }}
{{- $defaultValues := index . 2 }}
{{- $component := index $.Values.components $componentName }}
{{- if $component }}
  {{- if $component.values }}
    {{- mergeOverwrite $defaultValues $component.values }}
  {{- else }}
    {{- $defaultValues }}
  {{- end }}
{{- else }}
  {{- $defaultValues }}
{{- end }}
{{- end -}}
