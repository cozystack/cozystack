# site-router follow-ups (deferred by decision)

This is the consolidated list of work surfaced during the Phase-1 `site-router` build and deferred by explicit decision (see `PLAN.md` / `DECISIONS.md`). Nothing here blocks the Phase-1 increment; each item is tracked so no implicit debt is lost. It is the source list the maintainer turns into filed issues — the app itself ships without them.

## Image and build

- **Reproducible in-repo VyOS build (landed).** The pipeline is implemented in `packages/system/vyos-router-image` (pinned `vyos-build` flavor + containerDisk Makefile), wired into CI as the `build-vyos` job. It publishes a digest-pinned OCI containerDisk that each gateway's boot `DataVolume` imports via CDI's registry importer, with the digest stamped into `packages/apps/site-router/images/vyos-router-disk.tag`. See `docs/image-lifecycle.md`.
- **Validate the appliance image on a booted gateway (maintainer action).** The empirical boot-conformance proof against a real gateway — cloud-init applies the seed, the HTTPS `/configure` REST answers, eth0 DHCPs, nginx serves :443, the firewall seed applies — which the deferred site-router e2e covers once the image is published. Publishing the containerDisk and committing its real digest is a merge prerequisite rather than a follow-up: a placeholder digest is a reference that resolves nowhere while looking like a valid pin.

## Security hardening

- **Negative-security e2e runtime proof.** The guard structure (management firewall, tunnel-ingress source filter, forward default-deny, IPsec-match jump, Boundary-A management drop, MSS clamp, forced ESP-in-UDP encapsulation) is implemented and unit-tested, and its VyOS 1.5-rolling leaf syntax is validated live against the shipped image — the `firewall ipv4 {input,forward} filter` / `firewall ipv4 name <NAME>` family and the `ipsec match-ipsec-in` / `ipsec match-none-in` matchers (a full render-equivalent config commits cleanly), kept behind single-point helpers so a future image whose syntax differs is a one-place change. What remains is the runtime proof on a booted two-VM topology that an undeclared-source packet, and a valid remote-source packet with a world / non-tenant destination, are both actually dropped — the e2e negative-security suite. Blocked on the published appliance image.
- **Scoped `port_security`.** Replace the full gateway-port relaxation with declared-prefix scoping once kube-ovn supports a CIDR in allowed-address-pairs (`ovn.kubernetes.io/aaps`). Track upstream kube-ovn CIDR-AAP support. The guest tunnel-ingress source filter and its negative tests stay regardless of this change.
- **Tenant-baseline Cilium exclusion for the gateway (Boundary B hardening).** Cilium allow rules are additive, so the tenant baseline's broad allow-external/internal-communication ingress still reaches the gateway endpoint alongside the gateway-ingress policy. Fully realising Boundary B requires excluding the gateway endpoint from the tenant baseline — a shared `packages/apps/tenant` change with broad blast radius. In Phase 1 the guest VyOS firewall is the real backstop; this hardening makes the pod-boundary layer authoritative too.
- **Controller-namespace API key + post-boot rotation.** The management-API token is seeded via first-boot cloud-init. A controller-namespace key with post-boot rotation to a value that never appeared in cloud-init would remove the at-rest-in-user-data exposure. Deferred, matching the reference implementation's acknowledged trade-off.

## Observability

- **Tunnel byte / rekey counter metrics.** The controller surfaces tunnel and BGP up/down state, but per-tunnel byte counters and rekey counts are not yet exported — they need a guest-command change (to fetch the counters) plus a parser addition in `internal/vyos`. Deferred; the up/down gauges cover Phase-1 acceptance.

## Networking

- **`_cluster.pod-cidr` derivation for `managementCIDR`.** The chart's `managementCIDR` and the controller's `--management-cidr` both default to the kube-ovn default pod CIDR `10.244.0.0/16` and must be kept consistent by hand; a cluster with a custom `networking.podCIDR` needs both set manually or the management firewall rejects the real controller source. Deriving the value from an engine-injected `_cluster.pod-cidr` (as the LoadBalancer class is already injected via `_cluster."load-balancer-class"`) would make custom-pod-CIDR clusters work without manual configuration and remove the drift-locks-out-the-controller footgun. Until then the drift has no dedicated diagnostic: the controller firewalls itself out of the guest management API and the operator sees an opaque `ConfigureFailed` push timeout with nothing in it pointing at `managementCIDR`. Comparing the controller's own pod IP against the configured CIDR and naming both in a Warning Event is the cheap half of this item, deferred with it.
- **IPsec local-address / LB tunnel-address wiring.** The controller leaves the IPsec `local-address` unset so VyOS auto-detects it (the Phase-1 responder model). Wiring the tunnel LoadBalancer address into the render as an explicit local-address is a documented follow-up.
- **BGP is receive-only in Phase 1.** The public schema exposes `bgp.enabled`, `bgp.localASN` and `bgp.neighbors`, and the controller wires exactly those, so an enabled session establishes and accepts the neighbour's routes while originating none of its own. The render half that would originate them — `AdvertisedNetworks`, plus `RouterID`, `Timers` and per-peer `BGPPasswords` — is implemented in `internal/vyos/render` and reachable only from that package's unit tests, because no API field feeds it. Exposing `advertisedNetworks` (and with it the decision of what a tenant may advertise into a remote site) is the follow-up; until then "BGP enabled" means route learning, not route origination.

## Controller reconciliation and status

- **Controller failure is not reflected in the application's readiness (D9).** A SiteRouter mediation failure surfaces through Warning Events and controller metrics where applicable, but the HelmRelease projection has no controller-owned condition and the failure is NOT propagated into the aggregated `apps.cozystack.io/SiteRouter` application readiness the tenant reads. Closing this needs a durable per-instance status design plus the deferred apiserver pass-through into application readiness — a user-facing design call, deliberately out of this increment.
- **Broader VyOS live-state drift detection.** The controller re-applies on a spec/source change and re-establishes the guest source filter when it is found down (the D8 maintain-invariant), but it does not periodically re-apply the FULL managed config to catch arbitrary guest-side drift (a hand-edit, or a reboot that lost state) beyond the source filter. A periodic full-reapply — or a config-hash reconcile against the live guest — is deferred.

## External repositories (hand-offs, not monorepo work)

- **Portal / dashboard image + cloud-init lock-step.** A consumer that advances the app's boot image must regenerate first-boot cloud-init in the same step — the image and cloud-init are a matched pair (see `docs/image-lifecycle.md`). This is an external-repo dependency and must be filed against the consuming dashboard/portal, not this monorepo.

## Later-phase pointers

The routed Phase-1 build deliberately reuses one VyOS core (behind the controller, no backend/materializer abstraction) so later phases refactor when they land rather than paying for seams up front.

- **Phase 2 — `site-gateway` (NAT).** Re-open the NAT / DNAT / port-forward design as a separate `site-gateway` app for the masquerade + inbound-port-forward case that `site-router` deliberately does not cover.
- **Phase 3 — WireGuard backend.** An alternative tunnel backend alongside IPsec, reusing the same VyOS core and the same SiteRouter app contract.
- **Phase 4 — HA / per-tenant egress IP / initiator model.** Gateway high-availability (VRRP), a per-tenant egress IP, and an initiator model (the gateway dials out rather than only responding).
