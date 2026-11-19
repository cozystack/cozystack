# linstor

## Talos satellite graceful shutdown

Cozystack's Talos configuration removes the systemd `drbd-shutdown-guard`. The optional satellite preStop hook releases unused Secondary DRBD resources so their backing devices, including ZFS zvols, can be released during normal shutdown.

| Parameter | Default | Effect |
| --- | --- | --- |
| `talos.gracefulShutdown.enabled` | `false` | Add the satellite preStop hook when both `talos.enabled` and `drbd.enabled` are also `true`. |

The default satellite lifecycle is unchanged. To enable the hook, set these values for the `linstor` component of the `cozystack.linstor` package:

```yaml
talos:
  gracefulShutdown:
    enabled: true
```

Keep the setting in the package's declarative configuration at `spec.components.linstor.values.talos.gracefulShutdown.enabled`. The LINSTOR Helm chart embeds `hack/satellite-pre-stop.py` in `LinstorSatelliteConfiguration/cozystack-talos`; Piraeus converts its `podTemplate` to a strategic merge patch for each selected satellite DaemonSet. Package reconciliation and upgrades therefore retain the hook while the value remains enabled. Editing a generated Pod or DaemonSet directly does not persist through reconciliation. The hook uses Python and `drbdsetup` already supplied by the Cozystack satellite image.

The hook runs on every graceful satellite container stop, including ordinary pod restarts. It skips all Primary resources and any resource with an open device, rechecks each candidate, then calls `drbdsetup down` without force. Unknown or malformed status, command failure and timeout stop further actions. It does not demote resources, call the Kubernetes API, or change access permissions.

Enable it only when satellite and node restarts are performed sequentially, with replication and quorum checked between nodes. Piraeus creates a separate DaemonSet for each node; this chart does not serialize their rollouts. A shared configuration or image update can therefore stop unused Secondary replicas on several nodes at once and pause I/O until they return. Changing or disabling the setting can trigger a pod rollout, and the old pod still runs its existing hook.

Drain workloads before a planned node shutdown. The hook has a 60-second work budget, with each status command limited to 4 seconds and each down command to 10 seconds, and requests a 90-second pod termination grace period. The kubelet's node-shutdown budget can terminate the hook earlier; the pod grace period does not override that budget. Forced reboot, failed kubelet shutdown, still-open devices and unfinished replication are not fixed by this hook.

