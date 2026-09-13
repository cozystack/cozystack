{{/*
monitoring-agents.tenantLogRoutes renders the list of tenant log routes the
fluent-bit config ranges over, as YAML: one {namespace, host, port} entry per
namespace whose container logs must also reach a tenant VictoriaLogs.

Routes are discovered from the namespaces themselves: the tenant chart stamps
every tenant namespace with namespace.cozystack.io/monitoring, whose value is
the nearest tenant (itself included) that runs a Monitoring application, the
same rule that decides which vmagent collects the namespace's metrics. A
namespace whose label names a tenant other than the root target is routed to
that tenant's vlinsert; a sub-tenant without a Monitoring of its own is
therefore routed to its parent's store, and a namespace labelled with the root
target (or not labelled at all) keeps the root output only, which receives
everything anyway.

The explicit fluent-bit.tenantLogRouting list is layered on top: an entry for a
namespace the discovery also found replaces it (a custom host or port), an
entry for any other namespace is added. `lookup` returns nothing outside a
cluster, so an offline render carries the explicit list alone.

Called from inside the fluent-bit subchart's `tpl`, so `.Values` is the
subchart's values: `.Values.global.target` and `.Values.tenantLogRouting`.
*/}}
{{- define "monitoring-agents.tenantLogRoutes" -}}
{{- $routes := dict -}}
{{- $rootTarget := .Values.global.target -}}
{{- range (lookup "v1" "Namespace" "" "").items -}}
{{-   $target := index (.metadata.labels | default dict) "namespace.cozystack.io/monitoring" | default "" -}}
{{-   if and $target (ne $target $rootTarget) -}}
{{-     $_ := set $routes .metadata.name (dict "namespace" .metadata.name "host" (printf "vlinsert-generic.%s.svc" $target) "port" 9481) -}}
{{-   end -}}
{{- end -}}
{{- range (.Values.tenantLogRouting | default list) -}}
{{-   $_ := set $routes .namespace (dict "namespace" .namespace "host" (.host | default (printf "vlinsert-generic.%s.svc" .namespace)) "port" (.port | default 9481)) -}}
{{- end -}}
{{- $out := list -}}
{{- range $ns := (keys $routes | sortAlpha) -}}
{{-   $out = append $out (index $routes $ns) -}}
{{- end -}}
{{- toYaml $out -}}
{{- end -}}
