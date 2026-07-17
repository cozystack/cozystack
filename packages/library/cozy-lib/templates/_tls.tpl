{{/*
=============================================================================
 TLS trust-anchor helpers
=============================================================================

Per-app TLS in the Cozystack catalog issues a per-release, self-signed CA (via
cert-manager or the app operator). A tenant that connects to such an endpoint
needs the CA certificate (ca.crt) to verify the server — but nothing more. The
objects that hold ca.crt today also hold private keys:

  - the cert-manager CA secret  <release>-ca   carries the CA private key
    (tls.key) — full trust-chain compromise if leaked;
  - the cert-manager leaf secret <release>-tls  carries the server private key
    (tls.key) — server impersonation / MITM if leaked.

So any RBAC path that hands a tenant ca.crt by granting read on one of those
Secrets also hands over a private key. cozy-lib.tls.caCertSecret breaks that
coupling: it renders a canonical trust-anchor object that carries ONLY ca.crt,
labelled so tenants reach it through the core.cozystack.io/tenantsecrets API
that the base tenant roles already grant — never through a Secret that contains
tls.key.

Delivery mechanism (grounded in the platform RBAC, not invented here):

  - pkg/registry/core/tenantsecret/rest.go surfaces, under the virtual resource
    core.cozystack.io/tenantsecrets, exactly the namespace Secrets labelled
    internal.cozystack.io/tenantresource=true (TenantResourceLabelKey /
    TenantResourceLabelValue in pkg/apis/core/v1alpha1/tenantresource_types.go).
  - packages/system/cozystack-basics/templates/clusterroles.yaml grants
    get/list/watch on core.cozystack.io/tenantsecrets to tenant ServiceAccounts
    (cozy:tenant:base) and to use/admin/super-admin subjects (cozy:tenant:use:
    base). No grant on raw core/v1 secrets is involved, so attaching the label
    to a key-free object exposes the trust anchor without exposing any key.
  - internal/backupcontroller/credentials_projector.go relies on the same rule
    in reverse: it deliberately OMITS the label so its key-bearing projection
    is NOT promoted to a TenantSecret. This helper is the positive counterpart —
    a key-FREE object that is safe to promote.

This is the chart-side shape, for charts that hold the CA PEM as a value at
render time. It is NOT the general mechanism: most engines do not, because their
operator mints the CA asynchronously, long after the chart is rendered. Those are
served by the CA-extraction controller (internal/controller/cacert), which
projects the trust anchor out of whatever Secret the engine actually produces,
without the chart having to see the PEM at all. The helper and the controller
converge on the SAME canonical object, so a tenant learns exactly one name.

Two things follow, and both are easy to get wrong:

  - The canonical name is "<release>.tenant-ca", NOT "<release>-ca-cert".
    "<release>-ca-cert" is not a free name: Percona Server for MongoDB creates a
    Secret of exactly that name itself, and it carries a PRIVATE KEY. A chart
    that rendered a trust anchor there would be writing over live key material.
  - The internal.cozystack.io/tenantresource label below does not, on its own,
    grant a tenant anything. The lineage admission webhook recomputes that label
    from the ApplicationDefinition's spec.secrets selectors and overwrites
    whatever the chart wrote. The label here is the correct shape to render, but
    the read access comes from the definition selecting the object — not from
    this line.

    The overwrite does happen for a chart-rendered anchor, because Flux CREATEs
    the Secret and the webhook always sees a CREATE. It is not a standing
    guarantee though: the webhook carries objectSelector
    internal.cozystack.io/managed-by-cozystack DoesNotExist and stamps that
    marker with its verdict, so it sees each object once and later UPDATEs are
    skipped. Do not read "the webhook will fix it" as true of anything but the
    first admission.

Population of ca.crt stays the responsibility of whatever owns the PKI (the app
operator, as the redis-operator fork does with its CA-only Opaque Secret, or a
cert-manager chain resolved at the chart level). The helper itself is pure and
value-driven so it renders deterministically and refuses, by construction and by
guard, to carry a private key.
*/}}

