<!--
  DRAFT PR body for the site-router Phase-1 increment. This file is a hand-off
  artifact for the maintainer to paste into `gh pr create --body-file`; it is not
  itself the PR. Creating the PR is a gated action performed by the operator.
-->
# feat(site-router): add routed site-to-site IPsec gateway app (Phase 1)

## Summary

Adds `site-router`, a catalog app for **routed (L3), source-IP-preserving tenant site-to-site connectivity** over an IKEv2 IPsec tunnel terminated in a VyOS KubeVirt gateway VM. The chart materializes the gateway (VM + boot disk + tunnel LoadBalancer Service + credential Secrets + Cilium guards); a new `site-router-controller` mediates the pieces the chart cannot express — deny-set validation of the tunnel's remote networks, the kube-ovn return-route annotation, gateway-port `port_security` relaxation gated on the guest source filter, the live VyOS configuration push over the management API, tunnel/BGP observability, and status. No new tenant CRD: the `apps.cozystack.io/SiteRouter` app instance is the whole contract.

This is a port + productization of the reference implementation's VyOS router into the open-source monorepo, subset to the routed feature set and aligned to the catalog-app model. NAT/DNAT (a future `site-gateway`) and HA/VRRP are deliberately out of scope.

## What's included

- App chart `packages/apps/site-router` — gateway `VirtualMachine` (512/4096 blockSize for DRBD), boot DataVolume, tunnel `Service type: LoadBalancer` (UDP 500/4500, native `loadBalancerClass`), PSK + RBAC-isolated api-key Secrets, first-boot cloud-init, WorkloadMonitor, and two net-new Cilium policies (gateway `egressDeny` + gateway ingress).
- `site-router-controller` (`internal/controller/siterouter`, `cmd/site-router-controller`) wired into the platform — watches SiteRouter HelmReleases + gateway pods, runs the ordered mediation pipeline, finalizer-restores state on delete.
- VyOS core library `internal/vyos` (client/parse/observation) + routed render `internal/vyos/render` (interfaces, management firewall, IPsec forced-UDP, static routes, BGP, MSS clamp, tunnel-ingress source filter, forward default-deny, Boundary-A drop).
- Shared pure `internal/siterouter/denyset` validator + a SiteRouter-scoped admission check (`pkg/registry/apps/application`) that reject a cluster-overlapping `remoteCIDR` identically at apply time and reconcile time.
- Docs: `README.md` (prose + generated params), `docs/security-model.md`, `docs/image-lifecycle.md`, `docs/followups.md`.
- Tests: helm-unittest chart render (9 suites) + Go unit tests (render subset/security/net-new, deny-set, CNI mediation, status mapping, VyOS push, admission).

## Phase-1 acceptance checklist (honest status)

Status legend: **done** = implemented and unit-tested in this PR; **deferred-to-empirical** = implemented but its live proof needs a booted gateway (blocked on the published golden image + the e2e run); **follow-up** = tracked, out of Phase-1 scope (`docs/followups.md`).

| # | Acceptance item | Status |
|---|------------------|--------|
| 1 | Tenant deploys from the catalog; VyOS gateway VM boots (512-native on DRBD), dual-homed on the tenant pod network | done (chart render + blockSize, unit-tested) / deferred-to-empirical (live boot needs the published image) |
| 2 | IKEv2 IPsec tunnel over forced ESP-in-UDP, MSS clamp by default | done (render + unit tests: forced encapsulation unconditional, clamp 1320→1280) / deferred-to-empirical (live establishment) |
| 3 | Decrypted traffic L3-forwarded, source IP preserved; whole-subnet + ICMP for TCP/UDP/ICMP/SCTP | done (local selector 0.0.0.0/0 + forward filter) / deferred-to-empirical (live source-preservation proof) |
| 4 | Tunnel endpoint via native `Service type: LoadBalancer` (UDP) with cluster `loadBalancerClass` | done (unit-tested) / deferred-to-empirical (live LB assignment) |
| 5 | Controller mediation: deny-set, namespace routes, gateway-only `port_security` relax, source filter, status, delete-restore | done (unit-tested) / deferred-to-empirical (kube-ovn live port_security toggle; D8 fallback documented) |
| 6 | Security guards: source allow-list, Cilium `egressDeny` (169.254 + mgmt), forward default-deny, two-boundary API isolation, api-key not tenant-readable | done (unit-tested) / follow-up (Boundary-B additive-ingress residual; VyOS 1.5 syntax live-validation) |
| 7 | Tunnel-state observability (SA up/down, rekey, counters) | done (up/down + BGP gauges) / follow-up (byte + rekey counters need a guest-command + parser change) |
| 8 | Negative-security acceptance suite passes (undeclared source / other tenant / node / API / metadata / mgmt-API / world dropped; declared source → tenant dest passes, source preserved) | deferred-to-empirical (the e2e gate; blocked on the published golden image for a live run) |
| 9 | helm-unittest + Go unit + Chainsaw e2e (two-VM) green in CI | done (helm-unittest + Go unit) / deferred-to-empirical (Chainsaw e2e suite committed but deferred from CI until the VyOS golden image ships; live two-VM run) |

The negative-security suite (item 8) is the Phase-1 acceptance gate and is authored as a Chainsaw e2e; its **live** run against a real two-VM topology is blocked on the published cozystack-owned VyOS golden image (see follow-ups). The VyOS-version-specific firewall leaf syntax is carried behind single-point helpers with `TODO(T13)` markers and is flipped in lockstep once validated live.

## Follow-ups (drafts — to be filed by the maintainer)

Full detail in `packages/apps/site-router/docs/followups.md`. Consolidated list:

- Publish the cozystack-owned VyOS golden image (unblocks the default install + the e2e/empirical run).
- Reproducible in-repo VyOS build (pin snapshot, `vyos-build`, publish like the Talos image; then pin URL + `@sha256`).
- VyOS 1.5-rolling firewall-syntax live validation (the `TODO(T13)` markers, flip all helpers together).
- Tunnel-ingress world-egress destination-constraint live validation (e2e proves valid-source/world-dest is dropped).
- Scoped `port_security` (pending upstream kube-ovn CIDR-AAP support).
- Tenant-baseline Cilium exclusion for the gateway (Boundary-B hardening).
- Controller-namespace API key + post-boot rotation.
- Tunnel byte / rekey counter metrics (guest-command + parser change).
- `_cluster.pod-cidr` derivation for `managementCIDR` (custom-pod-CIDR clusters without manual config).
- IPsec local-address / LB tunnel-address wiring.
- Portal / dashboard image + cloud-init lock-step (external-repo hand-off).
- Phase 2 `site-gateway` (NAT) / Phase 3 WireGuard backend / Phase 4 HA + per-tenant egress IP + initiator model.

## Repo hygiene (pre-merge)

- The `feat(site-router):` title auto-applies `kind/feature`. To auto-apply `area/networking` (rather than falling to `area/uncategorized`), add `'site-router': 'area/networking'` to the scope mapping in `.github/workflows/pr-labeler.yaml` (alongside the existing `vpn`/`gateway`/`kube-ovn` entries) — `area/networking` already exists in `.github/labels.yml`.
- Add a changelog entry for the release that ships `site-router`.
