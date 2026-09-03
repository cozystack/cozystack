{{/*
opensearch.tls.enabled resolves the tri-state tls.enabled field to a plain
string "true" or "false" so callers can use it with `eq`:

  {{- $tlsEnabled := (include "opensearch.tls.enabled" .) | eq "true" -}}

Tri-state semantics:
  - tls.enabled explicitly set  → use that value
  - tls.enabled unset (null)    → auto-on when external is true

VERSION FLOOR: chart-managed HTTP TLS requires OpenSearch >= 2.0.0.

The operator picks the CA that signs the securityadmin admin certificate by
cluster version: adminCAName() honours spec.security.tls.http.caSecret only at
2.0.0 and above, and falls back to spec.security.tls.transport.caSecret below
that. This chart leaves the transport CA operator-generated, so on an older
cluster that field is empty and the operator signs the admin certificate with
its own transport CA while the HTTP listener trusts the cert-manager CA. The
two anchors never meet, securityadmin cannot authenticate, and the security
configuration (users, roles, audit policies) never applies — silently.

How the floor is applied depends on who asked for TLS, because the two cases
have opposite correct answers:

  - explicitly requested (tls.enabled: true) → fail, loudly. The user asked
    for something this version cannot deliver, and quietly giving them a
    weaker configuration than they asked for would be worse than refusing.

  - auto-on (tls.enabled unset, external: true) → resolve to false. The user
    asked for external access, not for chart-managed TLS. Operator-managed
    HTTP TLS is what such a release already runs today, so degrading keeps it
    working instead of breaking it on upgrade.

The comparison runs against the resolved version string rather than the enum —
the same value that goes into spec.general.version, which is the field the
operator itself branches on. So the floor tracks the version mapping, and an
images.opensearch override cannot move it out from under the operator either.
*/}}
{{- define "opensearch.tls.enabled" -}}
{{- /*
This default is not a null guard. `tls: {}` in values.yaml is EMPTY, so `default` fires
on the ordinary path as well, and neither shape needs it in order to render: a field
read through nil yields no value exactly as it does through an empty dict, so `enabled`
comes back invalid either way and the tri-state below falls through to `external`.

What it buys is that $tls is always a dict, so an edit reaching for `hasKey` or
`index`, both of which DO error on nil, cannot be broken by an input the platform
never sends but a hand-written values file can. That input is real: an explicit
`tls: null` arrives here as nil, because Helm deletes the key rather than coalescing
it back to the values.yaml default. Measured on v4.2.4, through a values file and
through --set alike.
*/ -}}
{{- $tls := .Values.tls | default dict -}}
{{- $explicit := not (kindIs "invalid" $tls.enabled) -}}
{{- $requested := ternary ($tls.enabled | toString) (.Values.external | default false | toString) $explicit -}}
{{- if and (eq $requested "true") (semverCompare "<2.0.0" (include "opensearch.versionMap" .)) -}}
  {{- if $explicit -}}
    {{- fail (printf "opensearch %s (version: %s) does not support chart-managed HTTP TLS: below 2.0.0 the operator signs the securityadmin admin certificate with the transport CA, which cannot verify against the cert-manager HTTP CA, so the admin certificate and the HTTP listener would never share a CA. Set tls.enabled: false to use operator-managed HTTP TLS, or use version v2 or later." (include "opensearch.versionMap" .) (.Values.version | default "v2")) -}}
  {{- else -}}
    {{- "false" -}}
  {{- end -}}
{{- else -}}
  {{- $requested -}}
{{- end -}}
{{- end -}}
