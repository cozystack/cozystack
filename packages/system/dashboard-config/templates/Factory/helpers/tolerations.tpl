{{- define "incloud-web-resources.factory.tolerations.block" -}}
{{- $i := (default 0 .reqIndex) -}}
{{- $jsonPathToArray := (default "" .jsonPathToArray) -}}
{{- $endpoint := (default "" .endpoint) -}}
{{- $pathToValue := (default "" .pathToValue) -}}
- type: antdText
  data:
    id: tolerations
    strong: true
    text: Tolerations
- type: Tolerations
  data:
    id: tolerations
    reqIndex: {{ $i }}
    jsonPathToArray: "{{ $jsonPathToArray }}"
    text: "~counter~ Tolerations"
    errorText: "0 Tolerations"
    notificationSuccessMessage: "Updated successfully"
    notificationSuccessMessageDescription: "Tolerations have been updated"
    modalTitle: "Edit tolerations"
    modalDescriptionText: ""
    inputLabel: ""
    endpoint: "{{ $endpoint }}"
    pathToValue: "{{ $pathToValue }}"
    editModalWidth: "1000px"
    cols:
      - 8
      - 3
      - 8
      - 4
      - 1
{{- end -}}
