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
{{- /* Read the values directly - values.yaml/schema already default them (min 2,
       maxSyncReplicas 0). Do NOT wrap in Sprig `default`: it treats a numeric 0 as
       empty and would silently substitute the fallback, discarding an explicit 0.
       An out-of-range value flows into max() and is clamped, never swapped. */ -}}
{{- $min := int .Values.autoscaling.minReplicas -}}
{{- $floor := add (int .Values.quorum.maxSyncReplicas) 1 -}}
{{- max $min $floor 2 -}}
{{- end -}}

{{/*
Active-path seed for spec.instances = max(.Values.replicas, effectiveMin). This is
the CONSTANT the chart writes to spec.instances whenever autoscaling is active. Two
properties matter and both hold:
  - it never undercuts the quorum/read floor (it is >= effectiveMin), so CNPG admits
    the Cluster and a fresh enable lands at a valid count;
  - it equals .Values.replicas whenever replicas has been staged at/above the floor,
    so the else-branch (dryRun/transition/off, which renders instances: replicas) and
    this active branch render the SAME value — the client-side patch (previous-vs-new
    render) is then empty across an enable / dryRun / transition-phase-2 flip and the
    live count does not dip.
Seeding effectiveMin alone (an earlier variant) made the two branches differ, so a
transition phase-2 flip rebased a staged live count down to the bare floor, shedding
replicas + PVCs the "warm hand-off" was supposed to preserve. Because the seed is a
constant, each reconcile renders the same value it rendered last time, so under
client-side apply the patch is empty and KEDA's higher live value written through /scale
is never reverted — helm-controller patches this CRD from the previous-vs-new rendered
manifest and does not consult live state (it leaves Helm's threeWayMergeForUnstructured at
the default). CAVEAT: this no-op holds only while the seed VALUE is unchanged between
renders; editing replicas / minReplicas / quorum.maxSyncReplicas moves the seed, and the
two-way patch then resets spec.instances to the new seed in one step (see the values.yaml
@field notes on those keys). BUT the whole scheme holds only under CLIENT-SIDE apply. The
platform's helm-controller (v1.5.0) defaults to server-side apply, which force-owns any
rendered field and reverts it from KEDA's /scale value; so the postgres ApplicationDefinition
forces client-side apply via release.cozystack.io/helm-server-side-apply: "false" (see the
db.yaml seed comment + pkg/registry/apps/application/rest.go). An omit-under-active design
was evaluated and rejected: under SSA the phase-2 handoff prunes spec.instances whenever
KEDA has not yet written /scale (e.g. a cluster idle at its floor), collapsing it to 1 —
see community design proposal §Upgrade "Implementation update (2026-09-02)".
*/}}
{{- define "postgres.autoscaling.activeSeed" -}}
{{- $replicas := int .Values.replicas -}}
{{- $emin := int (include "postgres.autoscaling.effectiveMin" .) -}}
{{- max $replicas $emin -}}
{{- end -}}

{{/*
Effective upper bound = max(maxReplicas, effectiveMin). The scaledobject.yaml render
guard rejects effectiveMin > maxReplicas before this is read, so in a rendered chart
effectiveMin <= maxReplicas always and this resolves to maxReplicas. The max() is a
defense-in-depth backstop that cannot actually engage given that guard (even maxReplicas
<= 0 is already rejected, since effectiveMin >= 2 > 0); it is kept so effectiveMax can
never render below a safe quorum should the guard ever be relaxed.
*/}}
{{- define "postgres.autoscaling.effectiveMax" -}}
{{- /* Direct read, not Sprig `default`: an explicit maxReplicas: 0 must clamp up to
       the floor via max(), not be silently coerced to the fallback 6. */ -}}
{{- $max := int .Values.autoscaling.maxReplicas -}}
{{- $emin := int (include "postgres.autoscaling.effectiveMin" .) -}}
{{- max $max $emin -}}
{{- end -}}

