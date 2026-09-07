{{/*
Helpers for pinning an application's external endpoints to reserved addresses.

The address substrate (cozystack/community#35, implemented by the
address-controller and its per-class drivers) makes an IP a first-class,
namespaced resource: a tenant reserves an `IPAddressClaim`, holds it across
workload rebuilds, and points workloads at it. A workload consumes a
reservation through exactly one annotation on the LoadBalancer Service that
should wear the address; the driver translates it into whatever pin mechanism
its backend has (`metallb.io/loadBalancerIPs` and friends), which tenants must
never write themselves.

These helpers exist so that annotation key is written down once. Every catalog
chart threads the same `externalIPs` values field through them, and the key —
still subject to the naming decision on #35 — changes in one place rather than
in every chart that consumes it. Charts differ only in where the resulting
annotations land: a chart-owned Service for some engines, an operator CR field
for others.

When the substrate is not installed the annotation is inert: the load balancer
keeps auto-assigning as it does today, so a chart may emit it unconditionally.
*/}}

{{/*
The Service annotation naming an IPAddressClaim in the Service's own namespace.
Mirrors ServiceClaimAnnotation in the address-controller's well_known.go, which
is the authority for the value.
*/}}
{{- define "cozy-lib.externalIP.claimAnnotation" -}}
local.sdn.cozystack.io/ip-address-claim
{{- end }}

{{/*
Normalize and validate `.Values.externalIPs` into a map of target -> claim name.

Invoke as:
  {{- $claims := include "cozy-lib.externalIP.claims" (list .Values.externalIPs .Values.external "rw" (list "rw" "ro") $) | fromYaml }}

Arguments, in order:
  - externalIPs   the raw value: a list of {target, claim} entries, or empty
  - external      the chart's external-exposure gate, as a bool
  - defaultTarget the target an entry that omits one selects — the endpoint the
                  chart publishes when `external: true`
  - targets       every target name this chart understands, defaultTarget included
  - $             global scope

Rendering fails on a malformed value rather than silently dropping it: a typo in
a claim name is the difference between the address a customer allow-listed and
some other address, and a claim that never binds is far more visible as an
install-time error than as a Service that quietly came up on the wrong IP.
*/}}
{{- define "cozy-lib.externalIP.claims" }}
{{-   include "cozy-lib.checkInput" . }}
{{-   $externalIPs := index . 0 }}
{{-   $external := index . 1 }}
{{-   $defaultTarget := index . 2 }}
{{-   $targets := index . 3 }}
{{-   $out := dict }}
{{-   if $externalIPs }}
{{-     if not (kindIs "slice" $externalIPs) }}
{{-       fail (printf "externalIPs must be a list of {target, claim} entries, got %s" (kindOf $externalIPs)) }}
{{-     end }}
{{-     if not $external }}
{{-       fail "externalIPs requires external: true — there is no external endpoint to pin an address to otherwise" }}
{{-     end }}
{{-     range $i, $entry := $externalIPs }}
{{-       if not (kindIs "map" $entry) }}
{{-         fail (printf "externalIPs[%d] must be a {target, claim} object, got %s" $i (kindOf $entry)) }}
{{-       end }}
{{-       $claim := $entry.claim | default "" }}
{{-       if not $claim }}
{{-         fail (printf "externalIPs[%d] has no claim: every entry must name an IPAddressClaim in this namespace" $i) }}
{{-       end }}
{{-       $target := $entry.target | default $defaultTarget }}
{{-       if not (has $target $targets) }}
{{-         fail (printf "externalIPs[%d] targets %q, which this application does not publish; valid targets: %s" $i $target (join ", " $targets)) }}
{{-       end }}
{{-       if hasKey $out $target }}
{{-         fail (printf "externalIPs[%d] targets %q a second time: one endpoint wears one address, so use one entry per target" $i $target) }}
{{-       end }}
{{-       $_ := set $out $target $claim }}
{{-     end }}
{{-   end }}
{{-   toYaml $out }}
{{- end }}

{{/*
Render the consumption annotation for one target, or nothing when that target
has no reserved address.

Invoke as:
  metadata:
    annotations:
      {{- include "cozy-lib.externalIP.annotations" (list $claims "rw" $) | nindent 6 }}

where $claims is the map returned by cozy-lib.externalIP.claims. Emitting
nothing (rather than an empty map) keeps the annotation out of charts that pass
it into an operator CR, where an empty object is not always the same as an
absent one.
*/}}
{{- define "cozy-lib.externalIP.annotations" }}
{{-   include "cozy-lib.checkInput" . }}
{{-   $claims := index . 0 }}
{{-   $target := index . 1 }}
{{-   with (index ($claims | default dict) $target) }}
{{      include "cozy-lib.externalIP.claimAnnotation" $ }}: {{ . | quote }}
{{-   end }}
{{- end }}
