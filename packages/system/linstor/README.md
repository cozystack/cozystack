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

Stopping piraeus-operator does not help on its own. The operator serves the validating webhooks on `LinstorCluster` and `LinstorSatelliteConfiguration` with `failurePolicy: Fail`, so while it is stopped every upgrade that changes one of them fails and retries without end, its post-upgrade hook never runs, and switching back to `linstor` fails the same way.

`storage.blockstorMigration` on the `cozystack.cozystack-platform` Package, passed to this chart as `blockstorMigration`, splits the switch into the runbook's steps. With `hold` or `adopt` every piraeus object renders exactly as under backend `linstor`, so the release changes nothing the stopped operator would have to validate. `hold` keeps the blockstor controller at zero replicas, and the release owns that count, so a retried upgrade does not start it.

1. Do the runbook's pre-flight checks and write down the replica count of `deploy/linstor-csi-controller`. Freeze: scale `deploy/piraeus-operator-controller-manager`, `deploy/linstor-csi-controller` and `deploy/linstor-controller` to 0, in that order. Take the dump and convert it now, with nothing left to write to the LINSTOR database, and apply the converted keyring Secret into `cozy-linstor`.
2. Set `storage: {backend: blockstor, acknowledgeBackendChange: blockstor, blockstorMigration: hold}` and wait for the `linstor` HelmRelease to become Ready. blockstor-apiserver and the satellites run, the controller is 0/0, and `ControllerConfig/default` names the passphrase Secret. Nothing of piraeus changes.
3. Confirm the keyring's key ID matches the manifests, then apply the converted manifests.
4. Set `blockstorMigration: adopt`. The controller starts; wait for adoption to converge as the runbook describes.
5. Scale piraeus-operator back to 1 and wait for it to become Available. It does not restore a replica count it finds at 0: check that `linstor-controller` and `linstor-csi-controller` are still 0/0, and if `linstor-controller` came back, scale it to 0 again before going on.
6. Remove `blockstorMigration`. `LinstorCluster` goes to external mode, which the running operator's webhook admits; the operator deletes linstor-controller, repoints linstor-csi at blockstor-apiserver and rolls its satellite pods.
7. Scale `linstor-csi-controller` back to the count from step 1, which the operator leaves at 0, and run the runbook's verification.

Up to step 6 the way back touches the control plane only, and works with the operator still stopped: set `storage: {backend: linstor, acknowledgeBackendChange: linstor}` without `blockstorMigration` in the same change, wait for the release, then scale `linstor-controller` to 1, piraeus-operator to 1 and `linstor-csi-controller` to its count. The blockstor CRDs and their objects stay behind; nothing reads them under `linstor`.
