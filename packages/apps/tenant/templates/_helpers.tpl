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
    2. Otherwise infer from tenant.spec.host:
       - host empty → tenant inherits a derived subdomain apex from the
         parent (`<name>.<parent apex>`); the operator implicitly
         expects routability under that apex, so default ON.
       - host set explicitly to a non-derived value (independent apex
         like `customer1.io`) → operator made a deliberate apex choice;
         keep explicit opt-in, default OFF.

  Escape hatch: an operator who wants a derived-apex tenant without a
  Gateway sets `gateway: false` explicitly on the tenant CR.

  Implementation note: values.yaml defaults `gateway: ~` (null) so the
  "unset" case is distinguishable from explicit `gateway: false`. Helm
  treats null as `kindIs "invalid"` here.

  The helper renders the literal string "true" or "false" (Sprig style)
  — callers must compare with `eq ... "true"` rather than relying on
  bool coercion.
*/}}
{{- define "tenant.gatewayEffective" -}}
{{- if kindIs "invalid" .Values.gateway -}}
{{- if eq (.Values.host | default "") "" -}}true{{- else -}}false{{- end -}}
{{- else -}}
{{- if .Values.gateway -}}true{{- else -}}false{{- end -}}
{{- end -}}
{{- end -}}
