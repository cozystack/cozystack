{{/* vim: set filetype=mustache: */}}
{{/*
Expand the name of the chart.
*/}}
{{- define "trust-manager.name" -}}
{{- default .Chart.Name .Values.nameOverride | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{/*
Create chart name and version as used by the chart label.
*/}}
{{- define "trust-manager.chart" -}}
{{- printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{/*
Common labels
*/}}
{{- define "trust-manager.labels" -}}
app.kubernetes.io/name: {{ include "trust-manager.name" . }}
helm.sh/chart: {{ include "trust-manager.chart" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- if .Chart.AppVersion }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
{{- end }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
{{- if .Values.commonLabels}}
{{ toYaml .Values.commonLabels }}
{{- end }}
{{- end -}}

{{/*
Util function for generating the image URL based on the provided options.
IMPORTANT: This function is standardized across all charts in the cert-manager GH organization.
Any changes to this function should also be made in cert-manager, trust-manager, approver-policy, ...
See https://github.com/cert-manager/cert-manager/issues/6329 for a list of linked PRs.
*/}}
{{- define "image" -}}
{{- /*
Calling convention:

- (tuple <imageValues> <imageRegistry> <imageNamespace> <defaultReference>)

We intentionally pass imageRegistry/imageNamespace as explicit arguments rather than reading
from `.Values` inside this helper, because `helm-tool lint` does not reliably track `.Values.*`
usage through tuple/variable indirection.
*/ -}}

{{- if ne (len .) 4 -}}
    {{- fail (printf "ERROR: template \"image\" expects (tuple <imageValues> <imageRegistry> <imageNamespace> <defaultReference>), got %d arguments" (len .)) -}}
{{- end -}}

{{- $image := index . 0 -}}
{{- $imageRegistry := index . 1 | default "" -}}
{{- $imageNamespace := index . 2 | default "" -}}
{{- $defaultReference := index . 3 -}}

{{- $repository := "" -}}
{{- if $image.repository -}}
    {{- $repository = $image.repository -}}

    {{- /*
        Backwards compatibility: if image.registry is set, additionally prefix the repository with this registry.
    */ -}}
    {{- if $image.registry -}}
        {{- $repository = printf "%s/%s" $image.registry $repository -}}
    {{- end -}}
{{- else -}}
    {{- $name := required "ERROR: image.name must be set when image.repository is empty" $image.name -}}
    {{- $repository = $name -}}

    {{- if $imageNamespace -}}
        {{- $repository = printf "%s/%s" $imageNamespace $repository -}}
    {{- end -}}

    {{- if $imageRegistry -}}
        {{- $repository = printf "%s/%s" $imageRegistry $repository -}}
    {{- end -}}

    {{- /*
        Backwards compatibility: if image.registry is set, additionally prefix the repository with this registry.
    */ -}}
    {{- if $image.registry -}}
        {{- $repository = printf "%s/%s" $image.registry $repository -}}
    {{- end -}}
{{- end -}}

{{- $repository -}}
{{- if and $image.tag $image.digest -}}
    {{- printf ":%s@%s" $image.tag $image.digest -}}
{{- else if $image.tag -}}
    {{- printf ":%s" $image.tag -}}
{{- else if $image.digest -}}
    {{- printf "@%s" $image.digest -}}
{{- else -}}
    {{- printf "%s" $defaultReference -}}
{{- end -}}
{{- end }}

{{/*
Namespace for all resources to be installed into
If not defined in values file then the helm release namespace is used
By default this is not set so the helm release namespace will be used

This gets around an problem within helm discussed here
https://github.com/helm/helm/issues/5358
*/}}
{{- define "trust-manager.namespace" -}}
    {{ .Values.namespace | default .Release.Namespace }}
{{- end -}}

{{/*
Create the name of the service account to use
*/}}
{{- define "trust-manager.serviceAccountName" -}}
{{- if .Values.serviceAccount.create -}}
    {{ default (include "trust-manager.name" .) .Values.serviceAccount.name }}
{{- else -}}
    {{ default "default" .Values.serviceAccount.name }}
{{- end -}}
{{- end -}}

{{/*
Namespaced resources rules
*/}}
{{- define "trust-manager.rbac.namespacedResourcesRules" -}}
- apiGroups:
  - ""
  resources:
  - "configmaps"
  verbs: ["get","list","create","patch","watch","delete"]

- apiGroups:
  - "" {{/* TODO(erikgb): Remove; MIGRATION to new events API group. */}}
  - "events.k8s.io"
  resources:
  - "events"
  verbs: ["create","patch"]

{{- if .Values.secretTargets.enabled }}
  {{- if .Values.secretTargets.authorizedSecretsAll }}
- apiGroups:
  - ""
  resources:
  - "secrets"
  verbs: ["get","list","create","patch","watch","delete"]
  {{- else if $.Values.secretTargets.authorizedSecrets }}
- apiGroups:
  - ""
  resources:
  - "secrets"
  verbs: ["get","list","watch"]
- apiGroups:
  - ""
  resources:
  - "secrets"
  verbs: ["create","patch","delete"]
  resourceNames:
{{ toYaml .Values.secretTargets.authorizedSecrets | nindent 4 }}
  {{- end }}
{{- end }}
{{- end -}}
