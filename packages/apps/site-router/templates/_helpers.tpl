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
{{- /* Not `eq .Values.cloudInitSeed ""`: a user-supplied `cloudInitSeed: null`
       is deleted by Helm's coalesce along with the chart default, and eq on the
       resulting nil fails the render rather than reading as the empty seed it
       plainly is. */}}
{{- if not (.Values.cloudInitSeed | default "") }}
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
Resolve the management CIDR: the source allowed to reach the VyOS HTTPS API
through the first-boot firewall.

The default lives here rather than in values.yaml because `_`-prefixed keys are
not @params and cozyvalues-gen rejects a values key it has no schema for. That
turns out to be the better place for it anyway, because it is the only place
that can tell UNSET from EXPLICITLY EMPTY — and the difference is the whole
fail-closed contract. Unset means "use the cluster pod CIDR". Empty means the
operator is asking for the open-management escape hatch, which secret-cloudinit
then refuses unless `_allowOpenManagement` says so out loud.

`| default` would collapse the two and quietly make the escape hatch
unreachable, so this uses hasKey instead.

Unset does NOT mean the literal kube-ovn default. It means "ask the cluster":
the same cozy-system/cozystack ConfigMap key the deny-set already reads
(denyset.ConfigMapKeyPodCIDR). Taking the platform value off the tenant surface
otherwise left nobody able to set it — the cozystack-values Secrets carry only
_cluster and _namespace, and _cluster has no pod-CIDR key — so on a cluster with
a non-default networking.podCIDR the seeded management rule would have excluded
the controller's real pod IP and locked it out of the router permanently. Reading
it is better than restoring the knob anyway: it removes the drift-locks-out-the-
controller footgun rather than moving who can trip it.

The literal stays as the last fallback, for an offline render (helm template,
helm-unittest) where lookup returns nothing, and for a cluster whose ConfigMap
omits the key. The controller's own --management-cidr flag is still set
separately and must still agree; see followups.md.
*/}}
{{- define "site-router.managementCIDR" -}}
{{- if hasKey .Values "_managementCIDR" -}}
{{- .Values._managementCIDR | toString -}}
{{- else -}}
{{- $cm := lookup "v1" "ConfigMap" "cozy-system" "cozystack" -}}
{{- $discovered := "" -}}
{{- if and $cm $cm.data -}}
{{- $discovered = (index $cm.data "ipv4-pod-cidr") | default "" -}}
{{- end -}}
{{- $discovered | default "10.244.0.0/16" -}}
{{- end -}}
{{- end -}}

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
Resolve the TLS material the VyOS HTTPS API presents, and that the controller
pins to. Same lookup-preserve shape as site-router.apiToken and for the same
reason: regenerating on every render would rotate the certificate out from under
a controller that has already pinned it.

Returns a YAML mapping (callers do `include ... | fromYaml`) with the PEM cert,
the PEM key, and the name the certificate is issued for.

WHY A FIXED NAME AND NOT THE ADDRESS. The controller dials the gateway by pod IP,
which nothing can know at render time, so the certificate names a stable
per-instance identity instead and the controller asks for exactly that name while
dialling the address. Per-instance rather than shared, so one instance's
certificate does not authenticate another's gateway.

The name is carried in the Secret rather than recomputed on the Go side: two
copies of a string-formatting rule are two things that can drift, and the failure
mode of drift here is a TLS error nobody expects.
*/}}
{{- define "site-router.apiTLS" -}}
{{- $ns := .Release.Namespace -}}
{{- $secret := printf "%s-api-key" .Release.Name -}}
{{- $serverName := printf "%s.%s.site-router.cozystack.internal" .Release.Name $ns -}}
{{- $existing := lookup "v1" "Secret" $ns $secret -}}
{{- if and $existing (hasKey $existing "data") (hasKey $existing.data "tls.crt") (hasKey $existing.data "tls.key") -}}
{{- dict "cert" (index $existing.data "tls.crt" | b64dec) "key" (index $existing.data "tls.key" | b64dec) "serverName" $serverName | toYaml -}}
{{- else -}}
{{- /* EC, and not by preference. VyOS stores a private key as bare base64 and
       rebuilds the PEM itself on read (vyos/pki.py load_private_key), trying
       exactly two armours: PKCS#8 `PRIVATE KEY` and SEC1 `EC PRIVATE KEY`.
       sprig's genSelfSignedCert is RSA and emits PKCS#1 `RSA PRIVATE KEY`, which
       neither wrap parses, so the commit dies at src/conf_mode/pki.py with
       "Invalid private key on certificate" and the gateway boots with no
       configuration at all. Measured on a real appliance boot, not deduced.
       genPrivateKey "ecdsa" emits SEC1, which is the second armour VyOS tries,
       and genSelfSignedCertWithKey issues a matching certificate for it. */ -}}
{{- $key := genPrivateKey "ecdsa" -}}
{{- $gen := genSelfSignedCertWithKey $serverName nil (list $serverName) 3650 $key -}}
{{- dict "cert" $gen.Cert "key" $key "serverName" $serverName | toYaml -}}
{{- end -}}
{{- end -}}

