{{- define "renderValues" -}}
{{- $prefix := .prefix | default (list "spec") -}}  {{/* Всегда начинаем с "spec" */}}
{{- range $key, $val := .values }}
  {{- $newPath := append $prefix $key }}
  {{- if kindIs "map" $val }}
    {{- include "renderValues" (dict "prefix" $newPath "values" $val) }}
  {{- else }}
- path:
{{ toYaml $newPath | indent 4 }}
  value: {{- if or (kindIs "slice" $val) (kindIs "map" $val) }}
{{ toYaml $val | indent 4 }}
    {{- else }} {{ $val | quote }}
    {{- end }}
  {{- end }}
{{- end -}}
{{- end -}}
