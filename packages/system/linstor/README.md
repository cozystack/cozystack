# Talos satellite graceful shutdown

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

## Upgrading the blockstor satellites

The `blockstor-satellite` DaemonSet replaces its pods one node at a time and holds the rollout on each node for `blockstorSatellite.minReadySeconds` (180 by default) after the new pod turns Ready. Ready only means the satellite's caches have synced, which takes seconds; its first pass, which re-renders and adjusts every DRBD resource on the node, ended 1.5 to 2.7 minutes after Ready on a test cluster with about 110 resources per node. The default covers that, so first passes on two nodes do not overlap; a node with more resources needs a longer pause. `maxUnavailable` is always 1 and `maxSurge` always 0, and the chart refuses other values: a wider rollout would run several first passes at once, and a second satellite on the node would drive the same DRBD resources.

The pause stops the rollout only if the satellite exits or restarts within it. Readiness does not drop when resources fail to adjust: a satellite that renders a bad `.res`, or whose `drbdadm adjust` fails, keeps running and stays Ready, and the next node follows once the pause is over. For an upgrade that changes how the satellite renders DRBD resources, the per-node procedure below is the only check that catches that.

The `linstor` HelmRelease waits for the whole rollout, and every node adds its pause to it: about 3.5 minutes per node at the default. cozystack-operator gives a package upgrade 10 minutes (`--helmrelease-upgrade-timeout`), which covers two nodes with room and three only just. On a larger cluster the first upgrade attempt times out while the DaemonSet keeps rolling; the release retries the upgrade, becomes Ready once the last node is done, and runs its post-upgrade hooks with the attempt that succeeds. Lower `minReadySeconds`, or raise the operator's timeout, if that is not acceptable. Changing either value does not restart the satellites. While the rollout runs:

- the HelmRelease is not Ready, and `HelmReleaseNotReady` fires once that has lasted 5 minutes, so on a satellite image change for any cluster of two nodes or more;
- `cozystack.linstor-scheduler` and `cozystack.linstor-gui` depend on `cozystack.linstor`, and their own upgrades wait for it.

For an upgrade that changes how the satellite renders DRBD resources, check each node before the next one goes. Set `blockstorSatellite.updateStrategy.type: OnDelete` in `spec.components.linstor.values` of the `cozystack.linstor` Package before the upgrade, so that nothing moves until a pod is deleted. Run every command below in the `satellite` container of that node's pod, `kubectl -n cozy-linstor exec <pod> -c satellite -- <command>`: it holds the rendered `/etc/drbd.d`, and `drbdadm` on the host or in another container reads a different configuration.

1. Before the upgrade, save `drbdsetup show` and `drbdsetup status --verbose` from every node.
2. Upgrade. The controller and the apiserver roll; the satellites stay on the old version.
3. Delete the satellite pod on one node and wait for the new one to be Ready and for its first pass to finish. Nothing reports the end of the pass yet. Every `.res` file whose rendering the release changes gets a new modification time in `/etc/drbd.d` (`ls -l --time-style=full-iso /etc/drbd.d`), and step 4 shows whether anything is still pending; wait at least 3 minutes after Ready on a node with about a hundred resources.
4. On that node, `drbdadm -d adjust all` prints nothing, `drbdsetup show` differs from the saved copy only where the release notes say it does, and `drbdsetup status` shows every resource in the role, disk and connection state it had before, none of them suspended. If `drbdadm -d adjust all` still prints something, the pass has not finished or has failed: wait and run it again, and read the satellite's log before going on.
5. Repeat steps 3 and 4 for the next node, then set the strategy back to `RollingUpdate`.

Under `OnDelete` the HelmRelease cannot become Ready until every satellite has been replaced by hand: the upgrade attempt times out and retries, as above, and everything listed above lasts until the last pod, which may be hours. To roll back, first revert the change that brought the new version, the platform version or the `cozystack.linstor` Package values, rather than running `helm rollback`, which helm-controller undoes; then replace the satellites one node at a time the same way.
