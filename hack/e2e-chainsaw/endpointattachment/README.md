# EndpointAttachment E2E (disabled / opt-in)

`chainsaw-test.yaml.disabled` exercises `EndpointAttachment` against a live cluster in two Tests. The postgres Test is the design's contract in one sequence: an operator-internal Service is refused by the tenant-facing check; a minted address publishes the read replicas of one application and is collected with its attachment; a reserved `IPAddressClaim` is attached to one application's primary, detached, still holds the same address, and is then attached to a different application's primary, which the same address now serves; deleting an application collects its attachment and leaves the reserved claim alone. The vminstance Test drives the two datapath modes against cozy-proxy as deployed: `PortList` publishes one port, `WholeIP` publishes every port, and a second whole-IP attachment on the same VM is refused with `BackendAlreadyDelegated` while the first keeps serving.

It is **not** part of the automated suite. The `.disabled` suffix keeps it out of Chainsaw's default discovery and `hack/select-e2e.sh` does not enumerate it. The address substrate the attachment consumes (`IPAddressClaim` / `IPAddressClass` / `IPAddress` under `local.sdn.cozystack.io`, community #35) is not packaged by Cozystack yet, and every attachment draws its address through it, so the suite has nothing to run against in the standard sandbox install.

## Preconditions

- The address-controller and a per-class driver for the sandbox's load balancer (metallb-iad for the MetalLB pool `hack/e2e-post-install-prep.sh` applies), with one `IPAddressClass` annotated as the default whose pool is routable from the machine running Chainsaw. The reachability probes dial the attached addresses from that machine and from pods in the tenant namespace.
- cozy-proxy deployed and the platform declaring it: the vminstance Test needs `cozystackController.endpointAttachments.cozyProxyContractImplemented: true` on the cozystack-controller, which the platform's system bundle sets on the full variants whenever it emits the cozy-proxy Package.
- `nc` and `kubectl` on the machine running Chainsaw. The database probes run `psql` inside the application pods.
- The `tenant-test` namespace, as configured in `hack/e2e-chainsaw/.chainsaw.yaml`.

## Scope note on the VM Test

The design's original VM scenario attached a second address in `PortList` mode to a VM whose chart Service was already `external: true` and expected both addresses reachable. That cannot pass against current cozy-proxy: its rule installer keeps one 1:1 slot per pod in both modes and a second delegated Service on the same pod evicts the first from both the ingress and the egress map, so the two reconcile loops flap over the slot. The Test therefore keeps the VM's chart Service internal and lets the attachment be the only delegated Service on the pod, which is also what the controller's `BackendAlreadyDelegated` refusal enforces from its side. Widening to several addresses per pod waits on a datapath that keeps only the egress half exclusive.

## Running

```sh
chainsaw test --test-file chainsaw-test.yaml.disabled hack/e2e-chainsaw/endpointattachment
```

To enable it permanently, rename it to `chainsaw-test.yaml` and register the suite in the two mapping tables `hack/select-e2e.sh` (`src_to_suites`) and `hack/select-install.sh` (`suite_to_source`) so the round-trip test in `hack/select-install_test.bats` keeps passing; the suite is backed by the `cozystack.cozystack-engine` source, which ships the controller.

## Cleanup

Chainsaw deletes the Postgres, VMDisk, VMInstance, IPAddressClaim and EndpointAttachment objects it applied. The rendered Services and minted claims are owned by their attachments and go with them. With the default `Retain` reclaim policy the `IPAddress` behind the reserved claim is left `Released` once the claim is deleted and needs an admin to clear its `claimRef` before it can be handed out again.
