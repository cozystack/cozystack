{{- define "incloud-web-resources.factory.labels" -}}
{{- $i := (default 0 .reqIndex) -}}
{{- $type := (default "labels" .type) -}}
{{- $title := (default "Labels" .title) -}}
{{- $jsonPath := (default ".metadata.labels" .jsonPath) -}}
{{- $endpoint := (default "" .endpoint) -}}
{{- $pathToValue := (default "/metadata/labels" .pathToValue) -}}
{{- $maxTagTextLength := (default 35 .maxTagTextLength) -}}
{{- $maxEditTagTextLength := (default 35 .maxEditTagTextLength) -}}
{{- $notificationSuccessMessage := (default "Updated successfully" .notificationSuccessMessage) -}}
{{- $notificationSuccessMessageDescription := (default "Labels have been updated" .notificationSuccessMessageDescription) -}}
{{- $modalTitle := (default "Edit labels" .modalTitle) -}}
{{- $modalDescriptionText := (default "" .modalDescriptionText) -}}
{{- $inputLabel := (default "" .inputLabel) -}}
{{- $containerMarginTop := (default "-30px" .containerMarginTop) -}}
- type: antdText
  data:
    id: {{ printf "%s-title" $type }}
    text: "{{ $title }}"
    strong: true
    style:
      fontSize: 14
- type: Labels
  data:
    id: {{ printf "%s-editor" $type }}
    reqIndex: {{ $i }}
    jsonPathToLabels: "{{ $jsonPath }}"
    selectProps:
      maxTagTextLength: {{ $maxTagTextLength }}
    notificationSuccessMessage: "{{ $notificationSuccessMessage }}"
    notificationSuccessMessageDescription: "{{ $notificationSuccessMessageDescription }}"
    modalTitle: "{{ $modalTitle }}"
    modalDescriptionText: "{{ $modalDescriptionText }}"
    inputLabel: "{{ $inputLabel }}"
    containerStyle:
      marginTop: "{{ $containerMarginTop }}"
    maxEditTagTextLength: {{ $maxEditTagTextLength }}
    endpoint: "{{ $endpoint }}"
    pathToValue: "{{ $pathToValue }}"
    editModalWidth: 650
    paddingContainerEnd: "24px"
{{- end -}}

{{- define "incloud-web-resources.factory.labels.base.selector" -}}
{{- $i := (default 0 .reqIndex) -}}
{{- $type := (default "pod-selector" .type) -}}
{{- $title := (default "Pod selector" .title) -}}
{{- $jsonPath := (default ".spec.template.metadata.labels" .jsonPath) -}}
- type: antdText
  data:
    id: {{ printf "%s-selector" $type }}
    text: "{{ $title }}"
    strong: true
    style:
      fontSize: 14
- type: LabelsToSearchParams
  data:
    id: {{ printf "%s-to-search-params" $type }}
    reqIndex: {{$i}}
    jsonPathToLabels: "{{ $jsonPath }}"
    linkPrefix: "{{ .linkPrefix | default "/openapi-ui/{2}/search" }}"
    errorText: "-"
{{- end -}}

