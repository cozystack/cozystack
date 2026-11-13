# node-remediation

Powers off a node of the Cozystack cluster that stays `Ready=Unknown`, through its BMC over Redfish, and releases the node's pods and volumes only after the BMC has confirmed that the system is off. Two [Medik8s](https://github.com/medik8s) operators do it: NodeHealthCheck watches the node conditions and creates a remediation CR from a template, and Fence Agents Remediation performs the power action and sets the `node.kubernetes.io/out-of-service` taint, which Kubernetes turns into force-deleted pods and detached volumes. KubeVirt then fails the VMIs of the dead node and `runStrategy: Always` starts them elsewhere.

The fence-agents operator runs in its identity-bound safety-mode. Every remediation carries the name, UID and system UUID of the Node that NodeHealthCheck selected; the operator reads the Redfish ComputerSystem, checks that its UUID is that node's and that no other known Node shares it, and only then posts `ForceOff` and polls the same system until it reports `Off`. The connection is verified against the CA the hoster supplies; there is no insecure mode. IPMI is not available in this mode. A node whose identity cannot be established is refused and nothing is released.

The package ships three releases into `cozy-node-remediation`: `node-remediation-crds` (the CRDs, kept on uninstall, the `cozy-node-remediation-bmc` namespace and the admission policy on the Secrets in it), `node-remediation` (both operators) and `node-remediation-config` (two NodeHealthCheck objects and the remediation template).

## Enabling

The package is off by default. The platform values decide everything, under `nodeRemediation` in the `cozystack-platform` Package; the keys are documented in `packages/core/platform/values.yaml`. In this order:

1. Make sure no `cozystack.local-ccm` Package runs its node lifecycle controller. That controller deletes a `NotReady` Node without proof that the machine is off, so the render refuses the package while it is on; set `spec.components.local-ccm.values.local-ccm.nodeLifecycleController.enabled: false` on that Package first (the second `local-ccm` is the subchart key the wrapper chart passes the values through). Every mode of the controller is refused, its default included, where it deletes only Nodes carrying the autoscaler's `ToBeDeletedByClusterAutoscaler` taint: such a Node deleted while `NotReady` releases its pods without proof that it is off just the same. The check runs on every render of the platform chart, so turning the controller back on while this package is enabled makes the next platform render fail as a whole, every package included, until one of the two is switched off again; the check reads that Package's values and nothing else.
2. Check every BMC mapping. Open each `address` from a cluster node, read the ComputerSystem at `systemsUri` and confirm that its `UUID` is the `systemUUID` the matching Node reports (`kubectl get node <name> -o jsonpath='{.status.nodeInfo.systemUUID}'`). The two must match byte for byte, case aside: a BMC that reports the first three fields of the SMBIOS UUID in the other byte order is refused as a mismatch, and the fix is on the BMC side or in the mapping, not in the operator. A wrong mapping makes the fence fail when the node needs it: the operator reads the ComputerSystem only once the node is selected, refuses on a UUID that is not the Node's before any power action, and releases nothing, so the node stays unremediated until the mapping is fixed.
3. Fill in `nodeRemediation` and add `cozystack.node-remediation` to `bundles.enabledPackages`. Wait until the `node-remediation-crds` HelmRelease is Ready.
4. Create the credentials Secrets in `cozy-node-remediation-bmc`, each holding exactly the keys `username` and `password`. The admission policy refuses any other shape, and it checks only Secrets written after it exists, which is why this step comes after the previous one. The `node-remediation-config` release stays failed until the Secret its template names exists.
5. Wait until all three HelmReleases are Ready.

A node added later is selected by NodeHealthCheck at once; until it has an entry under `nodes`, its fence fails and nothing is released, so add the entry (with step 2) when the node joins.

## Maintenance

NodeHealthCheck does not know about planned work and remediates a node that stays down longer than `unhealthyAfter`. Before an upgrade, a firmware update or any other planned outage of a node, label it `remediation.medik8s.io/exclude-from-remediation=true`; remove the label afterwards. The same label on every control-plane node turns control-plane fencing off.

A fenced node stays powered off until a person powers it on. Once it is `Ready` again, NodeHealthCheck deletes the remediation CR and the operator removes its taints. A remediation whose fence failed stays as a failed CR; after fixing the BMC access, delete the CR and NodeHealthCheck creates a new one while the node is still unhealthy.

## Disabling

Removing the package from `bundles.enabledPackages` uninstalls nothing: the Package carries `helm.sh/resource-policy: keep`, and deleting it removes the three releases in no particular order. If the operators went first while a remediation was open, that CR would keep its finalizer and the node its taints. So, in this order: wait until no `FenceAgentsRemediation` exists in `cozy-node-remediation-bmc`, set `nodeRemediation.paused: true` and check again, remove the package from `bundles.enabledPackages`, then delete the `cozystack.node-remediation` Package. The CRDs stay; `cozy-node-remediation-bmc` and the credentials go with the first release.

## Limits

The proof needs a BMC that answers. A node that lost its mains power usually takes its BMC down with it; the fence then fails and the manual procedure applies: confirm that the node is off through the BMC, the hypervisor or on site, set `node.kubernetes.io/out-of-service=nodeshutdown:NoExecute` on it, and remove the taint once it is `Ready` again.

The e2e suite of this repository runs on virtual nodes without a BMC. It checks the install, the admission policy and that the fence-agents webhook refuses a remediation for a node whose identity cannot be established; the success path, from an unhealthy node to the out-of-service taint, is not exercised in CI.

The NodeHealthCheck operator keeps upstream's unrestricted grant on ClusterRoles and ClusterRoleBindings, which it uses at start to build the aggregated role that reaches the remediation objects; the fence-agents webhook trusts that operator's service account as the only creator of remediations.
