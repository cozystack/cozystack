{{- define "incloud-web-resources.factory.annotations.block" -}}
{{- $i := (default 0 .reqIndex) -}}
{{- $jsonPath := (default ".metadata.annotations" .jsonPath) -}}
{{- $endpoint := (default "" .endpoint) -}}
{{- $pathToValue := (default "/metadata/annotations" .pathToValue) -}}
- type: antdText
  data:
    id: annotations
    strong: true
    text: Annotations
- type: Annotations
  data:
    id: annotations
    reqIndex: 0
    jsonPathToObj: "{{ $jsonPath }}"
    text: "~counter~ Annotations"
    errorText: "0 Annotations"
    notificationSuccessMessage: "Updated successfully"
    notificationSuccessMessageDescription: "Annotations have been updated"
    modalTitle: "Edit annotations"
    modalDescriptionText: ""
    inputLabel: ""
    endpoint: "{{ $endpoint }}"
    pathToValue: "{{ $pathToValue }}"
    editModalWidth: "800px"
    cols:
      - 11
      - 11
      - 2
{{- end -}}
