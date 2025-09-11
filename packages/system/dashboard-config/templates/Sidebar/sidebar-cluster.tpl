{{- define "incloud-web-resources.sidebar.menu.items.cluster" -}}
- key: administration
  label: Administration
  children:
  - key: tenantnamespaces
    label: TenantNamespaces
    link: /openapi-ui/{clusterName}/api-table/core.cozystack.io/v1alpha1/tenantnamespaces
  - key: storageclasses
    label: StorageClasses
    link: /openapi-ui/{clusterName}/api-table/storage.k8s.io/v1/storageclasses
  - key: persistentvolumes
    label: PersistentVolumes
    link: /openapi-ui/{clusterName}/builtin-table/persistentvolumes
{{- end }}
