{{- define "incloud-web-resources.factory.time.create" -}}
{{- $i := (default 0 .reqIndex) -}}
- type: antdText
  data:
    id: {{ .labelId | default "time-label" }}
    strong: true
    text: "{{ .text | default "Created" }}"
- type: antdFlex
  data:
    id: {{ .id | default "time-block" }}
    align: center
    gap: 6
  children:
    - type: antdText
      data:
        id: {{ .iconId | default "time-icon" }}
        text: "🌐"
    - type: parsedText
      data:
        id: {{ .valueId | default "time-value" }}
        text: "{reqsJsonPath[{{$i}}]['{{ .req }}']['-']}"
        formatter: timestamp
{{- end -}}

{{- define "incloud-web-resources.factory.timeblock" -}}
{{- $i := (default 0 .reqIndex) -}}
- type: antdFlex
  data:
    id: {{ .id | default "time-block" }}
    align: center
    gap: 6
  children:
    - type: antdText
      data:
        id: {{ .iconId | default "time-icon" }}
        text: "🌐"
    - type: parsedText
      data:
        id: {{ .valueId | default "time-value" }}
        text: "{reqsJsonPath[{{$i}}]['{{ .req }}']['-']}"
        formatter: timestamp
{{- end -}}
