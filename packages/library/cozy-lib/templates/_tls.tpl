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
cozy-lib.tls.validateCACert checks that a CA PEM value carries complete
certificate blocks and nothing else, and renders nothing. Every chart value
that accepts a CA in PEM form routes through here, so the fail-closed rules
live in exactly one place.

Invoked with a single dict argument:

  {{ include "cozy-lib.tls.validateCACert" (dict
       "caCert" $caCertPem
       "field"  "talos.imageFactoryCA"
  ) }}

Parameters:
  - caCert (required) the CA chain in PEM.
  - field  (required) the caller's field name, carried into the fail messages
           so the operator sees the value they actually set rather than this
           helper's parameter.

Emptiness is the caller's decision, not this helper's: one caller treats empty
as "unset" and never calls, another fails it with its own "required" message
first. What this helper owns is the shape of a value that IS supplied.

The checks bound block SHAPE only — a Helm template has no x509 parser, so a
body spelled in base64 characters passes however meaningless. The Go
CA-extraction controller (internal/controller/cacert) does the full pem.Decode
+ x509 parse on its path.
*/}}
{{- define "cozy-lib.tls.validateCACert" -}}
{{-   if not (kindIs "map" .) -}}
{{-     fail "cozy-lib.tls.validateCACert: expected a single dict argument" -}}
{{-   end -}}
{{-   $field := printf "%v" (default "" .field) -}}
{{-   if eq $field "" -}}
{{-     fail "cozy-lib.tls.validateCACert: field is required" -}}
{{-   end -}}
{{- /* Coerce to string first: an unquoted numeric YAML scalar (caCert: 12345)
       parses to float64, and regexMatch on it would die with a raw Go type
       error instead of this helper's own fail message. (caCert: 0 is treated
       as empty by `default ""` and fails the full-PEM check below.) */ -}}
{{-   $caCert := printf "%v" (default "" .caCert) -}}
{{- /* Reject private-key material, anchored to the PEM header and case-insensitive
       so it neither false-positives on certificate body text nor misses a
       lowercased header. Stops at "PRIVATE KEY", not the closing dashes, so PGP's
       "-----BEGIN PGP PRIVATE KEY BLOCK-----" is still caught. */ -}}
{{-   if regexMatch "(?i)-----BEGIN [A-Z0-9 ]*PRIVATE KEY" $caCert -}}
{{-     fail (printf "cozy-lib.tls.validateCACert: %s must not contain private key material" $field) -}}
{{-   end -}}
{{- /* Require the WHOLE value (\A ... \z) to be complete certificate blocks and
       whitespace, not merely to contain one, because callers emit the value
       VERBATIM into an object readable by anything that can read the namespace:
       preamble or trailing bytes (e.g. a raw DER/JWK key wearing no PEM
       header) would otherwise ride along. */ -}}
{{-   if not (regexMatch "(?i)\\A\\s*(-----BEGIN CERTIFICATE-----\\s+[A-Za-z0-9+/=][A-Za-z0-9+/=\\s]*-----END CERTIFICATE-----\\s*)+\\z" $caCert) -}}
{{-     fail (printf "cozy-lib.tls.validateCACert: %s must contain a complete PEM certificate block (BEGIN/END CERTIFICATE)" $field) -}}
{{-   end -}}
{{- end -}}

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
                private-key header or on any non-certificate bytes (the shared
                cozy-lib.tls.validateCACert checks).
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
{{-   include "cozy-lib.tls.validateCACert" (dict "caCert" $caCert "field" "caCert") -}}
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
