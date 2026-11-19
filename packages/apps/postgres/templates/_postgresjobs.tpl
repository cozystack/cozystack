{{/*
Generate resource requirements based on ResourceQuota named as the namespace.
*/}}
{{- define "postgresjobs.defaultResources" }}
cpu: "1"
memory: 512Mi
{{- end }}
{{- define "postgresjobs.resources" }}
resources:
{{ include "cozy-lib.resources.sanitize" (list (include "postgresjobs.defaultResources" $ | fromYaml) $) | nindent 2 }}
{{- end }}
