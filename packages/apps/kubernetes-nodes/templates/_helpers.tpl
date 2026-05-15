{{- /*
  kubernetes-nodes helpers
*/}}

{{- /*
  Resolve the parent CAPI Cluster CR by .Values.kubernetes (its name in
  the same namespace) and validate that it exists. Returns the Cluster
  object so callers can read clusterNetwork, controlPlaneEndpoint, and
  the owner UID for ownerReferences.
*/}}
{{- define "kubernetes-nodes.parentCluster" -}}
{{- $name := required ".Values.kubernetes is required" .Values.kubernetes -}}
{{- $cluster := lookup "cluster.x-k8s.io/v1beta1" "Cluster" .Release.Namespace $name -}}
{{- if not $cluster -}}
{{-   fail (printf "kubernetes-nodes: parent Cluster %q not found in namespace %q. Make sure the parent kubernetes HelmRelease is installed and Ready before installing this node pool." $name .Release.Namespace) -}}
{{- end -}}
{{- $cluster | toYaml -}}
{{- end -}}

{{- /*
  Standard labels every resource in this chart should carry. Includes a
  link back to the parent cluster so Cozystack tooling can correlate
  pools with their owning kubernetes app.
*/}}
{{- define "kubernetes-nodes.labels" -}}
app.kubernetes.io/managed-by: {{ .Release.Service | quote }}
app.kubernetes.io/instance:   {{ .Release.Name | quote }}
app.kubernetes.io/part-of:    "kubernetes-nodes"
cluster.x-k8s.io/cluster-name: {{ .Values.kubernetes | quote }}
cluster.x-k8s.io/deployment-name: {{ .Release.Name | quote }}
{{- end -}}

{{- /*
  ownerReferences fragment pointing at the parent Cluster CR so child
  resources get garbage-collected when the parent kubernetes app is
  uninstalled. Caller is expected to nindent this output under the
  resource's metadata block.
*/}}
{{- define "kubernetes-nodes.ownerRefs" -}}
{{- $cluster := include "kubernetes-nodes.parentCluster" . | fromYaml -}}
- apiVersion: cluster.x-k8s.io/v1beta1
  kind: Cluster
  name: {{ $cluster.metadata.name | quote }}
  uid: {{ $cluster.metadata.uid | quote }}
  blockOwnerDeletion: true
  controller: false
{{- end -}}
