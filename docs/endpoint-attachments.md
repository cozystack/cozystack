# Attaching external addresses to application endpoints

`EndpointAttachment` (`local.sdn.cozystack.io/v1alpha1`, namespaced) attaches one external address to one tenant-facing Service of one managed application. It is the Network load-balancing path: a dedicated address per attachment, drawn from the address substrate (`IPAddressClass` / `IPAddressClaim` / `IPAddress` under `local.sdn.cozystack.io`), either minted for the attachment or bound from a claim the tenant reserved earlier. It sits beside, not in place of, the Application load-balancing path through the tenant Gateway, where several databases share one address and are routed by SNI. A user who wants to save addresses publishes through the Gateway; a user whose client allow-lists an IP, whose protocol cannot carry SNI, or who needs an address they can hold and move, attaches one.

Attaching and detaching never touches the application's own Services, values or Helm release. The chart-level `external: true` keeps its exact behaviour in every chart; attachments are additive beside it.

## The resource

```yaml
apiVersion: local.sdn.cozystack.io/v1alpha1
kind: EndpointAttachment
metadata:
  name: mydb-replicas-public
  namespace: tenant-a
spec:
  applicationRef:            # immutable; the application, in this namespace
    kind: Postgres           # group defaults to apps.cozystack.io
    name: mydb
  endpoint:
    serviceName: postgres-mydb-ro
  loadBalancer:              # the only mechanism member today; exactly one is required
    className: public        # mint a claim from this IPAddressClass (omit both for the default class)
    # claimName: held-ip     # ...or bind a reserved IPAddressClaim (exclusive with className)
    # family: IPv4           # IPv4 | IPv6 | Dual
    # ports: [5432]          # a subset of the endpoint's ports
    # method: WholeIP        # WholeIP | PortList, VM datapath modes (see below)
    # allowICMP: true        # PortList only
status:
  phase: Attached            # Pending | Attached | Detached, a projection of the conditions
  serviceName: mydb-replicas-public-x7ktq
  claimName: mydb-replicas-public-k2m9p
  addresses: ["203.0.113.7"]
  conditions: [...]          # Resolved, Provisioned
```

`applicationRef`, `className`, `claimName` and `family` are immutable: they select the address, and changing one under a live attachment could re-mint or release an address a client has allow-listed. Changing the address source is delete-and-recreate, and a reserved claim is how an address survives that. `ports`, `method`, `allowICMP` and the endpoint are render knobs and stay mutable.

## Which Services can be attached

The endpoint must be a Service in the attachment's namespace carrying both label families the platform stamps: the lineage identity labels `apps.cozystack.io/application.{group,kind,name}` matching `applicationRef` on all three, and the tenant-facing marker `internal.cozystack.io/tenantresource: "true"`. The marker is what the lineage webhook writes on exactly the Services an `ApplicationDefinition.spec.services` selects, so operator-internal and headless Services, other applications' Services, system Services and the tenant Gateway's own data-plane Service are all refused with `Resolved=False`. Resolution is by condition, not admission, because the Service set is dynamic: a mariadb `-secondary` Service exists only while `replicas > 1`, and an attachment created before its application resolves once the Service appears.

The Gateway's own LoadBalancer Service is deliberately not attachable. Pinning a reserved address to a tenant Gateway is a TenantGateway concern, through the substrate's own annotation on the Gateway's infrastructure, and is a separate design item.

## What the controller renders

One additive `type: LoadBalancer` Service per attachment, created with `generateName` from the attachment's name so it can never collide with the Service it publishes. The Service mirrors the endpoint's selector and ports (or the `ports` subset), carries the substrate's consumption annotation `local.sdn.cozystack.io/ip-address-claim: <claim>`, and is created with the `loadBalancerClass` the claim's `IPAddressClass` fixes, read before creation because the field is immutable. The rendered Service copies nothing else from the endpoint: no labels, no annotations, so it can never be read as a second tenant-facing endpoint or as a route backend. The controller identifies its Service by the controller owner reference alone; the `cozystack.io/endpoint-attachment` label is a diagnostic, and a Service carrying it without the owner reference is ignored, never adopted.

