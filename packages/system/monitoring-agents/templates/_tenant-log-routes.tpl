{{- /*
  Tenant log routes for the node fluent-bit, as JSON {"<target>": ["<namespace>", ...]}.

  The tenant chart sets namespace.cozystack.io/monitoring on every tenant
  namespace to the nearest tenant (itself or an ancestor) that runs its own
  monitoring stack, so the label names the VictoriaLogs a namespace's logs
  belong to. A namespace is routed when that label is set and names a tenant
  other than global.target; an unlabelled namespace, an empty label, or one
  pointing at global.target stays on the default outputs.

  lookup results are not part of the digest helm-controller compares, so a
  namespace gaining, changing or losing the label does not re-render this
  release by itself. The tenantlogrouting reconciler in cozystack-controller
  (internal/controller/tenantlogrouting) forces an upgrade of this release
  whenever any namespace's label changes, so a new selection here that reads
  anything beyond that label must be taught to the reconciler too. Offline
  renders (helm template, dry-run) get no namespaces back from lookup and route
  nothing.
*/ -}}
{{- define "monitoring-agents.tenantLogRoutes" -}}
{{- $routes := dict -}}
{{- $namespaces := lookup "v1" "Namespace" "" "" -}}
{{- range $ns := ($namespaces.items | default list) -}}
{{- $target := index ($ns.metadata.labels | default dict) "namespace.cozystack.io/monitoring" | default "" -}}
{{- if and $target (ne $target $.Values.global.target) -}}
{{- $_ := set $routes $target (append (get $routes $target | default list) $ns.metadata.name) -}}
{{- end -}}
{{- end -}}
{{- toJson $routes -}}
{{- end -}}
