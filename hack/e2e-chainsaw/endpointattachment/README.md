# EndpointAttachment E2E

`chainsaw-test.yaml` exercises `EndpointAttachment` against a live cluster in two Tests. The postgres Test is the design's contract in one sequence: an attachment that names one application and points at the tenant-facing Service of another is refused with `EndpointLineageMismatch` and renders nothing; a minted address publishes the read replicas of one application and is collected with its attachment; a reserved `IPAddressClaim` is attached to one application's primary, detached, still holds the same address, and is then attached to a different application's primary, which the same address now serves; deleting an application collects its attachment and leaves the reserved claim alone. The vminstance Test drives the two datapath modes against cozy-proxy as deployed: `PortList` publishes one port, `WholeIP` publishes every port, and a second whole-IP attachment on the same VM is refused with `BackendAlreadyDelegated` while the first keeps serving. The cozyplane VPC path lives in its own parked suite, `hack/e2e-chainsaw/endpointattachment-vpc/`, because no e2e lane installs that networking variant.

Every attachment draws its address through the address substrate (`IPAddressClaim` / `IPAddressClass` / `IPAddress` under `local.sdn.cozystack.io`, community #35), whose packages, `cozystack.address-controller` and `cozystack.metallb-iad`, are optional. The e2e install enables both (`hack/e2e-platform-packages.sh`), and a selected run keeps them only when this suite is selected, since it is the only suite that needs them: `hack/select-install.sh` maps the suite to both packages and `hack/select-e2e.sh` maps both back to it, so a change to either selects this suite. Whenever metallb-iad is installed, `hack/e2e-post-install-prep.sh` creates the default `IPAddressClass` `sandbox` on a MetalLB pool of its own that does not auto-assign, `192.168.123.190-199`, announced on the sandbox bridge. The applications and cozy-proxy are the suite's runtime requirements in `hack/select-install.sh`, so a change to one of them does not select it.

## Preconditions

- The applications the fixtures create and their operators: `Postgres` (`cozystack.postgres-application`, with the CloudNativePG operator of `cozystack.postgres-operator`), and `VMDisk` and `VMInstance` (`cozystack.vm-disk-application` and `cozystack.vm-instance-application`, with KubeVirt and CDI). The engine that ships the controller, `cozystack.cozystack-engine`, and the `tenant-test` namespace as configured in `hack/e2e-chainsaw/.chainsaw.yaml`.
- The `cozystack.address-controller` and `cozystack.metallb-iad` packages enabled through `bundles.enabledPackages`, with one `IPAddressClass` annotated as the default whose pool is routable from the machine running Chainsaw; the sandbox gets both as described above. The VM reachability probes dial the attached addresses from that machine; the database probes dial them from pods in the tenant namespace.
- cozy-proxy deployed and the platform declaring it: the vminstance Test needs `cozystackController.endpointAttachments.cozyProxyContractImplemented: true` on the cozystack-controller, which the platform's system bundle sets wherever it emits the cozy-proxy Package: on the full variants unless disabled, and on the slim ones when opted in.
- `nc` and `kubectl` on the machine running Chainsaw. The database probes run `psql` inside the application pods.

## Scope note on the VM Test

The design's original VM scenario attached a second address in `PortList` mode to a VM whose chart Service was already `external: true` and expected both addresses reachable. That cannot pass against current cozy-proxy: its rule installer keeps one 1:1 slot per pod in both modes and a second delegated Service on the same pod evicts the first from both the ingress and the egress map, so the two reconcile loops flap over the slot. The Test therefore keeps the VM's chart Service internal and lets the attachment be the only delegated Service on the pod, which is also what the controller's `BackendAlreadyDelegated` refusal enforces from its side. Widening to several addresses per pod waits on a datapath that keeps only the egress half exclusive.

## Running

```sh
chainsaw test --config hack/e2e-chainsaw/.chainsaw.yaml \
  --set-string storageClass=<class> hack/e2e-chainsaw/endpointattachment
```

`storageClass` is the StorageClass the VM disk imports into, as `make test-chainsaw` passes it to every suite. In CI, `make test-chainsaw CHAINSAW_SUITES=endpointattachment` runs it.

## Cleanup

Chainsaw deletes the Postgres, VMDisk, VMInstance, IPAddressClaim and EndpointAttachment objects it applied. The rendered Services and minted claims are owned by their attachments and go with them. The sandbox class reclaims with `Delete`, so each address returns to the pool when its claim goes. Under the default `Retain` reclaim policy instead, every `IPAddress` the suite used is left `Released` once its claim is deleted: the one behind the reserved claim, and the ones behind the claims the attachments minted. Unless the `IPAddressClass` sets `releaseDelay`, each stays unusable until an admin clears its `claimRef`, so repeated runs drain the pool; with `releaseDelay` set, each returns to `Available` once the delay has passed.
