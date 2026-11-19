{{/* vim: set filetype=mustache: */}}
{{/*
Expand the name of the chart.
*/}}
{{- define "fluent-bit.name" -}}
{{- default .Chart.Name .Values.nameOverride | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{/*
Create a default fully qualified app name.
We truncate at 63 chars because some Kubernetes name fields are limited to this (by the DNS naming spec).
If release name contains chart name it will be used as a full name.
*/}}
{{- define "fluent-bit.fullname" -}}
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
{{- define "fluent-bit.chart" -}}
{{- printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{/*
Common labels
*/}}
{{- define "fluent-bit.labels" -}}
helm.sh/chart: {{ include "fluent-bit.chart" . }}
{{ include "fluent-bit.selectorLabels" . }}
{{- if .Chart.AppVersion }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
{{- end }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
{{- end -}}

{{/*
Selector labels
*/}}
{{- define "fluent-bit.selectorLabels" -}}
app.kubernetes.io/name: {{ include "fluent-bit.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end -}}

{{/*
Create the name of the service account to use
*/}}
{{- define "fluent-bit.serviceAccountName" -}}
{{- if .Values.serviceAccount.create -}}
    {{ default (include "fluent-bit.fullname" .) .Values.serviceAccount.name }}
{{- else -}}
    {{ default "default" .Values.serviceAccount.name }}
{{- end -}}
{{- end -}}

{{/*
Fluent-bit image with tag/digest
*/}}
{{- define "fluent-bit.image" -}}
{{- $tag := ternary "" (printf ":%s" (toString .tag)) (or (empty .tag) (eq "-" (toString .tag))) -}}
{{- $digest := ternary "" (printf "@%s" .digest) (empty .digest) -}}
{{- printf "%s%s%s" .repository $tag $digest -}}
{{- end -}}

{{/*
Ingress ApiVersion according k8s version
*/}}
{{- define "fluent-bit.ingress.apiVersion" -}}
{{- if and (.Capabilities.APIVersions.Has "networking.k8s.io/v1") (semverCompare ">=1.19-0" .Capabilities.KubeVersion.GitVersion) -}}
networking.k8s.io/v1
{{- else if and (.Capabilities.APIVersions.Has "networking.k8s.io/v1beta1") (semverCompare ">=1.14-0" .Capabilities.KubeVersion.GitVersion) -}}
networking.k8s.io/v1beta1
{{- else -}}
extensions/v1beta1
{{- end }}
{{- end }}

{{/*
Return if ingress is stable.
*/}}
{{- define "fluent-bit.ingress.isStable" -}}
  {{- eq (include "fluent-bit.ingress.apiVersion" .) "networking.k8s.io/v1" -}}
{{- end -}}
{{/*
Return if ingress supports ingressClassName.
*/}}
{{- define "fluent-bit.ingress.supportsIngressClassName" -}}
  {{- or (eq (include "fluent-bit.ingress.isStable" .) "true") (and (eq (include "fluent-bit.ingress.apiVersion" .) "networking.k8s.io/v1beta1") (semverCompare ">= 1.18-0" .Capabilities.KubeVersion.Version)) -}}
{{- end -}}
{{/*
Return if ingress supports pathType.
*/}}
{{- define "fluent-bit.ingress.supportsPathType" -}}
  {{- or (eq (include "fluent-bit.ingress.isStable" .) "true") (and (eq (include "fluent-bit.ingress.apiVersion" .) "networking.k8s.io/v1beta1") (semverCompare ">= 1.18-0" .Capabilities.KubeVersion.Version)) -}}
{{- end -}}

{{/*
Pdb apiVersion according k8s version and capabilities
*/}}
{{- define "fluent-bit.pdb.apiVersion" -}}
{{- if and (.Capabilities.APIVersions.Has "policy/v1") (semverCompare ">=1.21-0" .Capabilities.KubeVersion.GitVersion) -}}
policy/v1
{{- else -}}
policy/v1beta1
{{- end }}
{{- end -}}

{{/*
HPA ApiVersion according k8s version
Check legacy first so helm template / kustomize will default to latest version
*/}}
{{- define "fluent-bit.hpa.apiVersion" -}}
{{- if and (.Capabilities.APIVersions.Has "autoscaling/v2beta2") (semverCompare "<1.23-0" .Capabilities.KubeVersion.GitVersion) -}}
autoscaling/v2beta2
{{- else -}}
autoscaling/v2
{{- end -}}
{{- end -}}

{{/*
Create the name of OpenShift SecurityContextConstraints to use
*/}}
{{- define "fluent-bit.openShiftSccName" -}}
{{- if not .Values.openShift.securityContextConstraints.create -}}
{{- printf "%s" .Values.openShift.securityContextConstraints.existingName -}}
{{- else -}}
{{- printf "%s" (default (include "fluent-bit.fullname" .) .Values.openShift.securityContextConstraints.name) -}}
{{- end -}}
{{- end -}}
