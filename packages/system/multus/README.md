# Multus

Multus is the meta-CNI that lets a pod or a virtual machine carry a secondary network interface. This package installs it as the primary CNI shim, provides the `NetworkAttachmentDefinition` CRD, and stages the upstream reference CNI plugins into `/opt/cni/bin`.

For the operator-facing guide to putting a VM on a physically routed VLAN with these plugins, see [Attaching a virtual machine to an external VLAN](https://cozystack.io/docs/v1.6/networking/vm-external-vlan/).

## Staging the reference CNI plugins

The prose below is a contract: `hack/cni-plugins-staging-contract.bats` asserts the staged plugin names, the count, and the remediation commands quoted here against `images/multus-cni/Dockerfile`, `templates/multus-daemonset-thick.yml` and `packages/core/platform/values.yaml`. Update it in the same commit as any change to what is staged.

The `multus` package installs the reference plugins into `/opt/cni/bin` itself, on every platform, because no platform supplies all of them. That is the `stageCniPlugins` chart value, and the platform turns it on for every bundle. What differs is only how much is missing before staging runs. Talos puts six binaries in that directory — `bridge`, `firewall`, `host-local`, `loopback` and `portmap` from the same upstream release, plus `flannel`, which is a different project — so a NAD naming `macvlan`, `ipvlan` or `vlan` fails there too; staging adds the ten it does not carry and rewrites the four that overlap with the same upstream version (`v1.9.1` on both sides), not an older one. Generic Linux offers no guarantee at all — k3s keeps its own copies under `/var/lib/rancher/k3s/data/cni` (`/var/lib/rancher/k3s/data/current/bin` on releases older than October 2024) and Cilium installs only `cilium-cni` and `loopback`, so `/opt/cni/bin` there can lack even `bridge`.

Carrying the plugins costs roughly 67 MiB uncompressed per architecture in the `multus` image, which every node pulls whether staging is on or off. Size airgap mirrors accordingly.

Where staging is on, it tries to replace any file of the same name already in that directory, every time the Multus pod is recreated, so a hand-placed or locally patched `bridge` will normally not survive — carry such a change in the `multus` image instead. A plugin it fails to write is reported and skipped rather than removed, so whatever was there stays — an older copy where the node had one, and nothing at all where it did not. It installs 14 of the upstream reference plugins and leaves `loopback`, `dhcp`, `dummy` and `tap` alone. Leaving them alone is not a promise that something else provides them: `loopback` is installed by Cilium and kube-ovn, while `dhcp`, `dummy` and `tap` are simply not staged — `dhcp` needs a host daemon this package does not run, and the other two have no consumer here. If you need one of those, put it on the node yourself.

**If you provision `/opt/cni/bin` yourself, set the opt-out before upgrading.** The replacement happens as soon as Multus rolls, and turning `stageCniPlugins` off afterwards does not undo it: nothing in this package removes or restores a plugin, so the copies stay until you re-provision the node. Operators pinned to a particular plugin version should decline first and upgrade second. This applies on Talos too, where four of the names staging writes are ones the node image also carries. Declining looks like this, on the `platform` component of the `cozystack.cozystack-platform` Package CR:

```yaml
apiVersion: cozystack.io/v1alpha1
kind: Package
metadata:
  name: cozystack.cozystack-platform
spec:
  components:
    platform:
      values:
        networking:
          stageCniPlugins: false
```

Editing the multus HelmRelease or the child `Package cozystack.multus` directly does not hold: both are re-rendered by the platform reconcile. An unreadable value there — a spelling that is neither a `true` nor a `false` form — stops the whole platform render rather than resolving to off, so a typo in this key halts reconciliation cluster-wide.

If `bridge` is missing on a node where staging is on, read the log with `kubectl --namespace cozy-multus logs <multus-pod> --container install-cni-plugins`. The line `no /cni-plugins in this image; skipping staging` means the running `multus` image predates the plugin staging and installed nothing: the image reference and the manifest are re-pinned on different schedules, so a tree taken between a Dockerfile change and the next release bake can carry one without the other. Move to a release whose `multus` image carries the plugins. A line beginning `failed to install:` names plugins the container could not write, one of which may be the `bridge` you are looking for. The reason for each is earlier in the same log, printed as that plugin was attempted rather than next to the summary — so with more than one failure the causes are spread above it, not adjacent to it, and the one naming your plugin is the one to read. The container reports these and exits successfully on purpose, so the pod shows Ready and `kubectl get` will not point you here: failing it instead would leave the node unable to start any pod that goes through CNI, since Multus remains the primary CNI with no daemon behind it — host-network pods bypass CNI and still start. That means a NAD failing with `failed to find plugin` is the symptom to trust, and this log is where the reason is.

If the init container is absent from the Multus pod altogether, staging is off for this cluster. That means `networking.stageCniPlugins` was set to `false` somewhere: no bundle defaults it off, on Talos or anywhere else. Turn it back on with the same block as above and `true` in place of `false`, or stage the plugins through node provisioning instead.

Changing this value edits the Multus DaemonSet's pod template, so Multus rolls node by node. While a node's `multus-daemon` is down its CNI shim has no daemon behind it, and pod creation and deletion on that node fail for the length of the restart. Flip it during a window where that is acceptable, not while something else is rolling.
