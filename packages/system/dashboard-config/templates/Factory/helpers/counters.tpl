{{- define "incloud-web-resources.factory.counters.fields" -}}
{{- $i := (default 0 .reqIndex) -}}
{{- $type := (default "counter" .type) -}}
{{- $title := (default "" .title) -}}
- type: antdText
  data:
    id: {{ printf "%s-label" $type }}
    strong: true
    text: "{{ $title }}"
- type: ItemCounter
  data:
    id: {{ printf "%s-counter" $type }}
    text: "~counter~ {{ $type }}"
    reqIndex: {{$i}}
    jsonPathToArray: "{{ .jsonPath | default "" }}"
    errorText: "Error"
{{- end -}}

{{- define "incloud-web-resources.factory.counters.object.fields" -}}
{{- $i := (default 0 .reqIndex) -}}
{{- $type := (default "counter" .type) -}}
{{- $title := (default "" .title) -}}
- type: antdText
  data:
    id: {{ printf "%s-label" $type }}
    strong: true
    text: "{{ $title }}"
- type: KeyCounter
  data:
    id: {{ printf "%s-counter" $type }}
    text: "~counter~ {{ $type }}"
    reqIndex: {{$i}}
    jsonPathToObj: "{{ .jsonPath | default "" }}"
    errorText: "Error"
{{- end -}}
