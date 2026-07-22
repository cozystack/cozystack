# site-router follow-ups (deferred by decision)

This is the consolidated list of work surfaced during the Phase-1 `site-router` build and deferred by explicit decision (see `PLAN.md` / `DECISIONS.md`). Nothing here blocks the Phase-1 increment; each item is tracked so no implicit debt is lost. It is the source list the maintainer turns into filed issues — the app itself ships without them.

## Image and build

- **Reproducible in-repo VyOS build (landed).** The pipeline is implemented in `packages/system/vyos-router-image` (pinned `vyos-build` flavor + containerDisk Makefile), wired into CI as the `build-vyos` job, and consumed by the now-enabled `vyos-router` entry in `packages/system/vm-default-images/values.yaml` (a digest-pinned OCI containerDisk via CDI's registry importer, digest stamped into `images/vyos-router-disk.tag`). See `docs/image-lifecycle.md`.
- **Publish + validate the golden image (maintainer action).** Two things still require a CI run (a gated push): letting `build-vyos` publish the containerDisk to GHCR and stamp the real digest into the committed placeholder `.tag`, and the empirical boot-conformance proof against a real gateway (cloud-init applies the seed, the HTTPS `/configure` REST answers, eth0 DHCPs, nginx serves :443, the firewall seed applies) — the deferred site-router e2e covers the latter once the image is published.

## Security hardening

- **VyOS 1.5-rolling firewall-syntax live validation.** The guard structure (management firewall, tunnel-ingress source filter, forward default-deny, IPsec-match jump, Boundary-A management drop, MSS clamp, forced ESP-in-UDP encapsulation) is implemented and unit-tested, but the exact VyOS 1.5 leaf syntax is carried behind single-point helpers and marked `TODO(T13)`/`TODO(T06)` in `internal/vyos/render/render.go` (and the controller's `tunnelIngressRulesetPath`). The flat `firewall {name,forward,input}` family and the `ipsec match-ipsec`/`match-none` matchers may differ on the shipped image and must be validated live, then all helpers flipped together in lockstep. Blocked on the published golden image.
- **Tunnel-ingress world-egress destination-constraint live validation.** The destination-constrained accept structure (source ∈ remoteCIDR AND destination ∈ tenant network) is in place; the e2e negative-security suite must prove on the live gateway that a packet with a valid remote source but a world / non-tenant destination is dropped, not just an undeclared-source packet. Part of the same live-validation pass.
- **Scoped `port_security`.** Replace the full gateway-port relaxation with declared-prefix scoping once kube-ovn supports a CIDR in allowed-address-pairs (`ovn.kubernetes.io/aaps`). Track upstream kube-ovn CIDR-AAP support. The guest tunnel-ingress source filter and its negative tests stay regardless of this change.
- **Tenant-baseline Cilium exclusion for the gateway (Boundary B hardening).** Cilium allow rules are additive, so the tenant baseline's broad allow-external/internal-communication ingress still reaches the gateway endpoint alongside the gateway-ingress policy. Fully realising Boundary B requires excluding the gateway endpoint from the tenant baseline — a shared `packages/apps/tenant` change with broad blast radius. In Phase 1 the guest VyOS firewall is the real backstop; this hardening makes the pod-boundary layer authoritative too.
- **Controller-namespace API key + post-boot rotation.** The management-API token is seeded via first-boot cloud-init. A controller-namespace key with post-boot rotation to a value that never appeared in cloud-init would remove the at-rest-in-user-data exposure. Deferred, matching the reference implementation's acknowledged trade-off.

## Observability

- **Tunnel byte / rekey counter metrics.** The controller surfaces tunnel and BGP up/down state, but per-tunnel byte counters and rekey counts are not yet exported — they need a guest-command change (to fetch the counters) plus a parser addition in `internal/vyos`. Deferred; the up/down gauges cover Phase-1 acceptance.

## Networking

- **`_cluster.pod-cidr` derivation for `managementCIDR`.** The chart's `managementCIDR` and the controller's `--management-cidr` both default to the kube-ovn default pod CIDR `10.244.0.0/16` and must be kept consistent by hand; a cluster with a custom `networking.podCIDR` needs both set manually or the management firewall rejects the real controller source. Deriving the value from an engine-injected `_cluster.pod-cidr` (as the LoadBalancer class is already injected via `_cluster."load-balancer-class"`) would make custom-pod-CIDR clusters work without manual configuration and remove the drift-locks-out-the-controller footgun.
- **IPsec local-address / LB tunnel-address wiring.** The controller leaves the IPsec `local-address` unset so VyOS auto-detects it (the Phase-1 responder model). Wiring the tunnel LoadBalancer address into the render as an explicit local-address is a documented follow-up.

## External repositories (hand-offs, not monorepo work)

- **Portal / dashboard image + cloud-init lock-step.** A consumer that advances the app's boot image must regenerate first-boot cloud-init in the same step — the image and cloud-init are a matched pair (see `docs/image-lifecycle.md`). This is an external-repo dependency and must be filed against the consuming dashboard/portal, not this monorepo.

## Later-phase pointers

The routed Phase-1 build deliberately reuses one VyOS core (behind the controller, no backend/materializer abstraction) so later phases refactor when they land rather than paying for seams up front.

- **Phase 2 — `site-gateway` (NAT).** Re-open the NAT / DNAT / port-forward design as a separate `site-gateway` app for the masquerade + inbound-port-forward case that `site-router` deliberately does not cover.
- **Phase 3 — WireGuard backend.** An alternative tunnel backend alongside IPsec, reusing the same VyOS core and the same SiteRouter app contract.
- **Phase 4 — HA / per-tenant egress IP / initiator model.** Gateway high-availability (VRRP), a per-tenant egress IP, and an initiator model (the gateway dials out rather than only responding).