{{/*
Strip a PEM block down to the bare base64 body VyOS's `pki` nodes expect: their
constraint is a base64 validator, so the armour lines and the newlines have to
go. Takes the PEM string, returns one unbroken line.
*/}}
{{- define "site-router.pemBody" -}}
{{- /* Not a pipeline: sprig's regexReplaceAll takes (regex, input, replacement),
       so `. | regexReplaceAll "re" ""` binds the input to the REPLACEMENT and
       silently yields an empty string. */ -}}
{{- $bare := regexReplaceAll "-----[A-Z ]+-----" . "" -}}
{{- regexReplaceAll "[[:space:]]+" $bare "" -}}
{{- end -}}

{{/*
Build the CDI `source.registry.url` for the appliance boot disk from the stamped
reference in images/vyos-router-disk.tag, passed in as a string.

THE TAG IS DROPPED, THE DIGEST IS KEPT. CDI's docker:// transport cannot parse a
reference that carries both, and rejects it outright:

    Could not parse image: Docker references with both a tag and digest
    are currently not supported

which surfaces as a DataVolume stuck in ImportInProgress with printableStatus
DataVolumeError and an importer that restarts forever — never as a bad
reference. Nothing is lost by dropping the tag: the digest is what pins the
appliance, and the tag only mirrors information the digest already carries.

The stamp itself stays `<repo>:<tag>@sha256:<digest>` — that shape is what
hack/lib/image-refs.sh greps for, and the promote, retag and mirror tooling all
read it from there (docs/agents/image-refs.md), so normalising the stamp would
desync them. This chart is the only consumer in the tree that feeds a stamped
ref into a CDI `source.registry`, so the fix belongs here, at the point of
consumption. Every other consumer keeps the full stamp: kubelet parses
tag+digest happily, and the e2e bring-up of remote-site B applies the same
drop-the-tag normalisation in shell (hack/e2e-chainsaw/site-router).

A reference with no digest is REJECTED rather than passed through. The committed
default is the digest-less `…/vyos-router-disk:v0.0.0` placeholder, which no
build path in this repo ever publishes (see docs/image-lifecycle.md): rendering
it produces a DataVolume that imports nothing for minutes and leaves the gateway
on a VM that never boots, so the render is failed here instead, naming the cause
and the remedy. Consequence worth knowing: while the placeholder is committed,
the chart does not render in-tree at all — which is why tests/dv_test.yaml
asserts the guard and hack/site-router-appliance-ref.bats asserts the rendered
URL against a stamped reference.
*/}}
{{- define "site-router.applianceDiskUrl" -}}
{{- $ref := . | trim -}}
{{- if not $ref -}}
{{-   fail "empty images/vyos-router-disk.tag: the VyOS appliance containerDisk has not been stamped by a build — install a Cozystack build whose CI stamped it, or stamp it locally with `make -C packages/system/vyos-router-image image`" -}}
{{- end -}}
{{- $parts := splitList "@" $ref -}}
{{- if eq (len $parts) 1 -}}
{{-   fail (printf "images/vyos-router-disk.tag holds %q, which carries no @sha256: digest: the VyOS appliance containerDisk has not been stamped by a build, and CDI cannot import an unpinned appliance — install a Cozystack build whose CI stamped it, or stamp it locally with `make -C packages/system/vyos-router-image image`" $ref) -}}
{{- end -}}
{{- $digest := index $parts 1 -}}
{{- if or (ne (len $parts) 2) (not (regexMatch `^sha256:[0-9a-f]{64}$` $digest)) -}}
{{-   fail (printf "images/vyos-router-disk.tag holds %q, which is not a digest-pinned image reference (<repo>[:<tag>]@sha256:<64 hex digits>) — re-stamp it with `make -C packages/system/vyos-router-image image` or install a Cozystack build whose CI stamped it" $ref) -}}
{{- end -}}
{{- /*
  Strip a trailing tag from the repository. The tag is the last `:`-separated
  component of the last path segment, so the pattern requires the run after the
  `:` to be free of `/` — that is what keeps a registry port (`host:5000/repo`,
  no tag) from being mistaken for one.
*/ -}}
{{- $repo := regexReplaceAll `:[^:/]+$` (index $parts 0) "" -}}
{{- printf "docker://%s@%s" $repo $digest -}}
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
{{- $mgmt := include "site-router.managementCIDR" . -}}
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
{{- include "site-router.configBoot" (dict "ctx" .ctx "token" .token "tls" .tls) | trim }}
{{- end -}}

