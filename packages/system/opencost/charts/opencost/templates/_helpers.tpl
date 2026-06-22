{{/* vim: set filetype=mustache: */}}
{{/*
Expand the name of the chart.
*/}}
{{- define "opencost.name" -}}
  {{- default .Chart.Name .Values.nameOverride | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{/*
Create chart name and version as used by the chart label.
*/}}
{{- define "opencost.chart" -}}
  {{- printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{/*
Create a default fully qualified app name.
We truncate at 63 chars because some Kubernetes name fields are limited to this (by the DNS naming spec).
*/}}
{{- define "opencost.fullname" -}}
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
Allow the release namespace to be overridden
*/}}
{{- define "opencost.namespace" -}}
  {{- if .Values.namespaceOverride -}}
    {{- .Values.namespaceOverride -}}
  {{- else -}}
    {{- .Release.Namespace -}}
  {{- end -}}
{{- end -}}

{{/*
Create a default fully qualified app name.
We truncate at 63 chars because some Kubernetes name fields are limited to this (by the DNS naming spec).
*/}}
{{- define "opencost.prometheus.secretname" -}}
  {{- if .Values.opencost.prometheus.secret_name -}}
    {{- .Values.opencost.prometheus.secret_name -}}
  {{- else -}}
    {{- include "opencost.fullname" . -}}
  {{- end -}}
{{- end -}}

{{/*
Resolve the secret name that stores plugin config files.
*/}}
{{- define "opencost.pluginsConfigSecretName" -}}
  {{- if .Values.plugins.existingSecret -}}
    {{- .Values.plugins.existingSecret -}}
  {{- else -}}
    {{- printf "%s-plugins-config" (include "opencost.fullname" .) -}}
  {{- end -}}
{{- end -}}

{{/*
Resolve plugin names to install/mount. Prefer explicit install list, then configs keys.
*/}}
{{- define "opencost.plugins.installList" -}}
  {{- if .Values.plugins.install.plugins -}}
    {{- toYaml .Values.plugins.install.plugins -}}
  {{- else if .Values.plugins.configs -}}
    {{- toYaml (keys .Values.plugins.configs | sortAlpha) -}}
  {{- else -}}
[]
  {{- end -}}
{{- end -}}

{{/*
Plugin configuration check. Fails fast on misconfigurations that would otherwise
produce silent data loss or runtime Pod start failures:

  1. plugins.existingSecret and plugins.configs are mutually exclusive. When
     existingSecret is set, the chart no longer renders the generated Secret
     from .Values.plugins.configs, so any configs entries would be silently
     ignored.
  2. When plugins.existingSecret is empty, every plugin listed in
     plugins.install.plugins must have a matching key in plugins.configs --
     otherwise the Deployment will mount a subPath that doesn't exist in the
     generated Secret and the Pod will fail to start. (We intentionally skip
     this check when existingSecret is set: the chart cannot introspect an
     externally-managed secret at template-render time.)
*/}}
{{- define "opencost.plugins.configCheck" -}}
  {{- if not .Values.plugins.enabled -}}
    {{- /* nothing to validate when plugins are off */ -}}
  {{- else -}}
    {{- if and .Values.plugins.existingSecret .Values.plugins.configs -}}
      {{- fail "plugins.existingSecret and plugins.configs are mutually exclusive. When plugins.existingSecret is set the chart does not render a generated Secret from plugins.configs, so any configs entries would be silently ignored. Please specify only one." -}}
    {{- end -}}
    {{- if and (not .Values.plugins.existingSecret) .Values.plugins.install.plugins -}}
      {{- $configs := default (dict) .Values.plugins.configs -}}
      {{- range $p := .Values.plugins.install.plugins -}}
        {{- if not (hasKey $configs $p) -}}
          {{- fail (printf "plugins.install.plugins contains %q but plugins.configs has no matching %q entry. The Deployment mounts %s_config.json from the generated plugin secret, so the Pod will fail to start with a missing subPath. Either add a plugins.configs.%s entry or set plugins.existingSecret to a Secret that contains %s_config.json." $p $p $p $p $p) -}}
        {{- end -}}
      {{- end -}}
    {{- end -}}
  {{- end -}}
{{- end -}}

