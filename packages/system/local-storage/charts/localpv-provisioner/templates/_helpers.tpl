{{/* vim: set filetype=mustache: */}}
{{/*
Expand the name of the chart.
*/}}
{{- define "localpv.name" -}}
{{- default .Chart.Name .Values.nameOverride | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{/*
Create a default fully qualified localpv provisioner name.
We truncate at 63 chars because some Kubernetes name fields are limited to this (by the DNS naming spec).
If release name contains chart name it will be used as a full name.
*/}}
{{- define "localpv.fullname" -}}
{{- if .Values.fullnameOverride -}}
{{- .Values.fullnameOverride | trunc 63 | trimSuffix "-" -}}
{{- else -}}
{{- $name := default .Chart.Name .Values.nameOverride -}}
{{- if contains $name .Release.Name -}}
{{- .Release.Name | trunc 63 | trimSuffix "-" -}}
{{- else -}}
{{- printf "%s-%s" .Release.Name $name | trunc 63 | trimSuffix "-" -}}
{{- end -}}
{{- end -}}
{{- end -}}

{{/*
Create chart name and version as used by the chart label.
*/}}
{{- define "localpv.chart" -}}
{{- printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" | trunc 63 | trimSuffix "-" -}}
{{- end -}}


{{/*
Meta labels
*/}}
{{- define "localpv.common.metaLabels" -}}
chart: {{ template "localpv.chart" . }}
heritage: {{ .Release.Service }}
{{- end -}}

{{/*
Selector labels
*/}}
{{- define "localpv.selectorLabels" -}}
app: {{ template "localpv.name" . }}
release: {{ .Release.Name }}
component: {{ .Values.localpv.name | quote }}
{{- end -}}

{{/*
Component labels
*/}}
{{- define "localpv.componentLabels" -}}
openebs.io/component-name: openebs-{{ .Values.localpv.name }}
{{- end -}}

{{/*
Common labels
*/}}
{{- define "localpv.labels" -}}
{{ include "localpv.common.metaLabels" . }}
{{ include "localpv.selectorLabels" . }}
{{ include "localpv.componentLabels" . }}
{{- end -}}

{{/*
Create the name of the service account to use
*/}}
{{- define "localpv.serviceAccountName" -}}
{{- if .Values.serviceAccount.create -}}
    {{ default (include "localpv.fullname" .) .Values.serviceAccount.name }}
{{- else -}}
    {{ default "default" .Values.serviceAccount.name }}
{{- end -}}
{{- end -}}

{{/*
Name of the ConfigMap used to record analytics install-event state for the
current Helm release.

Usage:
  {{ include "localpv.analyticsStateCM.name" . }}
*/}}
{{- define "localpv.analyticsStateCM.name" -}}
{{- printf "%s-analytics-state" (include "localpv.fullname" .) | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{/*
Name of the Lease used by the provisioner to elect a single analytics
emitter in node-deployment mode.

Usage:
  {{ include "localpv.analyticsLease.name" . }}
*/}}
{{- define "localpv.analyticsLease.name" -}}
{{- printf "%s-analytics" (include "localpv.fullname" .) | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{/*
Creates the tolerations based on the global tolerations, with early eviction
Usage:
{{ include "tolerations_with_early_eviction" . }}
*/}}
{{- define "tolerations_with_early_eviction" -}}
{{- if .Values.earlyEvictionTolerations }}
    {{- toYaml .Values.earlyEvictionTolerations | nindent 8 }}
{{- end }}
{{- if .Values.localpv.tolerations }}
    {{- toYaml .Values.localpv.tolerations | nindent 8 }}
{{- end }}
{{- end }}

{{/*
Creates the image URL ie registry/repository:tag
*/}}
{{- define "localpv.common.image" -}}
{{ $registryName := "" }}
{{- if .override -}}
{{- $registryName = default .imageRoot.registry .global.imageRegistry | trimSuffix "/" -}}
{{- else -}}
{{- $registryName = default .global.imageRegistry .imageRoot.registry | trimSuffix "/" -}}
{{- end -}}
{{- $repositoryName := .imageRoot.repository -}}
{{- $termination := .imageRoot.tag | toString -}}
{{- if $registryName }}
    {{- printf "%s/%s:%s" $registryName $repositoryName $termination -}}
{{- else -}}
    {{- printf "%s:%s"  $repositoryName $termination -}}
{{- end -}}
{{- end -}}

{{/*
Concatenates imagepullsecrets, outputs in ENV format and handles different formats (example - secret or - name: secret)
*/}}
{{- define "localpv.helper.pullSecrets" -}}
{{- $names := list -}}
{{- with .Values.global.imagePullSecrets -}}
  {{- range . -}}
    {{- if kindIs "map" . }}
      {{- if and (hasKey . "name") (not (empty .name)) -}}
        {{ $names = append $names .name }}
      {{- end -}}
    {{- else if not (empty .) -}}
      {{ $names = append $names . -}}
    {{- end -}}
  {{- end -}}
{{- end -}}
{{- with .Values.imagePullSecrets -}}
  {{- range . }}
    {{- if kindIs "map" . -}}
      {{- if and (hasKey . "name") (not (empty .name)) -}}
        {{- $names = append $names .name }}
      {{- end -}}
    {{- else if not (empty .) -}}
      {{- $names = append $names . -}}
    {{- end -}}
  {{- end -}}
{{- end -}}
{{- if $names }}
- name: OPENEBS_IO_IMAGE_PULL_SECRETS
  value: "{{ join "," ($names | uniq) }}"
{{- end -}}
{{- end -}}

{{/*
Concatenates imagepullsecrets and handles different formats (example - secret or - name: secret)
*/}}
{{- define "localpv.common.pullSecrets" -}}
{{- $names := list -}}
{{- with .Values.global.imagePullSecrets -}}
  {{- range . -}}
    {{- if kindIs "map" . }}
      {{- if and (hasKey . "name") (not (empty .name)) -}}
        {{ $names = append $names .name }}
      {{- end -}}
    {{- else if not (empty .) -}}
      {{ $names = append $names . -}}
    {{- end -}}
  {{- end -}}
{{- end -}}
{{- with .Values.imagePullSecrets -}}
  {{- range . }}
    {{- if kindIs "map" . -}}
      {{- if and (hasKey . "name") (not (empty .name)) -}}
        {{- $names = append $names .name }}
      {{- end -}}
    {{- else if not (empty .) -}}
      {{- $names = append $names . -}}
    {{- end -}}
  {{- end -}}
{{- end -}}
{{- $names = uniq $names -}}
{{- if $names -}}
  {{- range $names }}
- name: {{ . }}
  {{- end -}}
{{- end -}}
{{- end -}}