{{- /*
The diagnostics payload, carried on its own disk (see vm.yaml) and installed by
the appliance seed. Rendered only when `_logSerialConsole` asks for it.

Its own disk, rather than more files beside the configuration, so a diagnostics
change cannot affect the configuration seed. That property is worth keeping from
the cloud-init design this replaces, where the two travelled as independent
`write_files` entries: the config.boot is captured verbatim from VyOS `save`, and
a payload that breaks it leaves the guest unconfigured and unreachable, which is
the very outage the diagnostics exist to explain.

cron rather than a systemd unit or the VyOS task-scheduler: cron needs no daemon
reload to pick up a new file, and unlike the task-scheduler it does not depend on
the configuration having committed, so it still reports when the commit is what
failed.
*/ -}}
{{- define "site-router.diagFiles" -}}
{{- $ctx := .ctx -}}
{{- $diag := $ctx.Files.Get "files/guest-diag.sh" | trimSuffix "\n" }}
{{- /*
Fail the render rather than ship an empty script. .Files.Get returns "" for a
path that is not in the PACKAGED chart, so a .helmignore that grows a /files
entry would install a cron job pointing at an empty file: the guest would boot,
cron would fire every minute, and the console would carry nothing but kernel
output, which is the exact silence this exists to end, with every render and test
still green. Same fail-loud posture as site-router.applianceDiskUrl.
*/ -}}
{{- if not $diag }}
{{- fail "files/guest-diag.sh is empty or missing from the packaged chart, so _logSerialConsole would install a cron job with no script; check .helmignore" }}
{{- end }}
guest-diag.sh: |
{{ $diag | indent 2 }}
guest-diag.cron: |
  # Managed by the cozystack site-router chart (values._logSerialConsole).
  #
  # The appliance seed installs this as /etc/cron.d/cozy-guest-diag. That name
  # carries no dot ON PURPOSE: Debian cron silently ignores /etc/cron.d entries
  # whose names contain anything other than letters, digits, underscore and
  # hyphen, so a dotted name would install cleanly and never run.
  #
  # PATH is set explicitly because cron's default is /usr/bin:/bin, and the
  # things worth reading here, systemctl and ss and swanctl, live in sbin.
  # Without this every one of those fields would report a bare "not found".
  SHELL=/bin/sh
  PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin
  * * * * * root /config/scripts/cozy-guest-diag.sh
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
{{- $tls := .tls -}}
{{- include "site-router.assertSafeVyOSInputs" $ctx -}}
{{- if include "site-router.managementCIDR" $ctx }}
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
                        address "{{ include "site-router.managementCIDR" $ctx }}"
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
pki {
    certificate site-router-api {
        certificate "{{ include "site-router.pemBody" $tls.cert }}"
        private {
            key "{{ include "site-router.pemBody" $tls.key }}"
        }
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
        certificates {
            certificate "site-router-api"
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
// vyos-config-version: "bgp@6:cluster@2:config-management@1:conntrack@6:conntrack-sync@2:container@3:dhcp-relay@2:dhcp-server@11:dhcpv6-server@6:dns-dynamic@4:dns-forwarding@4:firewall@20:flow-accounting@3:https@7:ids@2:interfaces@34:ipoe-server@4:ipsec@14:isis@3:l2tp@9:lldp@3:monitoring@2:nat@8:nat66@3:nhrp@1:ntp@3:openconnect@3:openvpn@5:ospf@2:pim@1:pki@1:policy@8:pppoe-server@12:pptp@5:qos@2:quagga@12:reverse-proxy@3:rip@1:rpki@2:salt@1:snmp@3:ssh@3:sstp@6:system@31:vpp@6:vrf@4:vrrp@4:wanloadbalance@4:webproxy@2"
// Release version: 2026.03
{{- end -}}
