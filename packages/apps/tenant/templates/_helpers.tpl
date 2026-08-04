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
  tenant.gatewayEffective resolves whether THIS tenant should own a
  dedicated Gateway resource. Returns the literal string "true" or
  "false" (callers must compare with `eq ... "true"`, bool coercion
  is not safe).

  When the helper returns "false" the tenant does NOT skip Gateway
  routing — it attaches its published Routes to the Gateway of the
  nearest ancestor that owns one, via _namespace.gateway propagation
  in namespace.yaml. This mirrors the existing _namespace.ingress
  inheritance: a child tenant publishes through its parent's
  publishing layer unless it explicitly asks for its own.

  Rules:
    1. tenant.spec.gateway set explicitly (true|false) → use it.
       Explicit choice always wins. A tenant that asks for its own
       Gateway gets its own Service / LB IP / Certificate.
    2. Otherwise → "false". The tenant inherits the parent's Gateway
       through _namespace.gateway. Derived-apex children (`host`
       empty) flow naturally: their predefined hostnames
       `*.<name>.<parent-apex>` are routed by the ancestor's
       controller which extends listener / SAN coverage as children
       attach. If no ancestor owns a Gateway, _namespace.gateway
       stays empty and apps fall back to Ingress (legacy path).

  Custom-apex tenants (operator sets tenant.spec.host to something
  not derived from the parent apex, e.g. `customer1.io`) must opt in
  explicitly via `gateway: true` if they want public exposure. The
  ancestor's TLS cert does not cover their apex and Let's Encrypt
  wildcards are single-level, so silent inheritance would either
  leak through the wrong cert or fail to terminate TLS. Forcing the
  explicit flag keeps "I want my apex routable" a deliberate choice.

  Implementation note: values.yaml leaves `gateway` absent (no key)
  so Helm reads it as missing → `kindIs "invalid"`. cozyvalues-gen's
  `[gateway]` (optional) marker syntax does not allow nullable schema
  typing, so the "key absent" form is what distinguishes "unset"
  from explicit `false`.
*/}}
{{/*
  tenant.ancestorTenantLabels emits the full set of
  `tenant.cozystack.io/<ancestor>: ""` namespace labels for the tenant whose
  namespace name is passed as the single argument (e.g. "tenant-ktj-htdev").

  Every tenant descends from tenant-root, so that label is always emitted.
  The remaining ancestors are encoded in the namespace name itself: tenant.name
  constructs each child namespace as `<parent-namespace>-<word>`, so every
  progressive dash-prefix of the name is a real ancestor namespace
  (tenant-ktj-htdev -> tenant-ktj, tenant-ktj-htdev). The last prefix is the
  tenant's own name, so its self-label is included too.

  Deriving the chain from the name (rather than from a lookup of the parent
  namespace's labels) is deterministic: it needs no cluster state, renders
  identically offline, and converges on every reconcile regardless of the
  order in which parent and child HelmReleases reconcile. It replaces an
  earlier splitList over `.Release.Namespace` that only ever reached one level
  up and therefore dropped tenant-root for tenants at depth >= 2 — breaking the
  `<ancestor>-egress` CiliumClusterwideNetworkPolicy that grants an ancestor
  reachability to its descendants via the `tenant.cozystack.io/<ancestor>`
  namespace label.
*/}}
{{- define "tenant.ancestorTenantLabels" -}}
{{- $parts := splitList "-" . -}}
tenant.cozystack.io/tenant-root: ""
{{- range $i, $v := $parts }}
{{- if ne $i 0 }}
{{ printf "tenant.cozystack.io/%s: \"\"" (join "-" (slice $parts 0 (add $i 1))) }}
{{- end }}
{{- end }}
{{- end -}}

{{- define "tenant.gatewayEffective" -}}
{{- if kindIs "invalid" .Values.gateway -}}
false
{{- else -}}
{{- if .Values.gateway -}}true{{- else -}}false{{- end -}}
{{- end -}}
{{- end -}}
