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
Domain resources (cpu, memory) as a JSON object.
Used in vm.yaml for rendering and in the update hook for merge patches.
*/}}
{{- define "virtual-machine.domainResources" -}}
{{- $result := dict -}}
{{- if or .Values.cpuModel (and .Values.resources .Values.resources.cpu .Values.resources.sockets) -}}
  {{- $cpu := dict -}}
  {{- if and .Values.resources .Values.resources.cpu .Values.resources.sockets -}}
    {{- $_ := set $cpu "cores" (.Values.resources.cpu | int64) -}}
    {{- $_ := set $cpu "sockets" (.Values.resources.sockets | int64) -}}
  {{- end -}}
  {{- if .Values.cpuModel -}}
    {{- $_ := set $cpu "model" .Values.cpuModel -}}
  {{- end -}}
  {{- $_ := set $result "cpu" $cpu -}}
{{- end -}}
{{- if and .Values.resources .Values.resources.memory -}}
  {{- $_ := set $result "resources" (dict "requests" (dict "memory" .Values.resources.memory)) -}}
{{- end -}}
{{- $result | toJson -}}
{{- end -}}

{{/*
Instance type actually rendered into the VM spec, empty when the VM is sized by
explicit resources instead.

KubeVirt v1.9.0 refuses a VirtualMachine that carries an instancetype matcher
together with explicit domain sizing, and the two halves of that rule are not
the same shape. validateCPU in pkg/instancetype/apply/cpu.go conflicts on
domain.cpu.cores and domain.cpu.sockets only when they are non-zero;
validateMemory in memory.go conflicts on domain.resources.requests.memory
whenever the key is present, zero included. A resources block that supplies
all three therefore replaces the matcher, the same way the kubernetes-nodes
chart already sizes its worker VMs. The zero guard on the cpu half is why a
cores of 0 next to a matcher renders instead of failing: KubeVirt does not
call it a conflict, so the instance type simply sizes the VM.

Everything below is decided from what domainResources actually emitted, never
from which keys the user set. The two readings differ, and only the first one
is the question KubeVirt asks. domainResources needs cpu and sockets together
before it writes domain.cpu, so resources.cpu on its own emits nothing and
conflicts with nothing; those releases render today and must keep rendering.
It also puts cpu and sockets through int64, which yields 0 for a quantity the
schema accepts, so resources.cpu of 500m emits a domain.cpu.cores of 0 that
sizes nothing while looking set. Reading the emitted values catches that one
alongside a plain unset field, and it survives a resources block the user nulls
outright, which Helm coalescing turns into a nil .Values.resources that a raw
field read would dereference. cpuModel writes domain.cpu.model, which
conflicts only when the instance type declares a model of its own, so it is
deliberately not among the three fields read back here.

A block that emits some sizing but not all of it has no safe reading next to an
instance type: dropping the matcher would size the VM from fields the user
never finished, and keeping it either loses to the webhook or silently discards
what was typed. Stop the render and name both sides instead.
*/}}
{{- define "virtual-machine.effectiveInstanceType" -}}
{{- $domain := include "virtual-machine.domainResources" . | fromJson -}}
{{- $sized := list -}}
{{- $unsized := list -}}
{{- if dig "cpu" "cores" nil $domain -}}
  {{- $sized = append $sized "resources.cpu" -}}
{{- else -}}
  {{- $unsized = append $unsized "resources.cpu" -}}
{{- end -}}
{{- if dig "cpu" "sockets" nil $domain -}}
  {{- $sized = append $sized "resources.sockets" -}}
{{- else -}}
  {{- $unsized = append $unsized "resources.sockets" -}}
{{- end -}}
{{- if dig "resources" "requests" "memory" nil $domain -}}
  {{- $sized = append $sized "resources.memory" -}}
{{- else -}}
  {{- $unsized = append $unsized "resources.memory" -}}
{{- end -}}
{{- if empty $unsized -}}
  {{- /* Sized by resources, so no matcher is rendered and nothing conflicts. */ -}}
{{- else if and $sized .Values.instanceType -}}
  {{- fail (printf "instanceType %q cannot be combined with a resources block that sizes the VM only in part. Sizing came from %s, but not from %s. resources.cpu and resources.sockets size the VM only when both are set to whole numbers. KubeVirt rejects a VirtualMachine that carries an instancetype matcher together with explicit domain cpu or memory, so either give resources.cpu, resources.sockets and resources.memory values that all size the VM, or clear %s and let the instance type size it." .Values.instanceType (join ", " $sized) (join ", " $unsized) (join ", " $sized)) -}}
{{- else -}}
  {{- /* An explicit instanceType: null survives the merge as a nil, which Go
         renders as the truthy literal <no value>: a name no lookup matches. */ -}}
  {{- .Values.instanceType | default "" -}}
{{- end -}}
{{- end -}}

{{/*
Node Affinity for Windows VMs
*/}}
{{- define "virtual-machine.nodeAffinity" -}}
{{- if .Values._cluster.scheduling -}}
{{- $dedicatedNodesForWindowsVMs := get .Values._cluster.scheduling "dedicatedNodesForWindowsVMs" -}}
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
