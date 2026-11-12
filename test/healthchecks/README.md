# Release health checks

`make go-module-tests`, part of `make unit-tests`, runs this module; `go -C test/healthchecks test ./...` runs it alone. It requires Go and Helm. The test renders the HelmReleases that carry `healthCheckExprs`, extracts the expressions they actually render, and executes every matching fixture in `testdata/status.yaml` with Flux's `StatusEvaluator`. `StatusReader` also checks API-group/kind matching. No cluster is contacted.

This is a separate Go module because the evaluator must match the installed controller rather than the Cozystack API server's dependency graph. The embedded [helm-controller v1.5.0](https://github.com/fluxcd/helm-controller/blob/v1.5.0/go.mod) uses Flux runtime v0.100.2, cli-utils v0.37.1-flux.1 and cel-go v0.26.1. `TestControllerEvaluatorPin` checks the controller image in `internal/fluxinstall/manifests/fluxcd.yaml` and the selected module versions. When updating helm-controller, refresh the pins from its `go.mod` and rerun the fixtures; do not simply update the expected image.

## Status contracts

| Resource | Current | Failed | Waiting |
| --- | --- | --- | --- |
| CNPG Cluster | Ready=True | No early failure | Every nonready status, including persistent errors, waits until the Helm timeout. |
| EtcdCluster | Positive replica count, all members ready, current-generation Available=True and Progressing=False/Reconciled | Positive replicas and current-generation Available=False with BootstrapFailed or DeadlineExceeded | Bootstrap, quorum loss, partial membership, stale conditions, unknown conditions and paused/zero replicas wait until the timeout. |

The evaluator checks top-level `status.observedGeneration` first, then InProgress, Failed and Current in that order; unmatched expressions return InProgress. The etcd gate deliberately has no InProgress expression so partial membership cannot hide terminal failure. Its conditions carry their own observedGeneration, so the gate checks those explicitly. The terminal fixtures include zero ready members and zero broken members: the operator's PVC-backed clusters do not use `brokenMembers` as a terminal-health signal.

Etcd `replicas=0` is an intentional pause, including a fresh cluster that has never bootstrapped and a cluster paused after its bootstrap deadline expires. A fresh terminal condition does not override the zero-replica policy. The gate assumes a tenant dependency requires a serving datastore, so that state does not satisfy readiness. The [operator pause branch](https://github.com/cozystack/etcd-operator/blob/v0.5.6/controllers/etcdcluster_controller.go#L1407) reports Available=False/Paused because an empty cluster cannot serve requests; its [deadline handler](https://github.com/cozystack/etcd-operator/blob/v0.5.6/controllers/etcdcluster_controller.go#L2032) distinguishes terminal failures from recovery.

CNPG's [Ready writer](https://github.com/cloudnative-pg/cloudnative-pg/blob/v1.30.0/pkg/resources/status/transactions.go#L32) uses ClusterIsNotReady for every nonhealthy phase, so a reason cannot tell a persistent error from bootstrap and the gate declares no `failed` expression. The writer does not report a generation with that condition, so the gate cannot distinguish a pre-edit Ready from a fresh Ready; the fixtures record this limitation, and adding a generation requirement would prevent CNPG from ever becoming Current.

## Verification boundary

An entirely absent `status` causes a CEL evaluation error with these bindings; `has(status.conditions)` only protects a missing field inside an existing status object. An empty `status: {}` evaluates to InProgress. The fixtures distinguish the two instead of claiming the guard handles a missing root binding. The Flux status reader converts the evaluation error to Unknown, preserving nonreadiness.

These tests prove expression execution, status-reader matching and the declared status policy. They do not execute Helm actions, the operators, or downstream `dependsOn`. RetryOnFailure on the gated releases is pinned by the charts' own helm-unittest suites.
