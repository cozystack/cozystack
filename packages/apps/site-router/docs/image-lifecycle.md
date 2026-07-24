# site-router VyOS appliance image lifecycle

This note records where the `site-router` gateway VM's boot image comes from, what it must carry, and the rule that keeps the image and its first-boot configuration in sync. It is the provenance record referenced from the (commented-out) `vyos-router` entry in `packages/system/vm-default-images/values.yaml`.

## What the image is

The gateway boots from a **VyOS 1.5-rolling** qcow2 appliance disk. VyOS rolling releases are date-stamped snapshots (`1.5-rolling-YYYY.MM.DD`); the exact snapshot is pinned when the artifact is published (see T14 below). VyOS is a Debian-derived, GPL-licensed router OS and is freely redistributable, so cozystack can host and republish its own copy.

The appliance disk carries three things baked in so it boots unattended under KubeVirt:

- **NoCloud cloud-init datasource** — KubeVirt seeds first-boot configuration via a `cloudInitNoCloud` volume, so the image must honor the NoCloud datasource (the prebuilt appliance also ships the OpenNebula datasource, which is harmless under KubeVirt).
- **cloud-init `write_files`** — first-boot config (interfaces, management firewall, and the management-API seed) is applied from NoCloud user-data. The seed is delivered as a `write_files` of a complete, VyOS-serialized `config.boot`, deliberately **not** the VyOS-native `vyos_config_commands` module nor a `runcmd` script — see [Config delivery](#config-delivery-why-write_files-and-why-cloud-init-at-all) below.
- **VyOS HTTPS API** — enabled in the image so the controller can push configuration to the running gateway once it is reachable.

## How it is consumed (Phase 1)

Consumption is via CDI, matching how every other OS image in the catalog is consumed:

- The image is registered as a golden image in `packages/system/vm-default-images` (namespace `cozy-public`). Each entry there becomes a CDI `DataVolume`/PVC named `vm-default-images-<name>`.
- The `site-router` VM disk clones that golden image by name, the same way `vm-disk` clones any other `vm-default-images-<name>` PVC.

The `vm-default-images` entry is committed **enabled**, consuming the in-repo-built appliance as a digest-pinned OCI containerDisk via CDI's registry importer (`source.registry`, `docker://…@sha256:…`). The digest lives in `packages/system/vm-default-images/images/vyos-router-disk.tag`, stamped by the image build (the `build-vyos` CI job) — mirroring how the Talos build stamps `bootbox/images/matchbox.tag`. `vm-default-images` is an **opt-in** bundle package (rendered only when listed in `bundles.enabledPackages`), and a not-yet-published or placeholder-digest import is a background CDI retry rather than a HelmRelease failure, so committing the entry enabled is safe even before the first CI publish. A registry containerDisk (rather than an HTTP qcow2) is what makes the boot disk pinnable by digest — CDI's HTTP importer cannot verify a `sha256`.

## How an update propagates

Two kinds of change reach a running gateway, and only one of them drops the tunnel.

**Day-2 configuration changes do not restart the VM and do not drop the tunnel.** The controller pushes the rendered VyOS configuration over the live HTTPS management API (`POST /configure`) as a single atomic transaction, so a change to `remoteCIDRs`, static routes, BGP or the tunnel parameters is applied in place; a no-op reconcile (config hash unchanged) makes no HTTP call at all. Established IPsec SAs survive a configuration push.

**An image change restarts the VM, which is the one routine tunnel-dropping operation.** Bumping the boot image means re-materializing the VM disk from the new golden image and restarting the gateway VM. A VM restart tears down every running IPsec SA, so the tunnel is down from the moment the VM stops until it reboots, cloud-init re-seeds the base configuration, the controller re-pushes the live configuration, and the remote peer re-establishes (the gateway is the responder, so the remote peer must re-dial). No day-2 configuration change causes this — only an image (or VM template) change does. Plan an image bump as a maintenance window for the affected tunnels.

Because the boot image and its first-boot cloud-init are a matched pair (see the invariant below), an image bump also re-runs first-boot cloud-init. The `cloudInitSeed` value is mixed into the VM firmware UUID: change it to force a first-boot cloud-init re-run, or clear it to preserve an existing VM's UUID (and skip the re-run) across a re-render that does not intend to reconfigure from scratch.

## Provenance and the no-internal-infra rule

The appliance is now built reproducibly in-repo (see the section below), so the committed default is a **cozystack-owned** artifact with no dependency on private infrastructure. No internal or third-party hosting URL for the appliance appears anywhere in this repository; the committed reference is a cozystack-owned OCI containerDisk under the `cozystack/cozystack` GHCR namespace, pinned by digest in `vyos-router-disk.tag`, which the `build-vyos` CI job publishes and stamps.

## T14 — reproducible in-repo build (pipeline landed)

The reproducible in-repo build is implemented in `packages/system/vyos-router-image`: a pinned `vyos-build` flavor (`flavors/vyos-router.toml`) produces the qcow2, `hack/build-qcow2.sh` drives the pinned build container, and the `Makefile` `image` target packages the qcow2 as a KubeVirt containerDisk, pushes it, and stamps the digest into `vm-default-images/images/vyos-router-disk.tag`. The `build-vyos` job in `.github/workflows/pull-requests.yaml` runs it in CI (mirroring `build-talos`) and carries the stamped digest into the installer artifact via the patch-fragment mechanism.

The flavor bakes the conformance fixes the reused (stripped) appliance lacked — a working cloud-init (NoCloud datasource forced, so `write_files` seeds first boot), the HTTPS REST API server, `/var/log/nginx`, and eth0-over-DHCP as an image default — plus a baked `config.boot.default` (the cloud-init VyOS module aborts without one; vyos.dev/T7206). The pins are the `vyos-build` container digest and git ref plus the snapshot version label; the upstream rolling apt mirror floats, so the build is pinned-inputs / best-effort-reproducible, not bit-identical.

Two things remain a **maintainer action**, both requiring a CI run (a push, gated to the maintainer): (1) let `build-vyos` publish the containerDisk to GHCR and stamp the real digest — the committed `.tag` carries a placeholder until then; (2) the empirical boot-conformance proof (cloud-init applies the seed, the HTTPS `/configure` REST answers, eth0 has a DHCP address, nginx serves :443, the firewall seed is applied), which the deferred site-router e2e performs once the image is published.

## Config delivery: why `write_files`, and why cloud-init at all

The first-boot seed is delivered as a cloud-init `write_files` that drops a complete, VyOS-serialized `config.boot` at `/opt/vyatta/etc/config/config.boot`, **not** via the VyOS-native `vyos_config_commands:` key and **not** via a `runcmd` script. This is forced by the stripped cloud-init module set on the pinned image, established empirically across three boot-conformance iterations on image `sha256:a3bfc9fe…` (2026-07): (1) `vyos_config_commands:` (the `cc_vyos_userdata` module) is enabled but serializes single-value leaf nodes into `config.boot` as the invalid braced `node { value }` instead of the valid inline `node "value"`, so boot-time activation rejects the whole file; (2) `runcmd:` / `bootcmd:` never run because `cloud_final_modules` is empty on this image; (3) `write_files` is the one config-capable module actually enabled (cloud_config stage), it runs before the `vyos-router` activation, and the file it drops is what gets activated — validated hands-off (config activates clean, eth0 DHCPs, the HTTPS API answers on :443 with the seeded key). The `config.boot` template base was captured verbatim from VyOS's own `save`, so its serialization is correct by construction; the chart substitutes only host-name, the api-key and managementCIDR. Do not hand-edit the tree into `node { value }` form and do not switch back to `vyos_config_commands:` / `runcmd:` without re-verifying the module set and serializer on the then-pinned image. The authoritative copy of this note lives next to the template in `templates/_helpers.tpl`.

Shipping the whole `config.boot` (not just a seed) means the chart now owns every baked default that must survive first boot — the locked `vyos` login (`encrypted-password "*"`) with no `service ssh`, the ttyS0 console, and the `// vyos-config-version:` / `// Release version:` footer that pins the config to the image build (a mismatch triggers a VyOS config migration that can reformat or reject the file). These are asserted in `tests/secret_cloudinit_test.yaml` and, being image-specific, are the sharpest edge of the atomic-advance invariant below.

cloud-init is required for the bootstrap even though the controller configures the gateway over the API: the controller's HTTPS `/configure` channel can only answer once the gateway already has an IP, the API running, and the per-instance api-key installed — it cannot bootstrap itself. cloud-init NoCloud user-data (a per-instance Secret) is the idiomatic KubeVirt channel for that day-0 step. The only cloud-init-free alternative — bootstrapping via qemu-guest-agent `guest-exec` — reopens a remote-exec surface on a deliberately-locked-down appliance (no SSH, login locked) and needs `guest-exec` RBAC plus the KubeVirt feature gate plus a security review; it was a deliberate Phase-1 non-goal.

## Invariant — image and cloud-init advance atomically

The boot image and the first-boot cloud-init it consumes are a matched pair: the config schema, the management-API seed layout, and any VyOS-version-specific config syntax or cloud-init behavior are tied to the exact image snapshot. Whenever the pinned image reference changes, the cloud-init contract must be re-validated and advanced in the same change — never bump one without the other. Treat the image digest and the cloud-init template as a single unit that moves together. Concretely: because the chart now ships the whole `config.boot` (captured from the image's own `save`), an image bump requires re-capturing `config.boot` from the new image and updating the `// vyos-config-version:` / `// Release version:` footer — not merely re-testing a seed.
