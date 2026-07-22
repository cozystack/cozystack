{{/*
Expand the name of the chart.
*/}}
{{- define "site-router.name" -}}
{{- default .Chart.Name .Values.nameOverride | trunc 63 | trimSuffix "-" }}
{{- end }}

{{/*
Create a default fully qualified app name.
We truncate at 63 chars because some Kubernetes name fields are limited to this (by the DNS naming spec).
If release name contains chart name it will be used as a full name.
*/}}
{{- define "site-router.fullname" -}}
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
{{- define "site-router.chart" -}}
{{- printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" | trunc 63 | trimSuffix "-" }}
{{- end }}

{{/*
Common labels
*/}}
{{- define "site-router.labels" -}}
helm.sh/chart: {{ include "site-router.chart" . }}
{{ include "site-router.selectorLabels" . }}
{{- if .Chart.AppVersion }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
{{- end }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
{{- end }}

{{/*
Selector labels
*/}}
{{- define "site-router.selectorLabels" -}}
app.kubernetes.io/name: {{ include "site-router.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end }}

{{/*
Generate a stable UUID for the gateway VM firmware. cloud-init re-runs only when
the derived UUID changes, i.e. only on an intentional cloudInitSeed change.
Ported from the vm-instance chart's virtual-machine.stableUuid idiom.
*/}}
{{- define "site-router.stableUuid" -}}
{{- $source := printf "%s-%s-%s" .Release.Namespace (include "site-router.fullname" .) .Values.cloudInitSeed }}
{{- $hash := sha256sum $source }}
{{- $uuid := printf "%s-%s-4%s-9%s-%s" (substr 0 8 $hash) (substr 8 12 $hash) (substr 13 16 $hash) (substr 17 20 $hash) (substr 20 32 $hash) }}
{{- if eq .Values.cloudInitSeed "" }}
  {{- /* Preserve the previous UUID so clearing the seed does not re-run cloud-init. */}}
  {{- $vmResource := lookup "kubevirt.io/v1" "VirtualMachine" .Release.Namespace (include "site-router.fullname" .) -}}
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
Resolve the VyOS HTTPS-API token. Reuses the token from the existing api-key
Secret when present (reconcile stability), otherwise generates a fresh one.
Callers MUST resolve the token exactly once per render and reuse the value, so
the api-key Secret and the cloud-init seed never diverge on first install.
*/}}
{{- define "site-router.apiToken" -}}
{{- $existing := lookup "v1" "Secret" .Release.Namespace (printf "%s-api-key" .Release.Name) -}}
{{- if and $existing (hasKey $existing "data") (hasKey $existing.data "token") -}}
{{- index $existing.data "token" | b64dec -}}
{{- else -}}
{{- randAlphaNum 40 -}}
{{- end -}}
{{- end -}}

{{/*
First-boot cloud-init userdata (VyOS `vyos_config_commands`). Ported from
the upstream VyOS-router reference implementation's buildCloudInitUserData:
hostname, HTTPS API key, listen-address, and the fail-closed management
firewall — plus the T08 guest security guards (Boundary-A management-API drop
for IPsec-decrypted traffic, forward-chain default-deny), seeded so the router
is fail-closed from first boot until the controller re-stamps the full set.
Takes a dict {ctx, token} so the token
is resolved once by the caller and shared with the api-key Secret.

listen-address is 0.0.0.0 because the pod IP is unknown at render time; the
management firewall (only managementCIDR reaches tcp 22/443, default-action
drop) is the compensating control (D6). When managementCIDR is empty (only
reachable with allowOpenManagement=true) no firewall is stamped.
*/}}
{{- define "site-router.cloudInitUserData" -}}
{{- $ctx := .ctx -}}
{{- $token := .token -}}
{{- $lines := list "#cloud-config" "vyos_config_commands:" -}}
{{- $lines = append $lines (printf "  - set system host-name '%s'" $ctx.Release.Name) -}}
{{/* R2: bring eth0 up over DHCP and start the HTTPS API /configure REST endpoints. Without an eth0 address the guest has no IP/routes and is unreachable, and `service https api keys` alone does NOT start the REST endpoints — `service https api rest` is required for the controller's /configure + /retrieve calls to answer. Both are validated as required on a cloud-init-capable VyOS image. NOTE: on the currently-referenced image cloud-init IGNORES vyos_config_commands, so these (and the whole seed) are inert there — the conformant-image swap is the T14 image follow-up — but they are mandatory for any image that honours the seed. Kept in lockstep with the base config above. */}}
{{- $lines = append $lines "  - set interfaces ethernet eth0 address dhcp" -}}
{{- $lines = append $lines (printf "  - set service https api keys id site-router-controller key '%s'" $token) -}}
{{- $lines = append $lines "  - set service https api rest" -}}
{{- $lines = append $lines "  - set service https listen-address 0.0.0.0" -}}
{{- if $ctx.Values.managementCIDR -}}
{{/* VyOS 1.5-rolling nftables firewall (validated live against the 2026.05.13-0044-rolling image): the management ACL lives under 'firewall ipv4 input filter', firewall 'state' is a multi-value leaf ('state established' / 'state related', not the old 'state established enable'), and a rule that sets a destination port must also set a protocol. Kept in lockstep with internal/vyos/render. */}}
{{- $lines = append $lines "  - set firewall ipv4 input filter rule 5 action accept" -}}
{{- $lines = append $lines "  - set firewall ipv4 input filter rule 5 state established" -}}
{{- $lines = append $lines "  - set firewall ipv4 input filter rule 5 state related" -}}
{{- $lines = append $lines "  - set firewall ipv4 input filter rule 10 action accept" -}}
{{- $lines = append $lines (printf "  - set firewall ipv4 input filter rule 10 source address '%s'" $ctx.Values.managementCIDR) -}}
{{- $lines = append $lines "  - set firewall ipv4 input filter rule 10 protocol tcp" -}}
{{- $lines = append $lines "  - set firewall ipv4 input filter rule 10 destination port '22,443'" -}}
{{- $lines = append $lines "  - set firewall ipv4 input filter default-action drop" -}}
{{/* T08 guest security guards, seeded fail-closed from first boot BEFORE the controller can reach the router (it re-stamps the full set on its first reconcile). Grouped inside the managementCIDR block so the open-management escape hatch stays "no firewall at all". Boundary A: drop the management ports for IPsec-decrypted traffic — a packet decrypted by VyOS and addressed to the guest's own API does not cross the pod veth where Cilium enforces. §3: forward-chain default-deny (routed mode advertises specific remotes, never a default route out the tunnel). VyOS 1.5: the inbound ipsec matchers are 'match-ipsec-in'/'match-none-in' (bare 'match-ipsec'/'match-none' are ambiguous prefixes) and the drop rule needs an explicit protocol alongside its port; validated live and kept in lockstep with internal/vyos/render. */}}
{{- $lines = append $lines "  - set firewall ipv4 input filter rule 1 ipsec match-ipsec-in" -}}
{{- $lines = append $lines "  - set firewall ipv4 input filter rule 1 protocol tcp" -}}
{{- $lines = append $lines "  - set firewall ipv4 input filter rule 1 destination port '22,443'" -}}
{{- $lines = append $lines "  - set firewall ipv4 input filter rule 1 action drop" -}}
{{- $lines = append $lines "  - set firewall ipv4 forward filter default-action drop" -}}
{{- $lines = append $lines "  - set firewall ipv4 forward filter rule 5 action accept" -}}
{{- $lines = append $lines "  - set firewall ipv4 forward filter rule 5 state established" -}}
{{- $lines = append $lines "  - set firewall ipv4 forward filter rule 5 state related" -}}
{{- $lines = append $lines "  - set firewall ipv4 forward filter rule 10 ipsec match-none-in" -}}
{{- $lines = append $lines "  - set firewall ipv4 forward filter rule 10 action accept" -}}
{{- end -}}
{{- join "\n" $lines -}}
{{- end -}}
