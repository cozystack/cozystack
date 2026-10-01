# Umbrella HelmRelease health checks

Run `make test-health-checks` from the repository root. It requires Go, Helm, dash and jq and is included in `make unit-tests`. The test renders the Harbor, tenant etcd, tenant gateway and monitoring wrappers, extracts their actual `healthCheckExprs`, and executes every matching fixture with Flux's `StatusEvaluator`. `StatusReader` also checks API-group/kind matching. No cluster is contacted.

This is a separate Go module because the evaluator must match the installed controller rather than the Cozystack API server's dependency graph. The embedded [helm-controller v1.5.0](https://github.com/fluxcd/helm-controller/blob/v1.5.0/go.mod) uses Flux runtime v0.100.2, cli-utils v0.37.1-flux.1 and cel-go v0.26.1. The pin test checks the controller image and the selected module versions. When updating helm-controller, refresh the pins from its `go.mod` and rerun the fixtures; do not simply update the expected image.

## Status contracts

| Resource | Current | Failed | Waiting |
| --- | --- | --- | --- |
| CNPG Cluster | Ready=True | No early failure | Every nonready status, including persistent errors, waits until the Helm timeout. |
| EtcdCluster | Positive replica count, all members ready, current-generation Available=True and Progressing=False/Reconciled | Positive replicas and current-generation Available=False with BootstrapFailed or DeadlineExceeded | Bootstrap, quorum loss, partial membership, stale conditions, unknown conditions and paused/zero replicas wait until the timeout. |
| TenantGateway | Ready=True, with the evaluator checking top-level observedGeneration when present | No early failure | Missing/unknown/false Ready waits until the timeout. |
| VLCluster, VMCluster, VMAgent, VMAlert, VMAlertmanager | updateStatus=operational | Any reported value other than operational or expanding, including failed, paused and unknown values | Absent updateStatus and expanding wait; stale observedGeneration waits even when the old status was failed. |

The evaluator checks top-level `status.observedGeneration` first, then InProgress, Failed and Current in that order; unmatched expressions return InProgress. The etcd gate deliberately has no InProgress expression so partial membership cannot hide terminal failure. Its conditions carry their own observedGeneration, so the gate checks those explicitly. The terminal fixtures include zero ready members and zero broken members: the operator's PVC-backed clusters do not use `brokenMembers` as a terminal-health signal.

Etcd `replicas=0` is an intentional pause, including a fresh cluster that has never bootstrapped and a cluster paused after its bootstrap deadline expires. A fresh terminal condition does not override the zero-replica policy. The gate assumes a tenant dependency requires a serving datastore, so that state does not satisfy readiness. This assumption would be invalid if the tenant dependency contract were changed to treat a deliberately unavailable datastore as ready. The current [operator pause branch](https://github.com/cozystack/etcd-operator/blob/v0.5.5/controllers/etcdcluster_controller.go#L1370) explicitly reports Available=False/Paused because an empty cluster cannot serve requests; its [deadline handler](https://github.com/cozystack/etcd-operator/blob/v0.5.5/controllers/etcdcluster_controller.go#L1939) distinguishes terminal failures from recovery.

CNPG's [Ready writer](https://github.com/cloudnative-pg/cloudnative-pg/blob/v1.30.0/pkg/resources/status/transactions.go#L32) uses ClusterIsNotReady for every nonhealthy phase. It does not report a generation with that condition, so the gate cannot distinguish a pre-edit Ready from a fresh Ready. Fixtures explicitly record this limitation; adding a generation requirement would prevent CNPG from ever becoming Current. Upgrade freshness still needs integration coverage.

VictoriaMetrics [v0.68.4 status metadata](https://github.com/VictoriaMetrics/operator/blob/v0.68.4/api/operator/v1beta1/vmextra_types.go#L1354) supplies top-level observedGeneration. TenantGateway writes it alongside Ready in `internal/controller/tenantgateway/status.go`. The fixtures exercise the evaluator's generation check for both contracts.

## Grafana rollout observer

Grafana operator v5.13.0's [Deployment reconciler](https://github.com/grafana/grafana-operator/blob/v5.13.0/controllers/reconcilers/grafana/deployment_reconciler.go) reports stage success after writing the Deployment, before its Pods are ready. The chart therefore uses its post-install/post-upgrade Job to wait for the real rollout, including when OIDC is disabled. User reconciliation and pruning remain conditional on the existing nonempty users map.

The chart hashes the complete rendered Grafana spec before adding `monitoring.cozystack.io/grafana-spec-hash` to its deployment Pod template. Both the CR and Job use the same validated base; invalid YAML, a missing spec map or a self-referencing marker fails rendering. The operator propagates the annotation through its deployment override merge. A matching marker, current Grafana owner UID, current Deployment observedGeneration, full updated/ready/available/total replicas and positive availability conditions are required before checking HTTP health. A second sample after HTTP rejects replacement or generation changes during that request. Old healthy Pods cannot satisfy a new spec's marker. Identical specs retain the marker during retries rather than restarting convergence.

`grafana_readiness_test.go` renders the real Job and executes its shell under dash, using real jq and fake named API GETs, HTTP responses and time. Cases include stale markers/generations, foreign owners, incomplete or paused rollouts, absent/malformed resources, API/HTTP failures, replacement during HTTP, recovery and health-only versus managed-user calls. Separate render tests check hash inputs, stable retries and malformed bases through both callers. The test doubles transport observations; they do not reproduce the readiness predicate in Go. The Alpine image runs BusyBox ash, so dash is a local compatibility check, not proof of that runtime.

The dedicated ServiceAccount can only get the named Grafana and Deployment. External Secret contents are neither read nor hashed; changes to them or other out-of-band operator inputs do not alter this marker. The observer covers a Helm action's desired spec and current HTTP health, not continuous monitoring or OAuth login correctness.

## Verification boundary

An entirely absent `status` causes a CEL evaluation error with these bindings; `has(status.conditions)` only protects a missing field inside an existing status object. An empty `status: {}` evaluates to InProgress. Tests distinguish the two instead of claiming the guard handles a missing root binding. The Flux status reader converts the evaluation error to Unknown, preserving nonreadiness.

These tests prove expression execution, status-reader matching, rendered observer behavior and the declared status policy. Helm tests retain RetryOnFailure on Harbor, tenant children and both inner monitoring actions, plus monitoring's 15-minute timeout and enabled hooks. RetryOnFailure is necessary because uninstalling a failed inner monitoring release deletes its CNPG Clusters, whose owner references make database PVCs garbage-collection targets. No retention policy is changed.

These tests do not execute Helm actions, the actual operator merge, BusyBox ash in the pinned non-root image, service-account/RBAC/network-policy access or downstream `dependsOn`. Integration verification must cover bootstrap, an unavailable new Grafana rollout while old Pods still answer HTTP, timeout and corrected-spec recovery, zero-replica etcd after terminal bootstrap, CNPG upgrade freshness, and unchanged Cluster/PVC UIDs and database sentinels through failed-install and upgrade retries. Those checks remain required before shipping.
