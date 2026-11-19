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
