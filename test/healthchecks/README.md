# Umbrella HelmRelease health checks

Run `make test-health-checks` from the repository root. It requires Go and Helm and is included in `make unit-tests`. The test renders the Harbor, tenant etcd, tenant gateway and monitoring wrappers, extracts their actual `healthCheckExprs`, and executes every matching fixture with Flux's `StatusEvaluator`. `StatusReader` also checks API-group/kind matching. No cluster is contacted.

This is a separate Go module because the evaluator must match the installed controller rather than the Cozystack API server's dependency graph. The embedded [helm-controller v1.5.0](https://github.com/fluxcd/helm-controller/blob/v1.5.0/go.mod) uses Flux runtime v0.100.2, cli-utils v0.37.1-flux.1 and cel-go v0.26.1. The pin test checks the controller image and the selected module versions. When updating helm-controller, refresh the pins from its `go.mod` and rerun the fixtures; do not simply update the expected image.

## Status contracts

| Resource | Current | Failed | Waiting |
| --- | --- | --- | --- |
| CNPG Cluster | Ready=True | No early failure | Every nonready status, including persistent errors, waits until the Helm timeout. |
| EtcdCluster | Positive replica count, all members ready, current-generation Available=True and Progressing=False/Reconciled | Current-generation Available=False with BootstrapFailed or DeadlineExceeded | Bootstrap, quorum loss, partial membership, stale conditions, unknown conditions and paused/zero replicas wait until the timeout. |
| TenantGateway | Ready=True, with the evaluator checking top-level observedGeneration when present | No early failure | Missing/unknown/false Ready waits until the timeout. |
| VLCluster, VMCluster, VMAgent, VMAlert, VMAlertmanager | updateStatus=operational | Any reported value other than operational or expanding, including failed, paused and unknown values | Absent updateStatus and expanding wait; stale observedGeneration waits even when the old status was failed. |
| Grafana | stage=complete and stageStatus=success | stageStatus=failed | Missing, intermediate and unknown stages wait until the timeout. |

The evaluator checks top-level `status.observedGeneration` first, then InProgress, Failed and Current in that order; unmatched expressions return InProgress. The etcd gate deliberately has no InProgress expression so partial membership cannot hide terminal failure. Its conditions carry their own observedGeneration, so the gate checks those explicitly. The terminal fixtures include zero ready members and zero broken members: the operator's PVC-backed clusters do not use `brokenMembers` as a terminal-health signal.

Etcd `replicas=0` is an intentional pause, including a fresh cluster that has never bootstrapped. The gate assumes a tenant dependency requires a serving datastore, so that state does not satisfy readiness. This assumption would be invalid if the tenant dependency contract were changed to treat a deliberately unavailable datastore as ready. The current [operator pause branch](https://github.com/cozystack/etcd-operator/blob/v0.5.5/controllers/etcdcluster_controller.go#L1370) explicitly reports Available=False/Paused because an empty cluster cannot serve requests; its [deadline handler](https://github.com/cozystack/etcd-operator/blob/v0.5.5/controllers/etcdcluster_controller.go#L1939) distinguishes terminal failures from recovery.

CNPG's [Ready writer](https://github.com/cloudnative-pg/cloudnative-pg/blob/v1.30.0/pkg/resources/status/transactions.go#L32) uses ClusterIsNotReady for every nonhealthy phase. It does not report a generation with that condition. [Grafana v5.13.0](https://github.com/grafana/grafana-operator/blob/v5.13.0/api/v1beta1/grafana_types.go#L128) likewise exposes stages without observedGeneration. Their gates cannot distinguish a status left over from before a spec edit from the same status after reconciliation. Fixtures explicitly record this limitation; adding a generation requirement would prevent these operators from ever becoming Current. Upgrade freshness still needs integration coverage.

VictoriaMetrics [v0.68.4 status metadata](https://github.com/VictoriaMetrics/operator/blob/v0.68.4/api/operator/v1beta1/vmextra_types.go#L1354) supplies top-level observedGeneration. TenantGateway writes it alongside Ready in `internal/controller/tenantgateway/status.go`. The fixtures exercise the evaluator's generation check for both contracts.

## Verification boundary

An entirely absent `status` causes a CEL evaluation error with these bindings; `has(status.conditions)` only protects a missing field inside an existing status object. An empty `status: {}` evaluates to InProgress. Tests distinguish the two instead of claiming the guard handles a missing root binding. The Flux status reader converts the evaluation error to Unknown, preserving nonreadiness.

These tests prove expression execution, status-reader matching and the declared status policy. Helm tests also retain the RetryOnFailure strategies on Harbor and the tenant children and monitoring's 15-minute timeout. They do not prove a live Helm action, timeout timing, successful operator reconciliation or downstream `dependsOn` behavior. Integration verification must cover bootstrap to Ready, terminal etcd failure, recovery after a spec edit, CNPG timeout, and preservation of PVCs across retries before the change is considered ready to ship.
