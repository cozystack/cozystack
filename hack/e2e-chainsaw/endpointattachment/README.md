# EndpointAttachment E2E (disabled / opt-in)

`chainsaw-test.yaml.disabled` exercises `EndpointAttachment` against a live cluster in three Tests. The postgres Test is the design's contract in one sequence: a minted address publishes the read replicas of one application and is collected with its attachment; a reserved `IPAddressClaim` is attached to one application's primary, detached, still holds the same address, and is then attached to a different application's primary, which the same address now serves; deleting an application collects its attachment and leaves the reserved claim alone. The refusal Test attaches to a MariaDB Service that carries the application's lineage labels but is not listed in its application definition, and expects `EndpointNotTenantFacing`. The vminstance Test drives the two datapath modes against cozy-proxy as deployed: `PortList` publishes one port, `WholeIP` publishes every port, and a second whole-IP attachment on the same VM is refused with `BackendAlreadyDelegated` while the first keeps serving.

It is **not** part of the automated suite. The `.disabled` suffix keeps it out of Chainsaw's default discovery and `hack/select-e2e.sh` does not enumerate it. Every attachment draws its address through the address substrate (`IPAddressClaim` / `IPAddressClass` / `IPAddress` under `local.sdn.cozystack.io`, community #35). Its packages, `cozystack.address-controller` and `cozystack.metallb-iad`, ship as optional platform packages, but the standard sandbox install enables neither and creates no `IPAddressClass`, so the suite has nothing to draw addresses from there.

## Preconditions

- The `cozystack.address-controller` and `cozystack.metallb-iad` packages enabled through `bundles.enabledPackages` (metallb-iad drives the MetalLB pool `hack/e2e-post-install-prep.sh` applies), with one `IPAddressClass` annotated as the default whose pool is routable from the machine running Chainsaw. The reachability probes dial the attached addresses from that machine and from pods in the tenant namespace.
- cozy-proxy deployed and the platform declaring it: the vminstance Test needs `cozystackController.endpointAttachments.cozyProxyContractImplemented: true` on the cozystack-controller, which the platform's system bundle sets wherever it emits the cozy-proxy Package: on the full variants unless disabled, and on the slim ones when opted in.
- `nc` and `kubectl` on the machine running Chainsaw. The database probes run `psql` inside the application pods.
- The `tenant-test` namespace, as configured in `hack/e2e-chainsaw/.chainsaw.yaml`.

## Scope note on the VM Test

The design's original VM scenario attached a second address in `PortList` mode to a VM whose chart Service was already `external: true` and expected both addresses reachable. That cannot pass against current cozy-proxy: its rule installer keeps one 1:1 slot per pod in both modes and a second delegated Service on the same pod evicts the first from both the ingress and the egress map, so the two reconcile loops flap over the slot. The Test therefore keeps the VM's chart Service internal and lets the attachment be the only delegated Service on the pod, which is also what the controller's `BackendAlreadyDelegated` refusal enforces from its side. Widening to several addresses per pod waits on a datapath that keeps only the egress half exclusive.

## Running

```sh
chainsaw test --config hack/e2e-chainsaw/.chainsaw.yaml --test-file chainsaw-test.yaml.disabled hack/e2e-chainsaw/endpointattachment
```

To enable it permanently, rename it to `chainsaw-test.yaml` and register the suite in the two mapping tables `hack/select-e2e.sh` (`src_to_suites`) and `hack/select-install.sh` (`suite_to_source`) so the round-trip test in `hack/select-install_test.bats` keeps passing; the suite is backed by the `cozystack.cozystack-engine` source, which ships the controller.

## Cleanup

Chainsaw deletes the Postgres, MariaDB, VMDisk, VMInstance, IPAddressClaim and EndpointAttachment objects it applied. The rendered Services and minted claims are owned by their attachments and go with them. Under the default `Retain` reclaim policy, every `IPAddress` the suite used is left `Released` once its claim is deleted: the one behind the reserved claim, and the ones behind the claims the attachments minted. Unless the `IPAddressClass` sets `releaseDelay`, each stays unusable until an admin clears its `claimRef`, so repeated runs drain the pool; with `releaseDelay` set, each returns to `Available` once the delay has passed.
