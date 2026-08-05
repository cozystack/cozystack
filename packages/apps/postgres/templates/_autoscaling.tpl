{{/*
Read-replica autoscaling helpers (KEDA). See the community design proposal
"database-horizontal-autoscaling". The chart renders a KEDA ScaledObject via
cozy-lib.keda.scaledObject; these helpers compute the engine-specific pieces:
the effective bounds (with the CNPG synchronous-quorum floor folded in), the
vmselect address, and the read-load PromQL.
*/}}

{{/*
postgres.autoscaling.active is "true" only when autoscaling is enabled AND not in
dry-run. It gates dropping the static spec.instances: in dry-run the static count
is kept and the ScaledObject is rendered paused (recommendation mode).
*/}}
{{- define "postgres.autoscaling.active" -}}
{{- if and .Values.autoscaling.enabled (not .Values.autoscaling.dryRun) -}}true{{- end -}}
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
Read-load metric: Σ(active read connections over the standby pods) + target.
Fed to KEDA as an AverageValue trigger so stock HPA computes
desired = ceil((Σ + target) / target) = 1 + ceil(Σ / target) = primaryCount + desiredRead.
The namespace matcher scopes it to this tenant; the exact expression (and the
replication-lag clamp) is calibrated on a live cluster per the proposal's PoC.
*/}}
{{- define "postgres.autoscaling.query" -}}
{{- $ns := .Release.Namespace -}}
{{- $rel := .Release.Name -}}
{{- printf "sum(cnpg_backends_total{namespace=%q,state=\"active\"} * on(namespace,pod) group_left() kube_pod_labels{namespace=%q,label_cnpg_io_cluster=%q,label_cnpg_io_instance_role=\"replica\"}) + %v" $ns $ns $rel .Values.autoscaling.target -}}
{{- end -}}
