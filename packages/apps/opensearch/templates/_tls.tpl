{{/*
opensearch.tls.certManagerIssued resolves tls.issuer to "true" or "false" so
callers can use it with `eq`:

  {{- $certManagerIssued := (include "opensearch.tls.certManagerIssued" .) | eq "true" -}}

tls.issuer is tri-state: an explicit value is obeyed, and an absent one follows
external, because a release nobody publishes has no third party to prove
anything to. Both issuers serve TLS; the field selects who signs it.

VERSION FLOOR: the cert-manager issuer requires OpenSearch >= 2.0.0.

The operator picks the CA that signs the securityadmin admin certificate by
cluster version: adminCAName() honours spec.security.tls.http.caSecret only at
2.0.0 and above, and falls back to spec.security.tls.transport.caSecret below
that. This chart leaves the transport CA operator-generated, so on an older
cluster that field is empty and the operator signs the admin certificate with
its own transport CA while the HTTP listener trusts the cert-manager CA. The
two anchors never meet, securityadmin cannot authenticate, and the security
configuration (users, roles, audit policies) never applies, silently.

How the floor is applied depends on who chose the issuer, because the two cases
have opposite correct answers:

  - chosen explicitly (tls.issuer: cert-manager) → fail, loudly. The user asked
    for something this version cannot deliver, and quietly giving them a weaker
    configuration than they asked for would be worse than refusing.

  - resolved from external (tls.issuer unset) → resolve to the operator issuer.
    The user asked for external access, not for a per-release CA. The operator
    issuer is what such a release already runs today, so degrading keeps it
    working instead of breaking it on upgrade.

The comparison runs against the resolved version string rather than the enum,
the same value that goes into spec.general.version, which is the field the
operator itself branches on. So the floor tracks the version mapping, and an
images.opensearch override cannot move it out from under the operator either.

`default dict` guards tls: null, which Helm delivers as nil rather than
coalescing the key back to the values.yaml default. Measured on v4.2.4, through
a values file and through --set alike. `kindIs "invalid"` guards an absent
issuer key, which is the shape the chart ships.
*/}}
{{- define "opensearch.tls.certManagerIssued" -}}
{{- $tlsMap := .Values.tls | default dict -}}
{{- $issuer := index $tlsMap "issuer" -}}
{{- $explicit := not (kindIs "invalid" $issuer) -}}
{{- $resolved := ternary ($issuer | toString) (ternary "cert-manager" "operator" (.Values.external | default false)) $explicit -}}
{{- $requested := eq $resolved "cert-manager" | toString -}}
{{- if and (eq $requested "true") (semverCompare "<2.0.0" (include "opensearch.versionMap" .)) -}}
  {{- if $explicit -}}
    {{- fail (printf "opensearch %s (version: %s) does not support the cert-manager HTTP issuer: below 2.0.0 the operator signs the securityadmin admin certificate with the transport CA, which cannot verify against the cert-manager HTTP CA, so the admin certificate and the HTTP listener would never share a CA. Set tls.issuer: operator to use the operator's own HTTP CA, or use version v2 or later." (include "opensearch.versionMap" .) (.Values.version | default "v2")) -}}
  {{- else -}}
    {{- "false" -}}
  {{- end -}}
{{- else -}}
  {{- $requested -}}
{{- end -}}
{{- end -}}
