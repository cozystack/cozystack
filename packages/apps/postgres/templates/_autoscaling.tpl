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
scopes it to the tenant. `or vector(0)` floors the read load at zero so that with
no active connections the query still returns `target` (→ desired 1, clamped up by
minReplicaCount) rather than an empty result.

Replication-lag brake (postgres.autoscaling.query, when maxReplicationLagSeconds>0):
while the max standby lag has exceeded the threshold at any point in the cooldown
window AND the primary is actively writing WAL, the query returns
`currentInstances * target` instead of `Σ + target`, which pins desired to the
current instance count and freezes scaling in BOTH directions (safer than pinning
maxReplicas, which would still allow scale-down under lag). Using max_over_time for
the lag term is the hysteresis band — the brake holds for the cooldown after a spike
rather than flapping on a single sample. The write gate (rate over WAL records > 0)
keeps an idle primary from tripping the brake. Metrics verified live on CNPG 1.30.0:
cnpg_pg_replication_lag (seconds) and cnpg_collector_wal_records exist and carry
namespace/pod; the design's cnpg_pg_stat_replication_sent_diff_bytes does not, so
WAL-record rate is the write signal. The cluster is scoped by joining kube_pod_labels
on (namespace,pod), same as the read-load term. The exact thresholds/cooldown are
tuned per the proposal's PoC; the base (no-lag) path is validated live.
*/}}
{{- define "postgres.autoscaling.query" -}}
{{- $ns := .Release.Namespace -}}
{{- $rel := .Release.Name -}}
{{- $target := .Values.autoscaling.target -}}
{{- $joinReplica := printf "* on(namespace,pod) group_left() kube_pod_labels{namespace=%q,label_cnpg_io_cluster=%q,label_cnpg_io_instance_role=\"replica\"}" $ns $rel -}}
{{- /* Read-load term Σ, floored at 0 so an idle cluster yields `target`. */ -}}
{{- $load := "" -}}
{{- if eq .Values.autoscaling.metric "ReadCPUUtilization" -}}
{{- $load = printf "((sum(rate(container_cpu_usage_seconds_total{namespace=%q,container=\"postgres\"}[5m]) %s) * 1000) or vector(0))" $ns $joinReplica -}}
{{- else -}}
{{- $load = printf "(sum(cnpg_backends_total{namespace=%q,state=\"active\"} %s) or vector(0))" $ns $joinReplica -}}
{{- end -}}
{{- $base := printf "%s + %v" $load $target -}}
{{- $maxLag := int (default 0 .Values.autoscaling.maxReplicationLagSeconds) -}}
{{- if le $maxLag 0 -}}
{{- $base -}}
{{- else -}}
{{- /* Cluster-wide join (all instances) for the lag and WAL-write terms. */ -}}
{{- $joinCluster := printf "* on(namespace,pod) group_left() kube_pod_labels{namespace=%q,label_cnpg_io_cluster=%q}" $ns $rel -}}
{{- $cooldown := "5m" -}}
{{- $lagHigh := printf "(max(max_over_time(cnpg_pg_replication_lag{namespace=%q}[%s]) %s) > bool %d)" $ns $cooldown $joinCluster $maxLag -}}
{{- $writing := printf "(max(rate(cnpg_collector_wal_records{namespace=%q}[5m]) %s) > bool 0)" $ns $joinCluster -}}
{{- $braking := printf "((%s * %s) or vector(0))" $lagHigh $writing -}}
{{- $frozen := printf "(count(kube_pod_labels{namespace=%q,label_cnpg_io_cluster=%q}) * %v)" $ns $rel $target -}}
{{- /* base when not braking, frozen (= currentInstances * target) when braking. */ -}}
{{- printf "(%s) * (1 - %s) + %s * %s" $base $braking $frozen $braking -}}
{{- end -}}
{{- end -}}
