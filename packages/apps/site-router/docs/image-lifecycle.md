# site-router VyOS appliance image lifecycle

This note records where the `site-router` gateway VM's boot image comes from, what it must carry, and the rule that keeps the image and its first-boot configuration in sync. It is the provenance record referenced from the (commented-out) `vyos-router` entry in `packages/system/vm-default-images/values.yaml`.

## What the image is

The gateway boots from a **VyOS 1.5-rolling** qcow2 appliance disk. VyOS rolling releases are date-stamped snapshots (`1.5-rolling-YYYY.MM.DD`); the exact snapshot is pinned when the artifact is published (see T14 below). VyOS is a Debian-derived, GPL-licensed router OS and is freely redistributable, so cozystack can host and republish its own copy.

The appliance disk carries three things baked in so it boots unattended under KubeVirt:

- **NoCloud cloud-init datasource** — KubeVirt seeds first-boot configuration via a `cloudInitNoCloud` volume, so the image must honor the NoCloud datasource (the prebuilt appliance also ships the OpenNebula datasource, which is harmless under KubeVirt).
- **cloud-init with VyOS `vyos_config_commands`** — first-boot config (interfaces, management firewall, and the management-API seed) is applied from NoCloud user-data.
- **VyOS HTTPS API** — enabled in the image so the controller can push configuration to the running gateway once it is reachable.

## How it is consumed (Phase 1)

Consumption is via CDI, matching how every other OS image in the catalog is consumed:

- The image is registered as a golden image in `packages/system/vm-default-images` (namespace `cozy-public`). Each entry there becomes a CDI `DataVolume`/PVC named `vm-default-images-<name>`.
- The `site-router` VM disk clones that golden image by name, the same way `vm-disk` clones any other `vm-default-images-<name>` PVC.

The `vm-default-images` entry is committed **disabled (commented out)** until the artifact is published, because the chart renders every list entry unconditionally as an HTTP import — a live entry pointing at a not-yet-published URL would create a perpetually-failing DataVolume for anyone who opts into the (opt-in) golden-image collection.

## Provenance and the no-internal-infra rule

The prebuilt appliance originated outside this repository, but the committed default here must be a **cozystack-owned** artifact so the OSS base has no dependency on private infrastructure. No internal or third-party hosting URL for the appliance appears anywhere in this repository; the committed URL is a cozystack-owned placeholder under the `cozystack/cozystack` GitHub releases namespace that the maintainer populates at publish time.

## T14 follow-up — reproducible in-repo build

Publishing the cozystack-owned artifact is a **maintainer action**, tracked as the T14 follow-up. It covers: pin a specific VyOS rolling snapshot, build the qcow2 reproducibly in-repo (via `vyos-build`, mirroring the in-repo Talos disk pipeline), publish it under cozystack ownership, and then uncomment the `vyos-router` entry in `vm-default-images/values.yaml` with the exact snapshot URL and a `@sha256` digest pin. Until T14 lands, the entry stays disabled.

## Invariant — image and cloud-init advance atomically

The boot image and the first-boot cloud-init it consumes are a matched pair: the config schema, the management-API seed layout, and any VyOS-version-specific `vyos_config_commands` are tied to the exact image snapshot. Whenever the pinned image reference changes, the cloud-init contract must be re-validated and advanced in the same change — never bump one without the other. Treat the image digest and the cloud-init template as a single unit that moves together.
