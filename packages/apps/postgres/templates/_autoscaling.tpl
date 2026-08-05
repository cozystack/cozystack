{{/*
Read-replica autoscaling helpers (KEDA). See the community design proposal
"database-horizontal-autoscaling". The chart renders a KEDA ScaledObject via
cozy-lib.keda.scaledObject; these helpers compute the engine-specific pieces:
the effective bounds (with the CNPG synchronous-quorum floor folded in), the
vmselect address, and the read-load PromQL.
*/}}

{{/*
postgres.autoscaling.active is "true" only when autoscaling is enabled AND neither
in dry-run NOR mid-transition. It gates dropping the static spec.instances: while
dry-run (permanent recommendation) or transition (migration phase 1) is set, the
static count is kept and the ScaledObject is rendered paused, so KEDA never
contends with Flux for the field.
*/}}
{{- define "postgres.autoscaling.active" -}}
{{- if and .Values.autoscaling.enabled (not .Values.autoscaling.dryRun) (not .Values.autoscaling.transition) -}}true{{- end -}}
{{- end -}}

{{/*
postgres.autoscaling.paused is "true" when the ScaledObject must be rendered
paused: dry-run (recommendation) or transition (migration phase 1).
*/}}
{{- define "postgres.autoscaling.paused" -}}
{{- if or .Values.autoscaling.dryRun .Values.autoscaling.transition -}}true{{- end -}}
{{- end -}}

{{/*
Effective lower bound = max(minReplicas, maxSyncReplicas+1, 2). The synchronous
quorum floor (maxSyncReplicas+1) and the read-serving floor (2) can never be
undercut; both live in this same chart's values, so a tenant raising
maxSyncReplicas re-renders the floor atomically.
*/}}
{{- define "postgres.autoscaling.effectiveMin" -}}
{{- $min := int (default 2 .Values.autoscaling.minReplicas) -}}
{{- $floor := add (int (default 0 .Values.quorum.maxSyncReplicas)) 1 -}}
{{- max $min $floor 2 -}}
{{- end -}}

{{/*
Effective upper bound = max(maxReplicas, effectiveMin). When the quorum floor
exceeds the configured maxReplicas, quorum wins and the maximum is raised to the
floor rather than clamping the cluster below a safe quorum.
*/}}
{{- define "postgres.autoscaling.effectiveMax" -}}
{{- $max := int (default 6 .Values.autoscaling.maxReplicas) -}}
{{- $emin := int (include "postgres.autoscaling.effectiveMin" .) -}}
{{- max $max $emin -}}
{{- end -}}

{{/*
Prometheus-API address of the tenant's vmselect (shared root vmselect in the
MVP, scoped by the query's namespace matcher; the per-object address lets a
tenant with an isolated monitoring stack point elsewhere later).
*/}}
{{- define "postgres.autoscaling.serverAddress" -}}
{{- printf "http://vmselect-shortterm.%s.svc:8481/select/0/prometheus" (include "cozy-lib.ns-monitoring" .) -}}
{{- end -}}

{{/*
Read-load metric: Σ(read load over the standby pods) + target, fed to KEDA as an
AverageValue trigger so stock HPA computes
desired = ceil((Σ + target) / target) = 1 + ceil(Σ / target) = primaryCount + desiredRead.
The join restricts the metric to this app's standby pods; the namespace matcher
scopes it to the tenant. The exact expression (and the replication-lag clamp) is
calibrated on a live cluster per the proposal's PoC.
*/}}
{{- define "postgres.autoscaling.query" -}}
{{- $ns := .Release.Namespace -}}
{{- $rel := .Release.Name -}}
{{- $join := printf "* on(namespace,pod) group_left() kube_pod_labels{namespace=%q,label_cnpg_io_cluster=%q,label_cnpg_io_instance_role=\"replica\"}" $ns $rel -}}
{{- if eq .Values.autoscaling.metric "ReadCPUUtilization" -}}
{{- printf "sum(rate(container_cpu_usage_seconds_total{namespace=%q,container=\"postgres\"}[5m]) %s) * 1000 + %v" $ns $join .Values.autoscaling.target -}}
{{- else -}}
{{- printf "sum(cnpg_backends_total{namespace=%q,state=\"active\"} %s) + %v" $ns $join .Values.autoscaling.target -}}
{{- end -}}
{{- end -}}
