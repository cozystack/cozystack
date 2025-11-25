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
Render all files matching a glob pattern with template processing
Usage: {{ include "cozystack.render-glob" (list . "bundles/system/cozyrds/*.yaml") }}
Escapes templates like {{ .name }} and {{ .namespace }} that should remain as-is
*/}}
{{- define "cozystack.render-glob" -}}
{{- $ := index . 0 }}
{{- $pattern := index . 1 }}
{{- range $path, $_ := $.Files.Glob $pattern }}
{{- $content := $.Files.Get $path }}
{{- /* Escape templates that should remain as-is using temporary markers */}}
{{- /* Process complex patterns first (longer matches) */}}
{{- $content = $content | replace "{{ slice .namespace 7 }}" "__TEMPLATE_SLICE_NAMESPACE_7__" }}
{{- $content = $content | replace "{{ slice .namespace" "__TEMPLATE_SLICE_NAMESPACE__" }}
{{- $content = $content | replace "{{ .namespace }}" "__TEMPLATE_DOT_NAMESPACE__" }}
{{- $content = $content | replace "{{ .name }}" "__TEMPLATE_DOT_NAME__" }}
{{- /* Render the template */}}
{{- $rendered := trim (tpl $content $) }}
{{- /* Restore escaped templates */}}
{{- $rendered = $rendered | replace "__TEMPLATE_DOT_NAME__" "{{ .name }}" }}
{{- $rendered = $rendered | replace "__TEMPLATE_DOT_NAMESPACE__" "{{ .namespace }}" }}
{{- $rendered = $rendered | replace "__TEMPLATE_SLICE_NAMESPACE__" "{{ slice .namespace" }}
{{- $rendered = $rendered | replace "__TEMPLATE_SLICE_NAMESPACE_7__" "{{ slice .namespace 7 }}" }}
---
{{ $rendered }}
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

{{/*
Build cozystack values structure from root values
Usage: {{ include "cozystack.build-values" . | nindent 8 }}
Returns: YAML string with cozystack values structure
*/}}
{{- define "cozystack.build-values" -}}
{{- $cozystack := dict }}
{{- if .Values.networking }}{{ $_ := set $cozystack "networking" .Values.networking }}{{ end }}
{{- if .Values.publishing }}{{ $_ := set $cozystack "publishing" .Values.publishing }}{{ end }}
{{- if .Values.scheduling }}{{ $_ := set $cozystack "scheduling" .Values.scheduling }}{{ end }}
{{- if .Values.authentication }}{{ $_ := set $cozystack "authentication" .Values.authentication }}{{ end }}
{{- if .Values.branding }}{{ $_ := set $cozystack "branding" .Values.branding }}{{ end }}
{{- if .Values.registries }}{{ $_ := set $cozystack "registries" .Values.registries }}{{ end }}
{{- if .Values.resources }}{{ $_ := set $cozystack "resources" .Values.resources }}{{ end }}
{{- $cozystack | toYaml | trim }}
{{- end -}}
