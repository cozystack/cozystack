{{- define "hcloud-cloud-controller-manager.name" -}}
{{- $.Values.nameOverride | default $.Chart.Name | trunc 63 | trimSuffix "-" }}
{{- end }}

{{- define "hcloud-cloud-controller-manager.selectorLabels" -}}
{{- tpl (toYaml $.Values.selectorLabels) $ }}
{{- end }}