{{/*
Prometheus-API address of the tenant's vmselect (shared root vmselect in the
MVP, scoped by the query's namespace matcher; the per-object address lets a
tenant with an isolated monitoring stack point elsewhere later).
*/}}
{{- define "postgres.autoscaling.serverAddress" -}}
{{- /* An explicit autoscaling.serverAddress wins; otherwise derive the tenant's
       shortterm vmselect. The derived address only works where the SAME vmselect holds
       BOTH cnpg_backends_total (from the CNPG podMonitor) AND kube_pod_labels (from
       kube-state-metrics) — true for a tenant using the shared root monitoring stack
       (monitoring: false), where both land in tenant-root. A tenant running its OWN
       Monitoring app (monitoring: true) has cnpg metrics in its vmselect but NOT
       kube_pod_labels (KSM lives only in root), so the join is empty there; such a tenant
       must set autoscaling.serverAddress to a vmselect that carries both series (or use
       a non-default metricsStorages name), else the query never resolves. */ -}}
{{- with .Values.autoscaling.serverAddress -}}
{{- . -}}
{{- else -}}
{{- printf "http://vmselect-shortterm.%s.svc:8481/select/0/prometheus" (include "cozy-lib.ns-monitoring" .) -}}
{{- end -}}
{{- end -}}