Related upstream discussion: [Piraeus orderly shutdown on Talos](https://github.com/piraeusdatastore/piraeus-operator/issues/860).

Run `make test` in this directory with Helm, helm-unittest and Python 3.11 or later. The tests cover rendering with each feature flag, resource selection and rechecks, bounded errors and timeouts, dry-run behavior, and logging in the shared pod PID namespace.

## Moving a LINSTOR cluster to blockstor

blockstor adopts the volumes of a LINSTOR cluster in place with `linstor-migrate`, following [blockstor's migration runbook](https://github.com/cozystack/blockstor/blob/main/docs/linstor-migration.md). The runbook freezes CSI and stops LINSTOR first, deploys blockstor, applies the converted manifests, waits for adoption, and repoints CSI last. Setting `storage.backend: blockstor` alone does all of that in one step: piraeus-operator goes to external mode, deletes linstor-controller and repoints linstor-csi before a single volume is adopted, and the blockstor controller starts before its manifests are in.

Stopping piraeus-operator does not help on its own. The operator serves the validating webhooks on `LinstorCluster` and `LinstorSatelliteConfiguration` with `failurePolicy: Fail`, and Helm patches those objects on every upgrade and rollback of the release, even when their render has not changed: it adds its ownership label and annotations to the new objects before diffing them against the previous manifest, so the patch is never empty, and a release applied server-side sends whole objects. With nothing serving the webhooks, each such upgrade fails and retries without end, and its post-upgrade hooks never run.

`storage.blockstorMigration` on the `cozystack.cozystack-platform` Package, passed to this chart as `blockstorMigration`, splits the switch into the runbook's steps. With `hold` or `adopt` every piraeus object renders exactly as under backend `linstor`, so the operator, once it is started again, finds LINSTOR's shape and keeps linstor-csi on LINSTOR. `hold` keeps the blockstor controller at zero replicas, and the release owns that count, so a retried upgrade does not start it. linstor-scheduler and linstor-gui stay on LINSTOR through both steps and move to blockstor-apiserver with linstor-csi, when the value is removed: until then blockstor serves no Resources, or Resources it has not adopted, and the scheduler extender cannot place a pod on those.

Under backend `blockstor`, and under backend `linstor` while blockstor's CRDs are left on the cluster, the release carries pre-install, pre-upgrade and pre-rollback hooks that set `failurePolicy: Ignore` on the two webhooks above when their Service has no ready endpoint, and post hooks of the same three that set `Fail` back on exactly what was changed. A LINSTOR cluster blockstor never reached carries no hooks and upgrades as before. Nothing about the webhooks has to be edited by hand at any step below. With the operator running the hooks change nothing, and the webhooks validate as before. A release that fails between the two hooks leaves the webhooks relaxed until the next install, upgrade or rollback of the release whose pre hook finds the operator serving; the `linstor.cozystack.io/relaxed-webhooks` annotation on `piraeus-operator-validating-webhook-configuration` names them meanwhile.

Run each step below as its own change, never together with a Cozystack upgrade. An upgrade of the platform also upgrades the piraeus-operator release, which sets the operator's replica count back and starts it in the middle of the freeze.

1. Do the runbook's pre-flight checks and write down the replica count of `deploy/linstor-csi-controller`. Freeze: scale `deploy/piraeus-operator-controller-manager`, `deploy/linstor-csi-controller` and `deploy/linstor-controller` to 0, in that order. Take the dump and convert it now, with nothing left to write to the LINSTOR database, and apply the converted keyring Secret into `cozy-linstor`.
2. Set `storage: {backend: blockstor, acknowledgeBackendChange: blockstor, blockstorMigration: hold}` and wait for the `linstor` HelmRelease to become Ready. blockstor-apiserver and the satellites run, the controller is 0/0, and `ControllerConfig/default` names the passphrase Secret. Nothing of piraeus's own objects changes, and linstor-scheduler still points at linstor-controller.
3. Confirm the keyring's key ID matches the manifests, then apply the converted manifests.
4. Set `blockstorMigration: adopt`. The controller starts; wait for adoption to converge as the runbook describes.
5. Scale piraeus-operator back to 1 and wait for it to become Available. It does not restore a replica count it finds at 0: check that `linstor-controller` and `linstor-csi-controller` are still 0/0, and if `linstor-controller` came back, scale it to 0 again before going on.
6. Remove `blockstorMigration`. `LinstorCluster` goes to external mode, which the running operator's webhook admits; the operator deletes linstor-controller, repoints linstor-csi at blockstor-apiserver and rolls its satellite pods. linstor-scheduler and linstor-gui are repointed at blockstor-apiserver in the same change.
7. Scale `linstor-csi-controller` back by hand to the count from step 1: the operator repoints it in step 6 but keeps the 0 it found, and nothing in the release sets it. Then run the runbook's verification, and check that `piraeus-operator-validating-webhook-configuration` no longer carries the `linstor.cozystack.io/relaxed-webhooks` annotation. The release of step 6 sets back whatever an earlier failed release left relaxed, so an annotation still there means that release did not finish.

Up to step 6 the way back touches the control plane only, and works with the operator still stopped: set `storage: {backend: linstor, acknowledgeBackendChange: linstor}` without `blockstorMigration` in the same change and wait for the release, which passes the webhooks the same way, then scale `linstor-controller` to 1, piraeus-operator to 1 and `linstor-csi-controller` back by hand to its count from step 1. The blockstor CRDs and their objects stay behind; nothing reads them under `linstor`.
