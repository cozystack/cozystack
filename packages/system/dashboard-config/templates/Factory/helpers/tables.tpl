{{- define "incloud-web-resources.factory.containers.table" -}}
  {{- $i := (default 0 .reqIndex) -}}
  {{- $type := (default "" .type) -}}
  {{- $title := (default "Init containers" .title) -}}
  {{- $jsonPath := (default "" .jsonPath) -}}
  {{- $pathToItems := (default "" .pathToItems) -}}
  {{- $apiGroup := (default "" .apiGroup) -}}
  {{- $kind := (default "" .kind) -}}
  {{- $resourceName := (default "" .resourceName) -}}
  {{- $namespace := (default "" .namespace) -}}
  {{- $namespacePart := "" -}}
  {{- if ne $namespace "" }}
    {{- $namespacePart = printf "namespaces/%s/" $namespace -}}
  {{- end }}
- type: VisibilityContainer
  data:
    id: {{ printf "%s-container" $type }}
    value: "{reqsJsonPath[{{$i}}]['{{ $jsonPath }}']['-']}"
    style:
      margin: 0
      padding: 0
  children:
    - type: antdText
      data:
        id: {{ printf "%s-title" $type }}
        text: "{{ $title }}"
        strong: true
        style:
          fontSize: 22
          marginBottom: 32px
    - type: EnrichedTable
      data:
        id: {{ printf "%s-table" $type }}
        fetchUrl: "/api/clusters/{2}/k8s/{{ $apiGroup }}/{{$namespacePart}}{{ $kind }}/{{$resourceName}}"
        clusterNamePartOfUrl: "{2}"
        customizationId: {{ .customizationId | default ("") }}
        baseprefix: "/openapi-ui"
        withoutControls: {{ default true .withoutControls }}
        pathToItems: {{ $pathToItems }}
{{- end -}}
