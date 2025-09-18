{{- define "incloud-web-resources.factory.links.details" -}}
{{- $i := (default 0 .reqIndex) -}}
{{- $type := (default "" .type) -}}
{{- $title := (default "" .title) -}}
{{- $jsonPath := (default "" .jsonPath) -}}
{{- $factory := (default "" .factory) -}}
{{- $ns := (default "" .namespace) -}}

{{- $nsPart := "" -}}
{{- if ne $ns "" }}
  {{- $nsPart = printf "%s/" $ns -}}
{{- end }}
- type: parsedText
  data:
    id: {{ printf "%s-title" $type }}
    strong: true
    text: "{{ $title }}"
    style:
      fontWeight: bold
- type: antdLink
  data:
    id: {{ printf "%s-link" $type }}
    text: "{reqsJsonPath[{{$i}}]['{{ $jsonPath }}']['-']}"
    href: "/openapi-ui/{2}/{{$nsPart}}factory/{{ $factory }}/{reqsJsonPath[{{$i}}]['{{ $jsonPath }}']['-']}"
{{- end -}}

{{- define "incloud-web-resources.factory.linkblock" -}}
{{- $i := (default 0 .reqIndex) -}}
{{- $type := (default "" .type) -}}
{{- $jsonPath := (default "" .jsonPath) -}}
{{- $factory := (default "" .factory) -}}
{{- $ns := (default "" .namespace) -}}

{{- $nsPart := "" -}}
{{- if ne $ns "" }}
  {{- $nsPart = printf "%s/" $ns -}}
{{- end }}
- type: antdLink
  data:
    id: {{ printf "%s-link" $type }}
    text: "{reqsJsonPath[{{$i}}]['{{ $jsonPath }}']['-']}"
    href: "/openapi-ui/{2}/{{$nsPart}}factory/{{ $factory }}/{reqsJsonPath[{{$i}}]['{{ $jsonPath }}']['-']}"
{{- end -}}