{{/*
Read-load metric: Σ(read load over the standby pods) + target, fed to KEDA as an
AverageValue trigger so stock HPA computes
desired = ceil((Σ + target) / target) = 1 + ceil(Σ / target) = primaryCount + desiredRead.
The join restricts the metric to this app's standby pods; the namespace matcher
scopes it to the tenant. The read load is floored to zero ONLY when replica pods
exist, so an idle cluster still returns `target` (→ desired 1, clamped up by
minReplicaCount) while a cluster whose carrying series are absent returns empty
(fail-safe hold — see the caveat below), instead of an unconditional `or vector(0)`
that cannot tell no-load from no-series.

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

Fail-safe (per the design proposal, "must not read a missing sample as zero" / "No
blind scaling"), SCOPED to the kube-state-metrics pod-label join: the read-load floor is
GATED on the existence of ANY CNPG instance pod (count(kube_pod_labels{...instance_role
=~".+"}) > 0), so the query separates two empty cases an unconditional `or vector(0)`
would conflate:
  - the cluster has instance pods (running idle, or still provisioning its replicas) but
    no active read connections -> load 0 -> desired 1 (scale to floor);
  - NO CNPG pod labels exist at all (a broken KSM pod-label pipeline) -> the floor is
    WITHHELD, the term stays empty. The cozy-lib helper pins ignoreNullValues=false on the
    trigger, so KEDA treats an empty result as an error (FailedGetExternalMetric) rather
    than its default of 0, and the HPA HOLDS the current count. (Without
    ignoreNullValues=false the empty result would read as 0 -> desired 0 -> clamp to the
    floor, i.e. a silent scale-DOWN — the exact failure this gate exists to prevent.)
The gate keys on ANY instance role (not replica-only) precisely so initial provisioning /
large restore — when the only replica-side pod is CNPG's join Job (jobRole, not
instanceRole) — floors on the primary and does NOT error; a replica-only gate would
spuriously trip DatabaseAutoscalerScalerErrors on a healthy basebackup that routinely
exceeds the alert's 15m window.
LIMIT: the gate keys on kube_pod_labels, not on the load-metric source. If kube_pod_labels
is healthy but the load series itself vanishes (cnpg_backends_total unscraped/renamed, or
container_cpu_usage_seconds_total absent for the CPU metric — a separate cAdvisor
pipeline), the term still floors to 0 and the cluster drifts down toward the floor
silently (the query returns a valid 0, so ScalerErrors does not fire). That drift is
bounded (never below the quorum floor, no collapse) and heavily damped (scaleDown
stabilization 1800s, 1 pod / 600s); operators should still alert on absent load series
for autoscaled namespaces. NOTE the opt-in lag brake below (maxReplicationLagSeconds > 0):
gating the floor on any instance role also keeps its base term non-empty during
provisioning so it no longer errors there; it stays opt-in / not-live-validated.
*/}}
{{- define "postgres.autoscaling.query" -}}
{{- $ns := .Release.Namespace -}}
{{- $rel := .Release.Name -}}
{{- $target := .Values.autoscaling.target -}}
{{- $joinReplica := printf "* on(namespace,pod) group_left() kube_pod_labels{namespace=%q,label_cnpg_io_cluster=%q,label_cnpg_io_instance_role=\"replica\"}" $ns $rel -}}
{{- /* Read-load term Σ. The floor is GATED on the existence of ANY instance pod
       (instance_role=~".+", primary OR replica), not replica-only: `or (vector(0) and
       on() (count(...instance_role=~".+") > 0))` returns 0 whenever the cluster has a
       labelled CNPG pod (idle or still provisioning -> desired 1), and WITHHOLDS the
       floor only when NO CNPG pod labels exist at all (a broken KSM pipeline) so the term
       stays empty (-> KEDA FailedGetExternalMetric -> HPA holds). Gating on replica-only
       would withhold the floor during initial provisioning/large restore — when the sole
       replica-side pod is CNPG's join Job (jobRole, not instanceRole) — and, with
       ignoreNullValues=false, spuriously fire DatabaseAutoscalerScalerErrors on a healthy
       basebackup that routinely exceeds the alert's 15m window. The primary always exists
       during provisioning, so gating on any instance role floors correctly there. This is
       the fail-safe separation of no-load from no-series. */ -}}
{{- $idleFloor := printf "(vector(0) and on() (count(kube_pod_labels{namespace=%q,label_cnpg_io_cluster=%q,label_cnpg_io_instance_role=~\".+\"}) > 0))" $ns $rel -}}
{{- $load := "" -}}
{{- if eq .Values.autoscaling.metric "ReadCPUUtilization" -}}
{{- $load = printf "((sum(rate(container_cpu_usage_seconds_total{namespace=%q,container=\"postgres\"}[5m]) %s) * 1000) or %s)" $ns $joinReplica $idleFloor -}}
{{- else -}}
{{- $load = printf "(sum(cnpg_backends_total{namespace=%q,state=\"active\"} %s) or %s)" $ns $joinReplica $idleFloor -}}
{{- end -}}
{{- $base := printf "%s + %v" $load $target -}}
{{- $maxLag := int .Values.autoscaling.maxReplicationLagSeconds -}}
{{- if le $maxLag 0 -}}
{{- $base -}}
{{- else -}}
{{- /* Cluster-wide join (all instances) for the lag and WAL-write terms. */ -}}
{{- $joinCluster := printf "* on(namespace,pod) group_left() kube_pod_labels{namespace=%q,label_cnpg_io_cluster=%q}" $ns $rel -}}
{{- $cooldown := "5m" -}}
{{- /* Each gate is individually floored with `or vector(0)`: if its source series
       is absent (a CNPG that does not emit cnpg_pg_replication_lag /
       cnpg_collector_wal_records) the gate resolves to 0 rather than an empty
       vector. That makes the fail-open decision explicit and per-gate — the brake
       only engages on positively-observed lag AND writing — instead of leaving it
       to the emptiness of the product. The brake is a freeze; failing open (scale
       normally) when a signal is missing is safer than freezing on absent data. */ -}}
{{- $lagHigh := printf "((max(max_over_time(cnpg_pg_replication_lag{namespace=%q}[%s]) %s) > bool %d) or vector(0))" $ns $cooldown $joinCluster $maxLag -}}
{{- $writing := printf "((max(rate(cnpg_collector_wal_records{namespace=%q}[5m]) %s) > bool 0) or vector(0))" $ns $joinCluster -}}
{{- $braking := printf "(%s * %s)" $lagHigh $writing -}}
{{- /* currentInstances: count only instance pods. Restrict to pods carrying an
       instance role (label_cnpg_io_instance_role=~".+") like the read-load term
       above; CNPG stamps cnpg.io/cluster on its bootstrap/join Job pods too (they
       carry jobRole, not instanceRole), and kube-state-metrics reports them until
       GC. Without this filter a lingering join Job pod inflates the frozen count
       while the brake is engaged, nudging desired up instead of holding it. */ -}}
{{- $frozen := printf "(count(kube_pod_labels{namespace=%q,label_cnpg_io_cluster=%q,label_cnpg_io_instance_role=~\".+\"}) * %v)" $ns $rel $target -}}
{{- /* base when not braking, frozen (= currentInstances * target) when braking.
       The frozen summand is floored with `or vector(0)`: PromQL `+` is a set
       intersection, so if count(kube_pod_labels) is momentarily empty (a
       kube-state-metrics scrape gap) an unfloored frozen term would empty the
       whole sum and defeat the base term's own `or vector(0)`, silently pinning
       desired to the floor under real load. Flooring keeps base + 0 = base. */ -}}
{{- printf "(%s) * (1 - %s) + ((%s * %s) or vector(0))" $base $braking $frozen $braking -}}
{{- end -}}
{{- end -}}
