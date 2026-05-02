{{- define "tenant.name" -}}
{{- $parts := splitList "-" .Release.Name }}
{{- if or (ne ($parts|first) "tenant") (ne (len $parts) 2) }}
{{- fail (printf "The release name should start with \"tenant-\" and should not contain any other dashes: %s" .Release.Name) }}
{{- end }}
{{- if not (hasPrefix "tenant-" .Release.Namespace) }}
{{- fail (printf "The release namespace should start with \"tenant-\": %s" .Release.Namespace) }}
{{- end }}
{{- $tenantName := ($parts|last) }}
{{- if ne .Release.Namespace "tenant-root" }}
{{- printf "%s-%s" .Release.Namespace $tenantName }}
{{- else }}
{{- printf "tenant-%s" $tenantName }}
{{- end }}
{{- end }}

{{/*
  tenant.gatewayEffective resolves whether a tenant should have its own
  per-tenant Gateway. Cozystack targets low-skill operators: the default
  must "just work" without forcing them to learn that `gateway` is a
  separate flag from `host`.

  Rules:
    1. If tenant.spec.gateway is set explicitly (true|false) → use it.
       Operator's explicit choice always wins, regardless of platform
       state — the helmrelease for `gateway` will fail upstream if the
       platform doesn't have gateway.enabled, but that's a user-visible
       error which is the right outcome for explicit opt-in.
    2. Otherwise (gateway unset) → consult both:
       a. `_cluster.gateway-enabled` — the platform-level flag. If the
          platform has not opted in to Gateway API, no auto-default
          fires. Auto-on a Gateway when the cluster doesn't ship the
          gateway-application chart only produces broken HelmReleases.
       b. `tenant.spec.host` — when the platform IS Gateway-enabled,
          a tenant with derived apex (`host` empty, computed as
          `<name>.<parent apex>`) gets auto-on; a tenant with a
          custom non-derived apex requires explicit opt-in, since
          a custom apex is a deliberate operator choice and they may
          not have intended public exposure.

  Escape hatch: an operator who wants a derived-apex tenant without a
  Gateway sets `gateway: false` explicitly on the tenant CR.

  Implementation note: values.yaml leaves `gateway` absent (no key) so
  Helm reads it as missing → `kindIs "invalid"`. cozyvalues-gen's
  `[gateway]` (optional) marker syntax does not allow nullable schema
  typing, so the "key absent" form is what distinguishes "unset" from
  explicit `false`.

  The helper renders the literal string "true" or "false" — callers
  must compare with `eq ... "true"` rather than rely on bool coercion.
*/}}
{{- define "tenant.gatewayEffective" -}}
{{- if kindIs "invalid" .Values.gateway -}}
{{- $platformOn := eq ((index .Values "_cluster" "gateway-enabled") | default "false") "true" -}}
{{- $derivedApex := eq (.Values.host | default "") "" -}}
{{- if and $platformOn $derivedApex -}}true{{- else -}}false{{- end -}}
{{- else -}}
{{- if .Values.gateway -}}true{{- else -}}false{{- end -}}
{{- end -}}
{{- end -}}
