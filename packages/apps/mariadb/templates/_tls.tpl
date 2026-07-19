{{/*
mariadb.tls.certManagerIssued resolves tls.issuer to "true" or "false" so
callers can use it with `eq`:

  {{- $certManagerIssued := (include "mariadb.tls.certManagerIssued" .) | eq "true" -}}

Defaults to the operator, and is deliberately not derived from `external`:
switching issuer re-issues the server certificate under a new authority, which
breaks any client that pinned the previous ca.crt, and it does not raise the
security floor — those instances already serve TLS. So it is chosen rather than
applied on upgrade.

`default (dict)` guards tls: null; `kindIs "invalid"` guards an absent key.
*/}}
{{- define "mariadb.tls.certManagerIssued" -}}
{{- $tlsMap := default (dict) .Values.tls -}}
{{- $issuer := index $tlsMap "issuer" -}}
{{- if kindIs "invalid" $issuer -}}
  {{- "false" -}}
{{- else -}}
  {{- eq $issuer "cert-manager" | toString -}}
{{- end -}}
{{- end -}}
