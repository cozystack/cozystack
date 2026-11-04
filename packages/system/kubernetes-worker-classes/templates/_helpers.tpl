{{- define "workerclasses.registrationName" -}}
{{- printf "%s-%s" .Release.Namespace .Release.Name | trunc 63 | trimSuffix "-" -}}
{{- end -}}
{{- define "workerclasses.image" -}}
{{- include "cozy-lib.image" (list (.Files.Get "images/kubernetes-worker-controller.tag" | trim) .) -}}
{{- end -}}
