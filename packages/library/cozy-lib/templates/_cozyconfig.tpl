{{/*
Cluster-wide configuration helpers.
These helpers read from .Values._cluster which is populated via valuesFrom from Secret cozystack-values.
*/}}

{{/*
Get the root host for the cluster.
Usage: {{ include "cozy-lib.root-host" . }}
*/}}
{{- define "cozy-lib.root-host" -}}
{{- (index .Values._cluster "root-host") | default "" }}
{{- end }}

{{/*
Get the bundle name for the cluster.
Usage: {{ include "cozy-lib.bundle-name" . }}
*/}}
{{- define "cozy-lib.bundle-name" -}}
{{- (index .Values._cluster "bundle-name") | default "" }}
{{- end }}

{{/*
Get the images registry.
Usage: {{ include "cozy-lib.images-registry" . }}
*/}}
{{- define "cozy-lib.images-registry" -}}
{{- (index .Values._cluster "images-registry") | default "" }}
{{- end }}

{{/*
Get the ipv4 cluster CIDR.
Usage: {{ include "cozy-lib.ipv4-cluster-cidr" . }}
*/}}
{{- define "cozy-lib.ipv4-cluster-cidr" -}}
{{- (index .Values._cluster "ipv4-cluster-cidr") | default "" }}
{{- end }}

{{/*
Get the ipv4 service CIDR.
Usage: {{ include "cozy-lib.ipv4-service-cidr" . }}
*/}}
{{- define "cozy-lib.ipv4-service-cidr" -}}
{{- (index .Values._cluster "ipv4-service-cidr") | default "" }}
{{- end }}

{{/*
Get the ipv4 join CIDR.
Usage: {{ include "cozy-lib.ipv4-join-cidr" . }}
*/}}
{{- define "cozy-lib.ipv4-join-cidr" -}}
{{- (index .Values._cluster "ipv4-join-cidr") | default "" }}
{{- end }}

{{/*
Get scheduling configuration.
Usage: {{ include "cozy-lib.scheduling" . }}
Returns: YAML string of scheduling configuration
*/}}
{{- define "cozy-lib.scheduling" -}}
{{- if .Values._cluster.scheduling }}
{{- .Values._cluster.scheduling | toYaml }}
{{- end }}
{{- end }}

{{/*
Get branding configuration.
Usage: {{ include "cozy-lib.branding" . }}
Returns: YAML string of branding configuration
*/}}
{{- define "cozy-lib.branding" -}}
{{- if .Values._cluster.branding }}
{{- .Values._cluster.branding | toYaml }}
{{- end }}
{{- end }}

{{/*
Namespace-specific configuration helpers.
These helpers read from .Values._namespace which is populated via valuesFrom from Secret cozystack-values.
*/}}

{{/*
Get the host for this namespace.
Usage: {{ include "cozy-lib.ns-host" . }}
*/}}
{{- define "cozy-lib.ns-host" -}}
{{- .Values._namespace.host | default "" }}
{{- end }}

{{/*
Get the etcd namespace reference.
Usage: {{ include "cozy-lib.ns-etcd" . }}
*/}}
{{- define "cozy-lib.ns-etcd" -}}
{{- .Values._namespace.etcd | default "" }}
{{- end }}

{{/*
Get the ingress namespace reference.
Usage: {{ include "cozy-lib.ns-ingress" . }}
*/}}
{{- define "cozy-lib.ns-ingress" -}}
{{- .Values._namespace.ingress | default "" }}
{{- end }}

{{/*
Get the monitoring namespace reference.
Usage: {{ include "cozy-lib.ns-monitoring" . }}
*/}}
{{- define "cozy-lib.ns-monitoring" -}}
{{- .Values._namespace.monitoring | default "" }}
{{- end }}

{{/*
Get the seaweedfs namespace reference.
Usage: {{ include "cozy-lib.ns-seaweedfs" . }}
*/}}
{{- define "cozy-lib.ns-seaweedfs" -}}
{{- .Values._namespace.seaweedfs | default "" }}
{{- end }}

{{/*
Legacy helper - kept for backward compatibility during migration.
Loads config into context. Deprecated: use direct .Values._cluster access instead.
*/}}
{{- define "cozy-lib.loadCozyConfig" }}
{{-   include "cozy-lib.checkInput" . }}
{{-   if not (hasKey (index . 1) "cozyConfig") }}
{{-     $_ := set (index . 1) "cozyConfig" (dict "data" ((index . 1).Values._cluster | default dict)) }}
{{-   end }}
{{- end }}
