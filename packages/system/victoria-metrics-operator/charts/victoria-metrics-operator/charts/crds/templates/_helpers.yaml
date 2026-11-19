{{- define "crds.upgrade.name" -}}
{{- print (include "vm.plain.fullname" .) "-upgrade-crds" }}
{{- end -}}

{{- define "crds.upgrade.serviceAccountName" -}}
{{- $Values := (.helm).Values | default .Values }}
{{- $upgrade := $Values.upgrade }}
{{- if $upgrade.serviceAccount.create -}}
    {{ default (include "crds.upgrade.name" .) $upgrade.serviceAccount.name }}
{{- else -}}
    {{ default "default" $upgrade.serviceAccount.name }}
{{- end -}}
{{- end -}}
