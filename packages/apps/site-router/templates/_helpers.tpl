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
Fail-fast validation of the tenant-settable values that flow into VyOS `set`
commands, so a value with an embedded quote/newline/space cannot terminate a
command and inject arbitrary VyOS config (e.g. an attacker's own API key). Run
BEFORE any tenant value is interpolated into a config line.

  - managementCIDR (when non-empty) must be a strict IPv4 CIDR `a.b.c.d/prefix`.
    Go's regexp `$` is end-of-text (not before a trailing newline), so any
    embedded OR trailing newline, and any quote/space, fails the match.
  - peer.address (when non-empty) must be a bare IPv4/IPv6 address or hostname —
    letters, digits, dots, colons, hyphens only. It is NOT interpolated into the
    chart's seed (the controller renders it as a structured op, never a shell
    string), so this is a defence-in-depth reject of a hostile value at the
    earliest point (chart render / apply time).

Empty values are allowed (the fail-closed managementCIDR check and the
peer-not-yet-configured state live elsewhere); this guard only rejects a
present-but-malformed value.
*/}}
{{- define "site-router.assertSafeVyOSInputs" -}}
{{- $mgmt := .Values.managementCIDR | toString -}}
{{- if $mgmt -}}
{{-   if not (regexMatch `^([0-9]{1,3}\.){3}[0-9]{1,3}/[0-9]{1,2}$` $mgmt) -}}
{{-     fail (printf "managementCIDR %q is not a strict IPv4 CIDR (a.b.c.d/prefix); refusing to interpolate it into the VyOS config (command-injection guard)" $mgmt) -}}
{{-   end -}}
{{- end -}}
{{- $peer := "" -}}
{{- if .Values.peer -}}{{- $peer = .Values.peer.address | default "" | toString -}}{{- end -}}
{{- if $peer -}}
{{-   if not (regexMatch `^[A-Za-z0-9.:-]+$` $peer) -}}
{{-     fail (printf "peer.address %q must be a bare IP address or hostname (letters, digits, dots, colons, hyphens only); refusing a value with whitespace/quotes/newlines (command-injection guard)" $peer) -}}
{{-   end -}}
{{- end -}}
{{- end -}}

{{/*
First-boot cloud-init userdata for the VyOS gateway. Ported from the upstream
VyOS-router reference implementation's buildCloudInitUserData: hostname, HTTPS
API key, listen-address, and the fail-closed management firewall — plus the T08
guest security guards (Boundary-A management-API drop for IPsec-decrypted
traffic, forward-chain default-deny), seeded so the router is fail-closed from
first boot until the controller re-stamps the full set. Takes a dict {ctx,
token} so the token is resolved once by the caller and shared with the api-key
Secret.

DELIVERY MECHANISM — READ BEFORE CHANGING (why we write_files a whole config.boot):
We deliver the seed by cloud-init `write_files`, dropping a COMPLETE, VyOS-
serialized config.boot at /opt/vyatta/etc/config/config.boot, because on the
pinned VyOS 1.5-rolling image the cloud-init module set is stripped and neither
obvious alternative runs:
  - `vyos_config_commands:` (the `cc_vyos_userdata` module) IS enabled, but it
    serializes single-value leaf nodes into config.boot as the invalid
    `node { value }` instead of the valid inline `node "value"`, so boot-time
    activation rejects the whole file (pinned empirically on image
    sha256:a3bfc9fe…, iteration 2).
  - `runcmd:` / `bootcmd:` are NOT enabled — `cloud_final_modules` is empty on
    this image, so `scripts-user` never runs (iteration 3).
`write_files` is the one config-capable module actually enabled (cloud_config
stage: vyos_ifupdown, vyos, write_files, vyos_userdata, vyos_install), and it
runs BEFORE the `vyos-router` activation, so the file we drop is what gets
activated (validated hands-off, iteration 4: config activates clean, eth0 DHCPs,
the HTTPS API answers on :443 with the seeded key; the write_files host-name
wins over the baked default's "vyos", proving ours is what activates).

The config.boot base (see `site-router.configBoot`) was captured verbatim from
VyOS's own `save` — the flavor's config.boot.default plus the 23 management
set-commands, committed and saved — so its serialization is correct by
construction. Templating only substitutes host-name, the api-key and
managementCIDR; do NOT hand-edit the tree into `node { value }` form, and do NOT
switch back to `vyos_config_commands:` / `runcmd:` unless you have re-verified
the module set + serializer on the then-pinned image.

IMAGE COUPLING: the trailing `// vyos-config-version:` / `// Release version:`
footer is tied to the exact image build — VyOS runs a config migration (which
can reformat or reject this file) if it does not match. It MUST advance in
lockstep with the pinned image. This is the strongest form of the
"image and cloud-init advance atomically" invariant (docs/image-lifecycle.md):
the whole config.boot, not just a seed, now lives beside the image. When the
image bumps, re-capture config.boot from the new image's `save`.

WHY cloud-init at all (and not controller-only over the API): the controller's
channel is the HTTPS REST API, which can only answer once the gateway already
has an IP, the API running, AND this per-instance api-key installed — it cannot
bootstrap itself (chicken-and-egg). cloud-init NoCloud user-data (delivered as
a per-instance Secret) is the idiomatic KubeVirt channel for that day-0
bootstrap. The only cloud-init-free alternative is qemu-guest-agent guest-exec,
which reopens a remote-exec surface on a deliberately-locked-down appliance (no
SSH, login locked) and needs guest-exec RBAC + the KubeVirt feature gate + a
security review — a deliberate Phase-1 non-goal.

CONFIG-SAFETY: the templated values land inside VyOS `"…"` quotes, so a value
with an embedded quote/newline could break the config.boot. assertSafeVyOSInputs
(called in configBoot) constrains managementCIDR to a strict IPv4 CIDR and
peer.address to [A-Za-z0-9.:-]; the token is randAlphaNum (alphanumeric) and the
host-name is the DNS-label Release.Name — none can carry a quote or newline.
Keep that guard in front of any new interpolated value.

listen-address is 0.0.0.0 because the pod IP is unknown at render time; the
management firewall (only managementCIDR reaches tcp 443 — the HTTPS API;
default-action drop) is the compensating control (D6). SSH (22) is NOT opened:
the appliance ships with no SSH service and the baked login locked, so the pod
network cannot reach a shell. When managementCIDR is empty (only reachable with
allowOpenManagement=true) no firewall is stamped.
*/}}
{{- define "site-router.cloudInitUserData" -}}
{{- $ctx := .ctx -}}
{{- $token := .token -}}
{{- $cfg := include "site-router.configBoot" (dict "ctx" $ctx "token" $token) | trim -}}
#cloud-config
write_files:
  - path: /opt/vyatta/etc/config/config.boot
    owner: root:vyattacfg
    permissions: '0660'
    content: |
{{ $cfg | indent 6 }}
{{- end -}}

