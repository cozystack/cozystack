# EndpointAttachment in a cozyplane VPC E2E (disabled / opt-in)

`chainsaw-test.yaml.disabled` exercises `EndpointAttachment` against a VM whose only interface is in a cozyplane VPC. The launcher pod's address, which a cozy-proxy Service maps to, never reaches such a guest, so a `WholeIP` attachment binds a cozyplane `FloatingIP` on the VM's VPC address instead. The Test refuses `PortList` with `MethodUnsupportedInVPC` and a `Dual` claim with `DualStackUnsupportedInVPC`, with no FloatingIP bound for either. Before the VM starts, it declares the VPC's default `RouteTable` with a `0.0.0.0/0` route to `internet`: cozyplane sends a FloatingIP's replies out of the VPC only through such a route, and the VPC application renders none, so without it the FloatingIP is bound and Ready while the address never answers. It then attaches `WholeIP`, expects a Ready FloatingIP that wears the attachment's address and targets the VM's VPC address, with no Service of the attachment's own, and dials the guest's port 22 on that address.

It is parked apart from `hack/e2e-chainsaw/endpointattachment/` rather than beside it, because it needs the cozyplane networking variant and no e2e lane installs that. On any other variant the cluster serves no `FloatingIP`, and a `VMInstance`'s `networks` name a different kind of VPC, so the Test cannot pass there. The `.disabled` suffix keeps it out of Chainsaw's default discovery and `hack/select-e2e.sh` does not enumerate it.

## Preconditions

- The cozyplane networking variant, with the aggregated `sdn.cozystack.io` API serving `VPC`, `RouteTable`, `Route` and `FloatingIP`.
- The applications the fixtures create: `VirtualPrivateCloud` (`cozystack.virtualprivatecloud-application` in its `cozyplane` variant), and `VMDisk` and `VMInstance` (`cozystack.vm-disk-application` and `cozystack.vm-instance-application`, with KubeVirt and CDI). The engine that ships the controller, `cozystack.cozystack-engine`, and the `tenant-test` namespace as configured in `hack/e2e-chainsaw/.chainsaw.yaml`.
- The address substrate as for `hack/e2e-chainsaw/endpointattachment/`: `cozystack.address-controller` and `cozystack.metallb-iad`, with one `IPAddressClass` annotated as the default whose pool is routable from the machine running Chainsaw. The reachability probe dials the attached address from that machine. The `Dual` refusal needs no IPv6 in the class: the attachment is refused before its claim has to bind.
- `nc` and `kubectl` on the machine running Chainsaw.

cozy-proxy is not needed: the VPC path renders no cozy-proxy Service.

## Running

```sh
chainsaw test --config hack/e2e-chainsaw/.chainsaw.yaml --test-file chainsaw-test.yaml.disabled \
  --set-string storageClass=<class> hack/e2e-chainsaw/endpointattachment-vpc
```

`storageClass` is the StorageClass the VM disk imports into, as `make test-chainsaw` passes it to every suite.

## Cleanup

Chainsaw deletes the VirtualPrivateCloud, VMDisk, VMInstance and EndpointAttachment objects it applied. The FloatingIP and the minted claims are owned by their attachments and go with them; the Test also waits for the whole-IP attachment's FloatingIP to be collected. What happens to each `IPAddress` afterwards follows the class's reclaim policy, as described for `hack/e2e-chainsaw/endpointattachment/`.
