{{/*
Expand the name of the chart.
*/}}
{{- define "linstor-scheduler.name" -}}
{{- default .Chart.Name .Values.nameOverride | trunc 63 | trimSuffix "-" }}
{{- end }}

{{/*
Create a default fully qualified app name.
We truncate at 63 chars because some Kubernetes name fields are limited to this (by the DNS naming spec).
If release name contains chart name it will be used as a full name.
*/}}
{{- define "linstor-scheduler.fullname" -}}
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
{{- define "linstor-scheduler.chart" -}}
{{- printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" | trunc 63 | trimSuffix "-" }}
{{- end }}

{{/*
Common labels
*/}}
{{- define "linstor-scheduler.labels" -}}
helm.sh/chart: {{ include "linstor-scheduler.chart" . }}
{{ include "linstor-scheduler.selectorLabels" . }}
{{- if .Chart.AppVersion }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
{{- end }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
{{- end }}

{{/*
Selector labels
*/}}
{{- define "linstor-scheduler.selectorLabels" -}}
app.kubernetes.io/name: {{ include "linstor-scheduler.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end }}

{{/*
Create the name of the service account to use
*/}}
{{- define "linstor-scheduler.serviceAccountName" -}}
{{- if .Values.serviceAccount.create }}
{{- default (include "linstor-scheduler.fullname" .) .Values.serviceAccount.name }}
{{- else }}
{{- default "default" .Values.serviceAccount.name }}
{{- end }}
{{- end }}

{{/*
Fully qualified name for the admission webhook resources. The base fullname is
truncated before the "-admission" suffix (and further sub-suffixes such as
"-selfsigned") are appended, so the longest derived name still fits the 63-char
Kubernetes name limit for long release names.
*/}}
{{- define "linstor-scheduler.admissionFullname" -}}
{{- printf "%s-admission" (include "linstor-scheduler.fullname" . | trunc 42 | trimSuffix "-") }}
{{- end }}

{{/*
Selector labels for the admission webhook. The name carries an "-admission"
suffix so the webhook pods are NOT matched by the base scheduler's selectors
(Deployment, PodDisruptionBudget), whose selector fields are immutable and so
cannot be narrowed after the fact. This keeps the base PDB and HPA scoped to the
scheduler pods only. The base name is truncated BEFORE the suffix is appended so
a long nameOverride can never eat the "-admission" suffix and collapse the name
back onto the base selector.
*/}}
{{- define "linstor-scheduler.admissionSelectorLabels" -}}
app.kubernetes.io/name: {{ printf "%s-admission" (include "linstor-scheduler.name" . | trunc 53 | trimSuffix "-") }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end }}

{{/*
Common labels for the admission webhook resources.
*/}}
{{- define "linstor-scheduler.admissionLabels" -}}
helm.sh/chart: {{ include "linstor-scheduler.chart" . }}
{{ include "linstor-scheduler.admissionSelectorLabels" . }}
{{- if .Chart.AppVersion }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
{{- end }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
app.kubernetes.io/component: admission
{{- end }}

{{/*
Name of the secret holding the admission webhook's TLS certificate.
Defaults to "<admissionFullname>-tls" unless admission.tls.secretName is set,
so users managing the secret externally can point the Deployment at their own.
*/}}
{{- define "linstor-scheduler.admissionTLSSecretName" -}}
{{- default (printf "%s-tls" (include "linstor-scheduler.admissionFullname" .)) .Values.admission.tls.secretName }}
{{- end }}

{{/*
Name of the service account used by the admission webhook. When the chart
creates service accounts it gets a dedicated one, so the webhook pod does not
inherit the scheduler's system:kube-scheduler / system:volume-scheduler bindings.
*/}}
{{- define "linstor-scheduler.admissionServiceAccountName" -}}
{{- if .Values.serviceAccount.create }}
{{- include "linstor-scheduler.admissionFullname" . }}
{{- else }}
{{- default "default" .Values.serviceAccount.name }}
{{- end }}
{{- end }}

{{/*
Get the kubernetes version we should assume for creating scheduler configs.
Strips distribution suffixes like +k3s1, +rke2r1 from version string.
*/}}
{{- define "linstor-scheduler.kubeVersion" }}
{{- $version := .Values.scheduler.image.compatibleKubernetesRelease | default .Capabilities.KubeVersion.Version }}
{{- regexReplaceAll "\\+.*$" $version "" }}
{{- end }}

{{/*
Find the linstor client secret containing TLS certificates
*/}}
{{- define "linstor-scheduler.linstorClientSecretName" -}}
{{- if .Values.linstor.clientSecret }}
{{- .Values.linstor.clientSecret }}
{{- else if .Capabilities.APIVersions.Has "piraeus.linbit.com/v1/LinstorController" }}
{{- $crs := (lookup "piraeus.linbit.com/v1" "LinstorController" .Release.Namespace "").items }}
{{- if $crs }}
{{- if eq (len $crs) 1 }}
{{- $item := index $crs 0 }}
{{- $item.spec.linstorHttpsClientSecret }}
{{- end }}
{{- end }}
{{- else if .Capabilities.APIVersions.Has "linstor.linbit.com/v1/LinstorController" }}
{{- $crs := (lookup "linstor.linbit.com/v1" "LinstorController" .Release.Namespace "").items }}
{{- if $crs }}
{{- if eq (len $crs) 1 }}
{{- $item := index $crs 0 }}
{{- $item.spec.linstorHttpsClientSecret }}
{{- end }}
{{- end }}
{{- end }}
{{- end }}

{{/*
Find the linstor URL by operator resources
*/}}
{{- define "linstor-scheduler.linstorEndpointFromCRD" -}}
{{- if .Capabilities.APIVersions.Has "piraeus.linbit.com/v1/LinstorController" }}
{{- $crs := (lookup "piraeus.linbit.com/v1" "LinstorController" .Release.Namespace "").items }}
{{- if $crs }}
{{- if eq (len $crs) 1 }}
{{- $item := index $crs 0 }}
{{- if include "linstor-scheduler.linstorClientSecretName" . }}
{{- printf "https://%s.%s.svc:3371" $item.metadata.name $item.metadata.namespace }}
{{- else }}
{{- printf "http://%s.%s.svc:3370" $item.metadata.name $item.metadata.namespace }}
{{- end }}
{{- end }}
{{- end }}
{{- else if .Capabilities.APIVersions.Has "linstor.linbit.com/v1/LinstorController" }}
{{- $crs := (lookup "linstor.linbit.com/v1" "LinstorController" .Release.Namespace "").items }}
{{- if $crs }}
{{- if eq (len $crs) 1 }}
{{- $item := index $crs 0 }}
{{- if include "linstor-scheduler.linstorClientSecretName" . }}
{{- printf "https://%s.%s.svc:3371" $item.metadata.name $item.metadata.namespace }}
{{- else }}
{{- printf "http://%s.%s.svc:3370" $item.metadata.name $item.metadata.namespace }}
{{- end }}
{{- end }}
{{- end }}
{{- end }}
{{- end }}

{{/*
Find the linstor URL either by override or cluster resources
*/}}
{{- define "linstor-scheduler.linstorEndpoint" -}}
{{- if .Values.linstor.endpoint }}
{{- .Values.linstor.endpoint }}
{{- else }}
{{- $piraeus := include "linstor-scheduler.linstorEndpointFromCRD" . }}
{{- if $piraeus }}
{{- $piraeus }}
{{- else }}
{{- fail "Please specify linstor.endpoint, no default URL could be determined" }}
{{- end }}
{{- end }}
{{- end }}