{{/*
Cloud integration source contents check. Either the Secret must be specified or the JSON, not both.
*/}}
{{- define "opencost.cloudIntegration.secretConfigCheck" -}}
  {{- if and .Values.opencost.cloudIntegrationSecret .Values.opencost.cloudIntegrationJSON -}}
    {{- fail "opencost.cloudIntegrationSecret and opencost.cloudIntegrationJSON are mutually exclusive. Please specify only one." -}}
  {{- end -}}
{{- end -}}

{{/*
Compute the cloud integration secret name when enabled.
*/}}
{{- define "opencost.cloudIntegration.secretName" -}}
  {{- if or .Values.opencost.cloudIntegrationSecret .Values.opencost.cloudIntegrationJSON -}}
    {{- if .Values.opencost.cloudIntegrationSecret -}}
      {{- .Values.opencost.cloudIntegrationSecret -}}
    {{- else -}}
      {{- printf "%s-cloud-integration" (include "opencost.fullname" .) -}}
    {{- end -}}
  {{- end -}}
{{- end -}}

{{/*
Common labels
*/}}
{{- define "opencost.labels" -}}
helm.sh/chart: {{ include "opencost.chart" . }}
{{ include "opencost.selectorLabels" . }}
{{- if .Chart.AppVersion }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
{{- end }}
app.kubernetes.io/part-of: {{ template "opencost.name" . }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
{{- if .Values.commonLabels}}
{{ toYaml .Values.commonLabels }}
{{- end }}
{{- end -}}

{{/*
Selector labels
*/}}
{{- define "opencost.selectorLabels" -}}
app.kubernetes.io/name: {{ include "opencost.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end -}}

{{/*
Create the name of the controller service account to use
*/}}
{{- define "opencost.serviceAccountName" -}}
  {{- if .Values.serviceAccount.create -}}
    {{- default (include "opencost.fullname" .) .Values.serviceAccount.name }}
  {{- else -}}
    {{- default "opencost" .Values.serviceAccount.name }}
  {{- end -}}
{{- end -}}

{{/*
Create the name of the controller service account to use
*/}}
{{- define "opencost.prometheusServerEndpoint" -}}
  {{- if .Values.opencost.prometheus.external.enabled -}}
    {{ tpl .Values.opencost.prometheus.external.url . }}
  {{- else if (and .Values.opencost.prometheus.amp.enabled .Values.opencost.sigV4Proxy) -}}
    {{- $port := .Values.opencost.sigV4Proxy.port | int }}
    {{- $ws := .Values.opencost.prometheus.amp.workspaceId }}
    {{- printf "http://localhost:%d/workspaces/%v" $port $ws -}}
{{- else -}}
    {{- $host := tpl .Values.opencost.prometheus.internal.serviceName . }}
    {{- $ns := tpl .Values.opencost.prometheus.internal.namespaceName . }}
    {{- $clusterName := .Values.clusterName }}
    {{- $scheme := .Values.opencost.prometheus.internal.scheme | default "http"}}
    {{- $port := .Values.opencost.prometheus.internal.port | int }}
    {{- $path := .Values.opencost.prometheus.internal.path | default "" }}
    {{- if $path }}
      {{- printf "%s://%s.%s.svc.%s:%d%s" $scheme $host $ns $clusterName $port $path -}}
    {{- else }}
      {{- printf "%s://%s.%s.svc.%s:%d" $scheme $host $ns $clusterName $port -}}
    {{- end }}
{{- end -}}
{{- end -}}

{{/*
Check that either thanos external or internal is defined
*/}}
{{- define "opencost.thanosServerEndpoint" -}}
  {{- if .Values.opencost.prometheus.thanos.external.enabled -}}
    {{ .Values.opencost.prometheus.thanos.external.url }}
  {{- else -}}
    {{- $host := .Values.opencost.prometheus.thanos.internal.serviceName }}
    {{- $ns := .Values.opencost.prometheus.thanos.internal.namespaceName }}
    {{- $clusterName := .Values.clusterName }}
    {{- $port := .Values.opencost.prometheus.thanos.internal.port | int }}
    {{- $scheme := .Values.opencost.prometheus.thanos.internal.scheme | default "http"}}
    {{- printf "%s://%s.%s.svc.%s:%d" $scheme $host $ns $clusterName $port -}}
  {{- end -}}
{{- end -}}

{{/*
  Fail if both kube-rbac-proxy and bearer token are set
*/}}
{{- define "kubeRBACProxyBearerTokenCheck" -}}
{{- if and .Values.opencost.prometheus.kubeRBACProxy .Values.opencost.prometheus.bearer_token }}
  {{- fail "Both kubeRBACProxy and bearer_token are set. Please specify only one." -}}
{{- end -}}
{{- end -}}

{{/*
Check that the config is valid
*/}}
{{- define "isPrometheusConfigValid" -}}
  {{- $prometheusModes := add .Values.opencost.prometheus.external.enabled .Values.opencost.prometheus.internal.enabled .Values.opencost.prometheus.amp.enabled | int }}
  {{- if gt $prometheusModes 1 -}}
    {{- fail "Only use one of the prometheus setups: internal, external, or amp" -}}
  {{- end -}}
  {{- if .Values.opencost.prometheus.thanos.enabled -}}
    {{- if and .Values.opencost.prometheus.thanos.external.enabled .Values.opencost.prometheus.thanos.internal.enabled -}}
      {{- fail "Only use one of the thanos setups: internal or external" -}}
    {{- end -}}
  {{- end -}}
{{- end -}}

{{/*
Define opencost config file name
*/}}
{{- define "opencost.configFileName" -}}
  {{- if  eq .Values.opencost.customPricing.provider "custom" -}}
    {{- print "default" -}}
  {{- else -}}
    {{- .Values.opencost.customPricing.provider -}}
  {{- end -}}
{{- end -}}

{{/*
Get api version of networking.k8s.io
*/}}
{{- define "networkingAPIVersion" -}}
{{- if .Capabilities.APIVersions.Has "networking.k8s.io/v1" }}
apiVersion: networking.k8s.io/v1
{{- else if .Capabilities.APIVersions.Has "networking.k8s.io/v1beta1" }}
apiVersion: networking.k8s.io/v1beta1
{{- end }}
{{- end -}}

{{- define "opencost.imageTag" -}}
{{ .Values.opencost.exporter.image.tag | default (printf "%s" .Chart.AppVersion) }}
{{- end -}}

{{- define "opencost.fullImageName" -}}
{{- if .Values.opencost.exporter.image.fullImageName }}
{{- .Values.opencost.exporter.image.fullImageName -}}
{{- else}}
{{- .Values.opencost.exporter.image.registry -}}/{{- .Values.opencost.exporter.image.repository -}}:{{- include "opencost.imageTag" . -}}
{{- end -}}
{{- end -}}

{{- define "opencostUi.imageTag" -}}
{{- .Values.opencost.ui.image.tag | default (printf "%s" .Chart.AppVersion) -}}
{{- end -}}

{{- define "opencostUi.fullImageName" -}}
{{- if .Values.opencost.ui.image.fullImageName }}
{{- .Values.opencost.ui.image.fullImageName -}}
{{- else}}
{{- .Values.opencost.ui.image.registry -}}/{{- .Values.opencost.ui.image.repository -}}:{{- include "opencostUi.imageTag" . -}}
{{- end -}}
{{- end -}}


{{- define "opencost.sccName" -}}
{{include "opencost.fullname" .}}-scc
{{- end -}}

{{- /*
  Compute a checksum based on the rendered content of specific ConfigMaps and Secrets.
*/ -}}
{{- define "configsChecksum" -}}
{{- $files := list
  "configmap-custom-pricing.yaml"
  "configmap-frontend.yaml"
  "configmap-metrics-config.yaml"
  "secret-cloud-integration.yaml"
  "secret.yaml"
  "secret-admin-token.yaml"
-}}
{{- $checksum := "" -}}
{{- range $files -}}
  {{- $content := include (print $.Template.BasePath (printf "/%s" .)) $ -}}
  {{- $checksum = printf "%s%s" $checksum $content | sha256sum -}}
{{- end -}}
{{- $checksum | sha256sum -}}
{{- end -}}

{{- define "opencost.caCertsSecretConfig.check" }}
  {{- if .Values.opencost.updateCaTrust.enabled }}
    {{- if and .Values.opencost.updateCaTrust.caCertsSecret .Values.opencost.updateCaTrust.caCertsConfig }}
      {{- fail "Both caCertsSecret and caCertsConfig are defined. Please specify only one." }}
    {{- else if and (not .Values.opencost.updateCaTrust.caCertsSecret) (not .Values.opencost.updateCaTrust.caCertsConfig) }}
      {{- fail "Neither caCertsSecret nor caCertsConfig is defined, but updateCaTrust is enabled. Please specify one." }}
    {{- end }}
  {{- end }}
{{- end }}
