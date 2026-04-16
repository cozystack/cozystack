{{/*
Expand the name of the chart.
*/}}
{{- define "kubernetes.name" -}}
{{- default .Chart.Name .Values.nameOverride | trunc 63 | trimSuffix "-" }}
{{- end }}

{{/*
Create a default fully qualified app name.
We truncate at 63 chars because some Kubernetes name fields are limited to this (by the DNS naming spec).
If release name contains chart name it will be used as a full name.
*/}}
{{- define "kubernetes.fullname" -}}
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
{{- define "kubernetes.chart" -}}
{{- printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" | trunc 63 | trimSuffix "-" }}
{{- end }}

{{/*
Common labels
*/}}
{{- define "kubernetes.labels" -}}
helm.sh/chart: {{ include "kubernetes.chart" . }}
{{ include "kubernetes.selectorLabels" . }}
{{- if .Chart.AppVersion }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
{{- end }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
{{- end }}

{{/*
Selector labels
*/}}
{{- define "kubernetes.selectorLabels" -}}
app.kubernetes.io/name: {{ include "kubernetes.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end }}

{{/*
wait-for-kubeconfig init container shared by the control-plane-side
Deployments (cluster-autoscaler, kccm, kcsi-controller) that mount the
*-admin-kubeconfig Secret provisioned asynchronously by Kamaji. The
Secret volume is declared optional so kubelet does not FailedMount while
Kamaji is still bootstrapping; this container polls the mounted path and
exits only when super-admin.svc appears, which happens after kubelet's
optional-Secret refresh cycle.

The 10m deadline stays strictly below the 15m HelmRelease
Install.Timeout set by cozystack-api for the Kubernetes kind (via the
release.cozystack.io/helm-install-timeout annotation) so the
CrashLoopBackOff surfaces before flux remediation fires and uninstalls
the Cluster CR.

The pinned busybox image in images/busybox.tag points directly at
docker.io by digest (not mirrored to ghcr.io like the other .tag files
here): the payload is a one-shot sh loop, the digest pin makes the
pull immutable, and the cost of maintaining a private mirror of a tiny
upstream image that does not move often is not worth it.

Call site owns the surrounding volumes block; the kubeconfig volume
must exist on the pod and mount at /etc/kubernetes/kubeconfig.
*/}}
{{- define "kubernetes.waitForAdminKubeconfig" -}}
- name: wait-for-kubeconfig
  image: "{{ .Files.Get "images/busybox.tag" | trim }}"
  command:
  - sh
  - -c
  - |
    set -eu
    deadline=$(( $(date +%s) + 600 ))
    until [ -s /etc/kubernetes/kubeconfig/super-admin.svc ]; do
      if [ "$(date +%s)" -ge "$deadline" ]; then
        echo "admin kubeconfig was not provisioned within 10m; exiting so the pod goes CrashLoopBackOff and surfaces in dashboards" >&2
        exit 1
      fi
      echo "waiting for admin kubeconfig (provisioned by Kamaji, visible after kubelet Secret refresh)..."
      sleep 5
    done
  volumeMounts:
  - name: kubeconfig
    mountPath: /etc/kubernetes/kubeconfig
    readOnly: true
{{- end }}
