{{- define "incloud-web-resources.customformoverride.hidden.metadata.system" -}}
- - metadata
  - creationTimestamp
- - metadata
  - deletionGracePeriodSeconds
- - metadata
  - deletionTimestamp
- - metadata
  - finalizers
- - metadata
  - generateName
- - metadata
  - generation
- - metadata
  - managedFields
- - metadata
  - ownerReferences
- - metadata
  - resourceVersion
- - metadata
  - selfLink
- - metadata
  - uid
{{- end -}}

{{- define "incloud-web-resources.customformoverride.hidden.metadata.system-clusterscope" -}}
- - metadata
  - creationTimestamp
- - metadata
  - namespace
- - metadata
  - deletionGracePeriodSeconds
- - metadata
  - deletionTimestamp
- - metadata
  - finalizers
- - metadata
  - generateName
- - metadata
  - generation
- - metadata
  - managedFields
- - metadata
  - ownerReferences
- - metadata
  - resourceVersion
- - metadata
  - selfLink
- - metadata
  - uid
{{- end -}}

{{- define "incloud-web-resources.customformoverride.hidden.metadata.system.job-template" -}}
- - spec
  - jobTemplate
  - metadata
  - creationTimestamp
- - spec
  - jobTemplate
  - metadata
  - namespace
- - spec
  - jobTemplate
  - metadata
  - deletionGracePeriodSeconds
- - spec
  - jobTemplate
  - metadata
  - deletionTimestamp
- - spec
  - jobTemplate
  - metadata
  - finalizers
- - spec
  - jobTemplate
  - metadata
  - generateName
- - spec
  - jobTemplate
  - metadata
  - generation
- - spec
  - jobTemplate
  - metadata
  - managedFields
- - spec
  - jobTemplate
  - metadata
  - ownerReferences
- - spec
  - jobTemplate
  - metadata
  - resourceVersion
- - spec
  - jobTemplate
  - metadata
  - selfLink
- - spec
  - jobTemplate
  - metadata
  - uid
{{- end -}}

{{- define "incloud-web-resources.customformoverride.hidden.metadata.system.template" -}}
- - spec
  - template
  - metadata
  - creationTimestamp
- - spec
  - template
  - metadata
  - namespace
- - spec
  - template
  - metadata
  - deletionGracePeriodSeconds
- - spec
  - template
  - metadata
  - deletionTimestamp
- - spec
  - template
  - metadata
  - finalizers
- - spec
  - template
  - metadata
  - generateName
- - spec
  - template
  - metadata
  - generation
- - spec
  - template
  - metadata
  - managedFields
- - spec
  - template
  - metadata
  - ownerReferences
- - spec
  - template
  - metadata
  - resourceVersion
- - spec
  - template
  - metadata
  - selfLink
- - spec
  - template
  - metadata
  - uid
{{- end -}}

{{- define "incloud-web-resources.customformoverride.hidden.metadata.api" -}}
- - kind
- - apiVersion
{{- end -}}

{{- define "incloud-web-resources.customformoverride.hidden.status" -}}
- - status
{{- end -}}
