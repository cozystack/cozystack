{{/*
cozy-lib.tls.caCertSecret renders the canonical CA-only trust-anchor Secret
"<release>.tenant-ca", carrying ONLY ca.crt and never a private key.

Why a dedicated object: the Secrets that already hold ca.crt also hold keys —
cert-manager's "<release>-ca" carries the CA key, the leaf "<release>-tls" the
server key — so any RBAC that grants a tenant ca.crt through them leaks a key
too. This helper emits ca.crt alone, labelled so the tenant reads it through the
core.cozystack.io/tenantsecrets virtual resource the base tenant roles already
grant (never through a raw core/v1 Secret).

Name is "<release>.tenant-ca", NOT "<release>-ca-cert": Percona Server for
MongoDB already claims "<release>-ca-cert" with a key-bearing Secret of its own.

This is the render-time path, for charts that hold the CA PEM as a value. Engines
whose operator mints the CA asynchronously are served instead by the CA-extraction
controller (internal/controller/cacert), which projects the same canonical object
out of whatever Secret the engine produces — so a tenant learns exactly one name.
*/}}

{{/*
Invoked with a single dict argument. A chart holding the CA PEM as a value calls
it like this (there is no production caller yet — this is the pattern one uses):

  {{ include "cozy-lib.tls.caCertSecret" (dict
       "name"      (printf "%s.tenant-ca" .Release.Name)
       "namespace" .Release.Namespace
       "caCert"    $caCertPem
       "labels"    (dict "app.kubernetes.io/instance" .Release.Name)
  ) }}

Parameters:
  - name        (required) Secret name. Convention: "<release>.tenant-ca".
  - caCert      (required) the CA chain in PEM. Must be one or more COMPLETE
                certificate blocks and nothing else; the helper fails closed on a
                private-key header or on any non-certificate bytes.
  - namespace   (optional) metadata.namespace.
  - labels      (optional) extra labels, merged onto the mandatory tenant labels.
  - annotations (optional) extra annotations.
*/}}
{{- define "cozy-lib.tls.caCertSecret" -}}
{{-   if not (kindIs "map" .) -}}
{{-     fail "cozy-lib.tls.caCertSecret: expected a single dict argument" -}}
{{-   end -}}
{{-   $name := default "" .name -}}
{{-   if eq $name "" -}}
{{-     fail "cozy-lib.tls.caCertSecret: name is required" -}}
{{-   end -}}
{{- /* Coerce to string first: an unquoted numeric YAML scalar (caCert: 12345)
       parses to float64, and trim on it would die with a raw Go type error
       instead of this helper's own fail message. (caCert: 0 reports "required",
       not coerced — `default ""` treats 0 as empty; pinned by a test.) */ -}}
{{-   $caCert := printf "%v" (default "" .caCert) -}}
{{-   if eq (trim $caCert) "" -}}
{{-     fail "cozy-lib.tls.caCertSecret: caCert is required and must be a non-empty PEM" -}}
{{-   end -}}
{{- /* Reject private-key material, anchored to the PEM header and case-insensitive
       so it neither false-positives on certificate body text nor misses a
       lowercased header. Stops at "PRIVATE KEY", not the closing dashes, so PGP's
       "-----BEGIN PGP PRIVATE KEY BLOCK-----" is still caught. */ -}}
{{-   if regexMatch "(?i)-----BEGIN [A-Z0-9 ]*PRIVATE KEY" $caCert -}}
{{-     fail "cozy-lib.tls.caCertSecret: caCert must not contain private key material" -}}
{{-   end -}}
{{- /* Require the WHOLE value (\A ... \z) to be complete certificate blocks and
       whitespace, not merely to contain one, because ca.crt is emitted VERBATIM
       below: preamble or trailing bytes (e.g. a raw DER/JWK key wearing no PEM
       header) would otherwise ride into the tenant-readable anchor. This bounds
       block SHAPE only — a Helm template has no x509 parser; the Go CA-extraction
       controller does the full pem.Decode + x509 parse. */ -}}
{{-   if not (regexMatch "(?i)\\A\\s*(-----BEGIN CERTIFICATE-----\\s+[A-Za-z0-9+/=][A-Za-z0-9+/=\\s]*-----END CERTIFICATE-----\\s*)+\\z" $caCert) -}}
{{-     fail "cozy-lib.tls.caCertSecret: caCert must contain a complete PEM certificate block (BEGIN/END CERTIFICATE)" -}}
{{-   end -}}
{{- /* internal.cozystack.io/tenant-ca is the selector an ApplicationDefinition
       matches and the CA-extraction controller stamps, so a helper-rendered
       anchor must carry it to converge with a projected one. tenantresource is
       rendered for shape but the lineage webhook recomputes it on admission. */ -}}
{{-   $labels := merge (dict "internal.cozystack.io/tenant-ca" "true" "internal.cozystack.io/tenantresource" "true") (default (dict) .labels) -}}
apiVersion: v1
kind: Secret
metadata:
  name: {{ $name }}
{{-   with .namespace }}
  namespace: {{ . }}
{{-   end }}
  labels: {{- toYaml $labels | nindent 4 }}
{{-   with .annotations }}
  annotations: {{- toYaml . | nindent 4 }}
{{-   end }}
type: Opaque
stringData:
  ca.crt: {{ $caCert | quote }}
{{- end -}}
