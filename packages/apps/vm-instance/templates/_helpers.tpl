{{/*
Expand the name of the chart.
*/}}
{{- define "virtual-machine.name" -}}
{{- default .Chart.Name .Values.nameOverride | trunc 63 | trimSuffix "-" }}
{{- end }}

{{/*
Create a default fully qualified app name.
We truncate at 63 chars because some Kubernetes name fields are limited to this (by the DNS naming spec).
If release name contains chart name it will be used as a full name.
*/}}
{{- define "virtual-machine.fullname" -}}
{{- if .Values.fullnameOverride }}
{{- .Values.fullnameOverride | trunc 63 | trimSuffix "-" }}
{{- else }}
{{- $name := default .Chart.Name .Values.nameOverride }}
{{- if contains $name .Release.Name }}
{{- .Release.Name | trunc 63 | trimSuffix "-" }}
{{- else }}
{{- printf "%s-%s" .Release.Name $name | trunc 63 | trimSuffix "-" }}
{{- end }}
{{- end }}
{{- end }}

{{/*
Create chart name and version as used by the chart label.
*/}}
{{- define "virtual-machine.chart" -}}
{{- printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" | trunc 63 | trimSuffix "-" }}
{{- end }}

{{/*
Common labels
*/}}
{{- define "virtual-machine.labels" -}}
helm.sh/chart: {{ include "virtual-machine.chart" . }}
{{ include "virtual-machine.selectorLabels" . }}
{{- if .Chart.AppVersion }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
{{- end }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
{{- end }}

{{/*
Selector labels
*/}}
{{- define "virtual-machine.selectorLabels" -}}
app.kubernetes.io/name: {{ include "virtual-machine.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end }}

{{/*
Generate a stable UUID for cloud-init re-initialization upon upgrade.
*/}}
{{- define "virtual-machine.stableUuid" -}}
{{- $source := printf "%s-%s-%s" .Release.Namespace (include "virtual-machine.fullname" .) .Values.cloudInitSeed }}
{{- $hash := sha256sum $source }}
{{- $uuid := printf "%s-%s-4%s-9%s-%s" (substr 0 8 $hash) (substr 8 12 $hash) (substr 13 16 $hash) (substr 17 20 $hash) (substr 20 32 $hash) }}
{{- if eq .Values.cloudInitSeed "" }}
  {{- /*  Try to save previous uuid to not trigger full cloud-init again if user decided to remove the seed. */}}
  {{- $vmResource := lookup "kubevirt.io/v1" "VirtualMachine" .Release.Namespace (include "virtual-machine.fullname" .) -}}
  {{- if $vmResource }}
    {{- $existingUuid := $vmResource | dig "spec" "template" "spec" "domain" "firmware" "uuid" "" }}
    {{- if $existingUuid }}
      {{- $uuid = $existingUuid }}
    {{- end }}
  {{- end }}
{{- end }}
{{- $uuid }}
{{- end }}

{{/*
Node Affinity for Windows VMs
*/}}
{{- define "virtual-machine.nodeAffinity" -}}
{{- $configMap := lookup "v1" "ConfigMap" "cozy-system" "cozystack-scheduling" -}}
{{- if $configMap -}}
{{- $dedicatedNodesForWindowsVMs := get $configMap.data "dedicatedNodesForWindowsVMs" -}}
{{- if eq $dedicatedNodesForWindowsVMs "true" -}}
{{- $isWindows := hasPrefix "windows" (toString .Values.instanceProfile) -}}
affinity:
  nodeAffinity:
    {{- if $isWindows }}
    requiredDuringSchedulingIgnoredDuringExecution:
      nodeSelectorTerms:
      - matchExpressions:
        - key: scheduling.cozystack.io/vm-windows
          operator: In
          values:
          - "true"
    {{- else }}
    preferredDuringSchedulingIgnoredDuringExecution:
    - weight: 100
      preference:
        matchExpressions:
        - key: scheduling.cozystack.io/vm-windows
          operator: NotIn
          values:
          - "true"
    {{- end }}
{{- end -}}
{{- end -}}
{{- end -}}
