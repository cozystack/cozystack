{{/*
Expand the name of the chart.
*/}}
{{- define "ouroboros.name" -}}
{{- default .Chart.Name .Values.nameOverride | trunc 63 | trimSuffix "-" }}
{{- end }}

{{/*
Fully qualified app name (truncated to 63 chars).
*/}}
{{- define "ouroboros.fullname" -}}
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
Chart label.
*/}}
{{- define "ouroboros.chart" -}}
{{- printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" | trunc 63 | trimSuffix "-" }}
{{- end }}

{{/*
Common labels.
*/}}
{{- define "ouroboros.labels" -}}
helm.sh/chart: {{ include "ouroboros.chart" . }}
{{ include "ouroboros.selectorLabels" . }}
{{- if .Chart.AppVersion }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
{{- end }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
{{- end }}

{{/*
Selector labels for the controller.
*/}}
{{- define "ouroboros.selectorLabels" -}}
app.kubernetes.io/name: {{ include "ouroboros.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
app.kubernetes.io/component: controller
{{- end }}

{{/*
Proxy fullname.
*/}}
{{- define "ouroboros.proxyFullname" -}}
{{ include "ouroboros.fullname" . }}-proxy
{{- end }}

{{/*
Proxy labels.
*/}}
{{- define "ouroboros.proxyLabels" -}}
helm.sh/chart: {{ include "ouroboros.chart" . }}
{{ include "ouroboros.proxySelectorLabels" . }}
{{- if .Chart.AppVersion }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
{{- end }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
{{- end }}

{{/*
Proxy selector labels.
*/}}
{{- define "ouroboros.proxySelectorLabels" -}}
app.kubernetes.io/name: {{ include "ouroboros.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
app.kubernetes.io/component: proxy
{{- end }}

{{/*
DaemonSet (etc-hosts) fullname.
*/}}
{{- define "ouroboros.etcHostsFullname" -}}
{{ include "ouroboros.fullname" . }}-etc-hosts
{{- end }}

{{/*
DaemonSet selector labels.
*/}}
{{- define "ouroboros.etcHostsSelectorLabels" -}}
app.kubernetes.io/name: {{ include "ouroboros.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
app.kubernetes.io/component: etc-hosts
{{- end }}

{{/*
DaemonSet labels.
*/}}
{{- define "ouroboros.etcHostsLabels" -}}
helm.sh/chart: {{ include "ouroboros.chart" . }}
{{ include "ouroboros.etcHostsSelectorLabels" . }}
{{- if .Chart.AppVersion }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
{{- end }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
{{- end }}

{{/*
ServiceAccount name.
*/}}
{{- define "ouroboros.serviceAccountName" -}}
{{- default (include "ouroboros.fullname" .) .Values.serviceAccount.name }}
{{- end }}

{{/*
Image with tag (defaulting to chart appVersion).
*/}}
{{- define "ouroboros.image" -}}
{{ .Values.image.repository }}:{{ .Values.image.tag | default .Chart.AppVersion }}
{{- end }}

{{/*
Cluster DNS domain hint baked into chart-time --proxy-fqdn / --target-host.
Returns the operator-supplied controller.clusterDomain (trailing dot
stripped) when explicitly set, OR an empty string when the operator left
it unset. An empty result tells the deployment template to omit the
chart-time FQDN flag and instead emit --proxy-service-name +
--proxy-service-namespace (controller composes the FQDN at runtime from
the auto-detected /etc/resolv.conf cluster-domain). The platform-agnostic
default — chart works on cluster.local, cozy.local, k8s.example.com, etc.
without operator override.
*/}}
{{- define "ouroboros.clusterDomain" -}}
{{- /* Trim trailing dot if the operator typed an FQDN-style "cluster.local."
       — the proxyFqdn helper appends its own dot, so a literal trailing
       dot in the input would render "...svc.cluster.local.." which CoreDNS
       rejects. */ -}}
{{- with .Values.controller.clusterDomain -}}
{{- trimSuffix "." . -}}
{{- end -}}
{{- end }}

{{- define "ouroboros.proxyFqdn" -}}
{{ include "ouroboros.proxyFullname" . }}.{{ .Release.Namespace }}.svc.{{ include "ouroboros.clusterDomain" . }}.
{{- end }}

{{/*
Single source of truth for the value written into the
'app.kubernetes.io/managed-by' label and matched by the cleanup-hook
selector. Must stay in lockstep with externaldns.ManagedByValue in
internal/externaldns/endpoint.go — pinned at both ends:
  - Go side: TestOwnership_RoundTripsThroughBuild in
    internal/externaldns/ownership_test.go (Build → IsOwnedByOuroboros
    → OwnershipSelector all consistent)
  - chart side: 'cleanup hook selector pins managed-by literal' case
    in charts/ouroboros/tests/cleanup-hook_test.yaml (the selector
    contains exactly 'app.kubernetes.io/managed-by=ouroboros')
A change to one end without the other will trip one of these tests.
*/}}
{{- define "ouroboros.managedByValue" -}}
ouroboros
{{- end }}

{{/*
Effective controller mode. Resolves the legacy etcHosts.enabled boolean
into the new explicit controller.mode value. Returns one of "coredns",
"coredns-import", "etc-hosts", or "external-dns".
*/}}
{{- define "ouroboros.mode" -}}
{{- if .Values.etcHosts.enabled -}}
etc-hosts
{{- else -}}
{{- default "coredns" .Values.controller.mode -}}
{{- end -}}
{{- end }}

{{/*
Namespace where DNSEndpoint CRs are written. Operator override wins;
default is the release namespace.
*/}}
{{- define "ouroboros.externalDnsNamespace" -}}
{{- default .Release.Namespace .Values.externalDns.namespace -}}
{{- end }}