{{/*
The complete VyOS config.boot dropped by cloud-init write_files (see the
DELIVERY MECHANISM note above). Captured verbatim from VyOS `save` on the pinned
image (flavor config.boot.default + the 23 management set-commands), so the
curly-brace serialization is correct by construction — only host-name, the
api-key and managementCIDR are substituted. The firewall block is emitted only
when managementCIDR is set (open-management stays "no firewall at all"). Every
baked default here is load-bearing and must be preserved: the locked `vyos`
login (`encrypted-password "*"`) with no `service ssh`, the ttyS0 console, and
the version footer (see IMAGE COUPLING). Keep the firewall rules in lockstep
with internal/vyos/render and the assertions in tests/secret_cloudinit_test.yaml.
*/}}
{{- define "site-router.configBoot" -}}
{{- $ctx := .ctx -}}
{{- $token := .token -}}
{{- include "site-router.assertSafeVyOSInputs" $ctx -}}
{{- if $ctx.Values.managementCIDR }}
firewall {
    ipv4 {
        forward {
            filter {
                default-action "drop"
                rule 5 {
                    action "accept"
                    state "established"
                    state "related"
                }
                rule 10 {
                    action "accept"
                    ipsec {
                        match-none-in
                    }
                }
            }
        }
        input {
            filter {
                default-action "drop"
                rule 1 {
                    action "drop"
                    destination {
                        port "22,443"
                    }
                    ipsec {
                        match-ipsec-in
                    }
                    protocol "tcp"
                }
                rule 5 {
                    action "accept"
                    state "established"
                    state "related"
                }
                rule 10 {
                    action "accept"
                    destination {
                        port "443"
                    }
                    protocol "tcp"
                    source {
                        address "{{ $ctx.Values.managementCIDR }}"
                    }
                }
            }
        }
    }
}
{{- end }}
interfaces {
    ethernet eth0 {
        address "dhcp"
        description "site-router uplink (pod network); managed by cloud-init and the site-router controller"
    }
    loopback lo {
    }
}
service {
    https {
        api {
            keys {
                id site-router-controller {
                    key "{{ $token }}"
                }
            }
            rest {
            }
        }
        listen-address "0.0.0.0"
    }
    ntp {
        allow-client {
            address "127.0.0.0/8"
            address "169.254.0.0/16"
            address "10.0.0.0/8"
            address "172.16.0.0/12"
            address "192.168.0.0/16"
            address "::1/128"
            address "fe80::/10"
            address "fc00::/7"
        }
        server time1.vyos.net {
        }
        server time2.vyos.net {
        }
        server time3.vyos.net {
        }
    }
}
system {
    config-management {
        commit-revisions "100"
    }
    console {
        device ttyS0 {
            speed "115200"
        }
    }
    host-name "{{ $ctx.Release.Name }}"
    login {
        operator-group default {
            command-policy {
                allow "*"
            }
        }
        user vyos {
            authentication {
                encrypted-password "*"
            }
        }
    }
    option {
        reboot-on-upgrade-failure "5"
    }
    syslog {
        local {
            facility all {
                level "info"
            }
            facility local7 {
                level "debug"
            }
        }
    }
}


// Warning: Do not remove the following line.
// vyos-config-version: "bgp@8:broadcast-relay@1:cluster@2:config-management@1:conntrack@6:conntrack-sync@2:container@3:dhcp-relay@2:dhcp-server@11:dhcpv6-server@6:dns-dynamic@4:dns-forwarding@4:firewall@20:flow-accounting@3:https@7:ids@2:interfaces@34:ipoe-server@4:ipsec@14:isis@3:l2tp@9:lldp@3:mdns@1:monitoring@2:nat@8:nat66@3:nhrp@1:ntp@3:openconnect@3:openvpn@5:ospf@2:pim@1:pki@1:policy@9:pppoe-server@12:pptp@5:qos@3:quagga@12:reverse-proxy@3:rip@1:rpki@2:snmp@3:ssh@3:sstp@6:system@33:vpp@6:vrf@4:vrrp@4:vyos-accel-ppp@2:wanloadbalance@4:webproxy@2"
// Release version: 1.5-rolling-20260720
{{- end -}}