The attachment itself is owned by the application's HelmRelease by UID and stamped with the application's lineage labels. Deleting the application collects its attachments, their Services and any minted claims; a same-name recreated application does not re-acquire the previous incarnation's attachments.

## Address lifecycle

- `className` (or neither field): a claim is minted under the attachment and collected with it. The address then follows the class's reclaim policy, which defaults to `Retain`: the `IPAddress` stays `Released` until an admin clears its `claimRef`. It does not return to the pool on its own.
- `claimName`: the reserved claim is referenced, never owned or written. Detaching leaves the address held; the same claim can be attached to another endpoint or application later, and the address follows.
- A claim already worn by another Service is reported as `Provisioned=False` / `ClaimInUse` and not contended.
- An endpoint Service that disappears (a scale-down) turns the attachment `Detached`: the rendered Service is withdrawn, the claim and its address are kept, and the attachment reattaches when the Service returns.

## Conditions

`Resolved=True` means the endpoint Service exists and passed the tenant-facing check. `Provisioned=True` means the claim is bound and the rendered Service has been assigned its address. That is all it asserts: address and Service wiring. End-to-end reachability additionally depends on the CNI's own policy objects (SecurityGroups, NetworkPolicies and, under a VPC-capable CNI, the VPC gateway's ingress setting), which this controller does not read. `status.phase` is `Attached` when both conditions are true, `Detached` when the endpoint went away after an address was held, and `Pending` otherwise.

## TLS

Attaching a database does not turn its TLS on. The controller mirrors a Service and knows nothing about engines. In the charts whose TLS default follows `external` (postgres among them), an attachment never sets `external`, so such a database is attached with TLS in whatever state its own values put it. Enable TLS explicitly on the application before attaching a database kind, and expect a follow-up that enforces "no plaintext database on a public address" for attachments the same way it is enforced for `external: true`. No chart adds an IP SAN to a server certificate, so a client dialing an attached address verifies by hostname only if it also sets up DNS for a name the certificate carries.

## VM datapath modes

A vm-instance's tenant-facing Service is headless with a single sentinel port until the chart exposes it, so a VM attachment must set `ports` or `method`; neither gives `Provisioned=False` / `NothingToPublish`. `method: PortList` publishes the listed ports and `method: WholeIP` publishes every port through 1:1 NAT, both rendered as the Service contract cozy-proxy implements. The platform declares whether an implementor of that contract is deployed (`cozystackController.endpointAttachments.cozyProxyContractImplemented`, set by the system bundle wherever it emits cozy-proxy); where it is not, a method-bearing attachment renders no Service and reports `DatapathContractUnavailable`, because a load balancer would still attract an address that every proxy skipped.

Two rules follow from the datapath as built. `WholeIP` presumes a single backend: while the endpoint resolves to more than one ready pod, the attachment keeps its Service and its address but selects nothing and reports `MultipleBackends`; a rolling update or a live migration passes through this state and recovers on its own. And the datapath maps one address per backend pod in either mode: a second delegated Service on the same pod, whether another attachment or a chart Service rendered with `external: true`, is refused with `BackendAlreadyDelegated`, the oldest delegation winning. The refusal is advisory on the attachment side; the authoritative gate belongs to the component programming the NAT. `allowICMP` is honoured by cozy-proxy in `PortList` mode and may become a no-op under a datapath that passes ICMP unconditionally.

## Engines with in-protocol discovery

Kafka, MongoDB replica sets and NATS hand clients other addresses in-protocol. Attaching an address to their Services does not yield a working external endpoint until the allocated address is written back into engine configuration, which is not done yet.

## Quota

`count/endpointattachments.local.sdn.cozystack.io` works with a stock `ResourceQuota`, because the `local.` prefix marks the CRD-served half of the sdn family, which the kube-apiserver serves and where quota admission runs; the aggregated `sdn.cozystack.io` kinds never see it. The scarce resource, addresses, is already bounded at the claim.
