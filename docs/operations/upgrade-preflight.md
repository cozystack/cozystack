# Upgrade Preflight

Cozystack runs a health gate before a platform upgrade touches anything. It executes as a `pre-upgrade` Helm hook at weight 0, ahead of the schema migrations at weight 1, and refuses to let the upgrade start when the cluster is not in a state where it can safely absorb one. The point is to surface a broken node or a degraded storage layer up front, with a message naming what is wrong, instead of halfway through a HelmRelease rollout.

The gate is enabled by default.

## When it runs

The hook renders only when the `cozystack-version` ConfigMap in the release namespace reports a version **below** `migrations.targetVersion`, which is the same condition the migration hook uses. Three consequences worth knowing:

- A fresh install is never gated. There is no prior state to protect and nothing to query.
- A release that ships no new migration is not gated either, because the version does not advance.
- The checks do not re-run on every Flux reconcile of a healthy cluster, which keeps a live `linstor` exec off the steady-state path.

## What it checks

| Check id | What it asserts | Blocking by default |
| --- | --- | --- |
| `nodes-ready` | Every Kubernetes node carries `Ready=True`. A node with no `Ready` condition at all counts as not ready. | yes |
| `linstor` | Every LINSTOR satellite is `ONLINE` and no DRBD volume sits in `Inconsistent`, `Failed`, `DUnknown` or `Outdated`. Skipped as not-applicable when LINSTOR is not installed. | no, advisory |

Cordoned nodes are exempt from `nodes-ready` whatever their readiness, and are reported as a warning instead. A cordon is the operator declaring a node out of service, and the normal maintenance sequence (cordon, drain, power off) leaves the node at `Ready=Unknown` with its workloads already moved. The trade-off is deliberate: a node that failed and was then cordoned is waved through on that same signal.

Every check fails closed. If a check cannot determine health, because the API server refused the call, timed out, or returned output it could not parse, that counts as a failure rather than a pass. A gate that ran no checks at all also fails.

The `linstor` check is advisory until its detection of faulty states has been exercised against a genuinely degraded cluster: it runs and reports, but a problem it finds warns rather than blocks.

## Reading the verdict

Read the status ConfigMap, not the Job:

```bash
kubectl --namespace cozy-system get configmap cozystack-preflight-status --output yaml
```

It carries `verdict` (`running`, `passed` or `blocked`), the check ids by outcome (`failedChecks`, `advisoryChecks`, `okChecks`, `naChecks`, `skippedChecks`), `completedAt`, and the full run log.

The hook Job is not a durable record. A failed upgrade is retried automatically by the platform HelmRelease (`Upgrade.Strategy` is `RetryOnFailure`, with a retry interval that defaults to 30 seconds), and Helm deletes the previous hook Job at the top of every attempt, so the Job and its log survive roughly one retry interval before an identical run replaces them. The HelmRelease itself reports only a generic `pre-upgrade hooks failed`, which names neither the check nor the node. The ConfigMap is written by every run, including one that starts and is then killed, and no hook lifecycle deletes it.

While the gate is blocking, the upgrade stays pending and the checks keep re-running at the retry interval. Fixing the reported problem is enough; no manual retry is needed.

## Overriding the gate

These keys live under `spec.components.platform.values.preflight` on the `cozystack.cozystack-platform` Package CR. Nothing else reads them, and a misspelled path is silently ignored, so check the nesting.

```yaml
apiVersion: cozystack.io/v1alpha1
kind: Package
metadata:
  name: cozystack.cozystack-platform
spec:
  components:
    platform:
      values:
        preflight:
          # Proceed past every failing check. The failures are logged for the
          # audit trail and downgraded to warnings.
          override: false
          # Skip named checks entirely, instead of overriding all of them.
          skipChecks: []
          # Named checks still run and report, but do not block.
          advisoryChecks: [linstor]
          # Do not render the gate at all.
          enabled: true
```

Prefer the narrowest hatch that clears your situation: fix what the check reports, then `skipChecks` for a single check, then `override` for all of them, then `enabled: false`. Both booleans accept `true`, `1`, `yes` and `on` for yes and `false`, `0`, `no` and `off` for no, in any of the shapes the Package CR's untyped JSON can deliver, so a quoted `"false"` behaves the same as a bare one. Anything unrecognised leaves the gate up.

## Adding a check

Drop an executable script into `packages/core/platform/images/migrations/preflight/checks/`, named `<order>-<id>.sh`. The runner discovers it, derives the check id from the filename, and interprets its exit code: `0` is OK, `2` blocks the upgrade, `3` is not-applicable, and any other non-zero exit is treated as a failure. Source `../lib/preflight-lib.sh` and use `kubectl_t` for API calls so the call is bounded and cannot hang the hook.

Anything a check needs from RBAC must be added to `packages/core/platform/templates/preflight-hook.yaml`. The gate holds read-only cluster-wide access plus two namespaced grants, `pods/exec` in `cozy-linstor` and write access to its own status ConfigMap, and it should stay that way.