{{/*
cozy-lib.tls.caCertSecret renders the canonical CA-only trust-anchor Secret.

Invoked with a single dict argument (named parameters, not the (arg, $) list
form, because it needs no global scope):

  {{ include "cozy-lib.tls.caCertSecret" (dict
       "name"      (printf "%s.tenant-ca" .Release.Name)
       "namespace" .Release.Namespace
       "caCert"    $caCertPem
       "labels"    (dict "app.kubernetes.io/instance" .Release.Name)
  ) }}

Parameters:
  - name      (required) Secret name. Convention: "<release>.tenant-ca" — the
              same canonical name the CA-extraction controller publishes, and
              deliberately not "<release>-ca-cert", which Percona Server for
              MongoDB already claims with a key-BEARING Secret.
  - caCert    (required) the CA certificate chain in PEM. Must contain a
              COMPLETE certificate block — BEGIN CERTIFICATE, a base64 body, END
              CERTIFICATE — and must NOT contain a BEGIN...PRIVATE KEY PEM
              header; the helper fails closed on either violation. Both checks
              are anchored to the PEM header line and are case-insensitive, so
              they neither false-positive on certificate body/comment text nor
              miss a lowercased hand-pasted header. The block check cannot go
              further and confirm the body decodes to a certificate: no x509
              parser is reachable from a Helm template. See the guard itself for
              why, and for why the controller-side guard is the one that parses.
  - namespace (optional) metadata.namespace.
  - labels    (optional) extra labels merged onto the mandatory
              internal.cozystack.io/tenantresource label (which always wins here
              — though see the header: the lineage webhook recomputes that label
              from the ApplicationDefinition when it admits the object, so
              rendering it is necessary but not sufficient for tenant read
              access).
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
{{- /* Coerce BEFORE any string operation. An unquoted numeric YAML scalar
       (caCert: 12345) parses to a float64, and trim on a non-string dies with a
       raw Go template type error rather than this helper's own fail message —
       fail-closed, but unreadable. The printf turns it back into something the
       guards can read, so the author is told which field is wrong.

       It does NOT rescue caCert: 0, and the ordering is why: `default ""` runs
       first and sprig treats 0 as empty, so printf receives "" and the helper
       reports "required" for a value that was supplied. That is a wart, not a
       hole — 0 is not a certificate and fails closed either way — and it is
       pinned by a test rather than left as a surprise. Swapping the order to
       rescue it would make `default` useless for a genuinely absent value. */ -}}
{{-   $caCert := printf "%v" (default "" .caCert) -}}
{{-   if eq (trim $caCert) "" -}}
{{-     fail "cozy-lib.tls.caCertSecret: caCert is required and must be a non-empty PEM" -}}
{{-   end -}}
{{- /* Anchor both guards to the PEM header form, case-insensitively. A free
       substring match would false-positive on a legitimate certificate whose
       subject/comment text happens to contain "PRIVATE KEY", and would miss a
       lowercase hand-pasted header. The (?i) header regex catches every key
       variant (PKCS#8, RSA, EC, ENCRYPTED, OPENSSH, DSA, PGP) and only a real
       BEGIN...PRIVATE KEY line, not arbitrary blob text.

       It stops at "PRIVATE KEY" rather than anchoring on the closing "-----",
       which reads tighter and is strictly weaker: PGP's header is
       "-----BEGIN PGP PRIVATE KEY BLOCK-----", so a closing anchor lets the one
       flavour a human is most likely to paste by hand walk past the guard. This
       end has no second line of defence — unlike the controller, which would
       still refuse a PGP block because every PEM block must parse as a
       CERTIFICATE — so the miss would land a private key in a tenant-readable
       Secret. */ -}}
{{-   if regexMatch "(?i)-----BEGIN [A-Z0-9 ]*PRIVATE KEY" $caCert -}}
{{-     fail "cozy-lib.tls.caCertSecret: caCert must not contain private key material" -}}
{{-   end -}}
{{- /* Require the WHOLE value to be certificate blocks and whitespace, not
       merely to CONTAIN one. The regex is anchored (\A ... \z) around one or
       more COMPLETE blocks — armour, at least one base64 character of body, the
       closing line — because ca.crt below is emitted VERBATIM. An unanchored
       match let a value carry bytes before the first block or after the last and
       still pass, and those bytes — a human-readable preamble, or a raw DER/JWK
       key that wears no PEM private-key header and so slips the guard above —
       would then be published to the tenant inside the trust anchor. Anchoring
       makes preamble and trailing bytes a rejection, not a leak. A bare
       "-----BEGIN CERTIFICATE-----", armour with no END, and armour around text
       that could never be base64 are refused for the same reason: each renders a
       trust anchor carrying nothing a verifier can load, under a name that tells
       the tenant it is the CA.

       INHERENT LIMIT, stated rather than papered over: this bounds the block's
       SHAPE, not its contents. The body is only checked for characters outside
       the base64 alphabet, so text that happens to be spelled with base64
       characters ("this is not base64 and never could be") is accepted — a
       regex cannot count a base64 body modulo 4, let alone decode it. And even
       a body that IS base64 may decode to anything: a Helm template has no x509
       parser. Sprig's only one is buildCustomCert, and it takes (cert, key) and
       fails unless the PRIVATE KEY parses too — precisely what a trust anchor
       must never carry, so it is unusable here by construction. Base64 that
       decodes to something other than a certificate therefore still passes this
       guard.

       That gap is bounded by where the two producers get their bytes. This
       helper is for charts that hold the CA as a VALUE at render time, so its
       input is platform-authored and a bad value is a chart bug that fails
       loudly at the client. The general path — an operator-minted CA, extracted
       from whatever Secret the engine actually produced — is the CA-extraction
       controller (internal/controller/cacert), which is Go, does the full
       pem.Decode + x509.ParseCertificate, and REBUILDS the projection from the
       parsed blocks. The two ends reach the same result — a trust anchor that is
       nothing but certificates — the controller by re-encoding, this helper by
       refusing anything else, each enforcing as much as its language allows. */ -}}
{{-   if not (regexMatch "(?i)\\A\\s*(-----BEGIN CERTIFICATE-----\\s+[A-Za-z0-9+/=][A-Za-z0-9+/=\\s]*-----END CERTIFICATE-----\\s*)+\\z" $caCert) -}}
{{-     fail "cozy-lib.tls.caCertSecret: caCert must contain a complete PEM certificate block (BEGIN/END CERTIFICATE)" -}}
{{-   end -}}
{{- /* internal.cozystack.io/tenant-ca is the label that actually reaches the
       tenant. It is the single, engine-agnostic selector an ApplicationDefinition
       puts in spec.secrets.include[].matchLabels, and the CA-extraction controller
       stamps it on every anchor it projects. A helper-rendered anchor must carry
       it too, or the two producers of "<release>.tenant-ca" would converge on the
       name but not on the label: the generic selector would miss this object, the
       lineage webhook would mark it tenantresource=false, and the tenant would be
       locked out of the very Secret this helper exists to publish.

       tenantresource is rendered alongside it for shape, but it is NOT what grants
       access: the lineage webhook recomputes that label from the definition's
       selectors when it admits this object and overwrites whatever is written
       here. */ -}}
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
