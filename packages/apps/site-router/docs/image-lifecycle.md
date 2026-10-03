# site-router appliance image lifecycle

This note records where the `site-router` gateway VM's boot image comes from, what it must carry, and the rule that keeps the image and its first-boot configuration in sync.

## What the image is

The gateway boots the KubeVirt disk of a [barerouter](https://github.com/aenix-io/barerouter) release: a router image built from the public VyOS rolling sources with the VyOS name, trademarks and logo artwork removed, installed onto a disk and turned into a KubeVirt appliance. barerouter is published by Aenix under its own organisation, together with the image's SBOM, the list of every installed package and where the source of each one is. cozystack builds no VyOS-derived image and hosts none; it pins a barerouter release by digest.

The appliance used to be built here, first by a live-build against the VyOS rolling repository and then from a published VyOS Stream ISO. Stream images are VyOS's pre-built binaries and carry a EULA that does not allow repackaging and republishing them, and a self-built image may be redistributed only with the VyOS branding replaced. That is barerouter's job now, in a repository of its own, so that the licensing of the image and its sources sits next to the artifact and not in this one.

The KubeVirt disk adds four things to the plain router, documented as the disk's interface in barerouter's `docs/kubevirt.md`:

- **A serial console on ttyS0.** Without it `virtctl console` is silent.
- **The appliance seed**, a systemd unit that installs the per-instance `config.boot` from the NoCloud disk before `vyos-router` reads it. The image carries no cloud-init; see [Config delivery](#config-delivery-the-appliance-seed) below.
- **`qemu-guest-agent`**, so the VMI reports `AgentConnected` and is introspectable.
- **A locked bootloader**, so the serial console cannot reach a root shell through the password reset entry. Root in that guest would remove the only compensating control the relaxed OVN port has ([security-model.md](./security-model.md)).

## How it is consumed (Phase 1)

Consumption is via CDI, matching how every other OS image in the catalog is consumed:

- Each gateway's boot `DataVolume` imports the digest-pinned OCI containerDisk directly, via CDI's registry importer (`source.registry`, `docker://…@sha256:…`). A registry containerDisk rather than an HTTP qcow2 is what makes the boot disk pinnable by digest — CDI's HTTP importer cannot verify a `sha256`.
- The import is per instance. A shared golden PVC in `cozy-public` would be the cheaper import, and earlier revisions of this feature tried both routes, but it cannot be advanced: CDI populates a `DataVolume` only at creation, so a new digest cannot replace an existing golden's contents, and every later gateway would keep booting the previous appliance while its configuration contract had already moved on. Importing per instance keeps the disk and the digest that produced it in lockstep.

The pin lives in `packages/apps/site-router/images/barerouter-disk.tag` as `ghcr.io/aenix-io/barerouter/kubevirt-disk:<version>@sha256:<digest>`. In the terms of `docs/agents/image-refs.md` it is a third-party pass-through reference: no package here builds it, and the promote, retag and mirror tooling leave it as it is. `site-router.applianceDiskUrl` consumes it: it hands CDI the digest alone, dropping the tag because CDI's `docker://` transport cannot parse a reference carrying both (`Docker references with both a tag and digest are currently not supported`), and it fails the render loudly on a `.tag` that is empty or carries no `@sha256:` digest rather than importing a reference that cannot resolve. `hack/image-refs-no-placeholder.bats` fails the build if a placeholder (all-zero) digest is ever committed, because that shape satisfies every check above while resolving nowhere.

This deliberately does **not** go through `packages/system/vm-default-images`. That package is opt-in (rendered only when listed in `bundles.enabledPackages`) and is a catalog of user-selectable OS images — neither fits a platform-owned appliance disk that one app requires in order to boot at all. Nor does it go through a golden package of its own: an operator naming only that package in `bundles.disabledPackages` would strand the app's registration on an unresolvable dependency, and the golden still could not be advanced in place. Importing from the registry per instance needs no package, no `dependsOn` edge and no shared object in `cozy-public` at all.

## Moving to a new barerouter release

1. Take the release's containerDisk reference, with its digest, from the release notes, and write it to `images/barerouter-disk.tag`.
2. Take the `config.boot` footer from the new image and put it in `templates/_helpers.tpl`, `hack/e2e-chainsaw/site-router/remote-site-b.yaml` and the `Release version` assertion in `tests/secret_cloudinit_test.yaml`; see the invariant below for how.
3. Check the rest of the chart's `config.boot` against the new image's syntax, and run the site-router e2e suite.

Existing gateways keep running on the disk they already imported and take the new appliance only when their instance is recreated, which is the tunnel-dropping restart described below.

## How an update propagates

Two kinds of change reach a running gateway, and only one of them drops the tunnel.

**Day-2 configuration changes do not restart the VM and do not drop the tunnel.** The controller pushes the rendered VyOS configuration over the live HTTPS management API (`POST /configure`) as a single atomic transaction, so a change to `remoteCIDRs`, static routes, BGP or the tunnel parameters is applied in place; a no-op reconcile (config hash unchanged) makes no HTTP call at all. Established IPsec SAs survive a configuration push.

**An image change restarts the VM, which is the one routine tunnel-dropping operation.** Bumping the boot image means re-importing the VM disk from the new appliance digest and restarting the gateway VM. A VM restart tears down every running IPsec SA, so the tunnel is down from the moment the VM stops until it reboots, the appliance seed re-installs the base configuration, the controller re-pushes the live configuration, and the remote peer re-establishes (the gateway is the responder, so the remote peer must re-dial). No day-2 configuration change causes this — only an image (or VM template) change does. Plan an image bump as a maintenance window for the affected tunnels.

Because the boot image and its first-boot configuration are a matched pair (see the invariant below), an image bump also re-runs first boot. The `cloudInitSeed` value is mixed into the VM firmware UUID: change it to force a first-boot re-run, or clear it to preserve an existing VM's UUID (and skip the re-run) across a re-render that does not intend to reconfigure from scratch.

## Config delivery: the appliance seed

The per-instance configuration is a complete, VyOS-serialized `config.boot` carried verbatim as the NoCloud `user-data`, and installed at `/opt/vyatta/etc/config/config.boot` by `barerouter-seed.service` on the barerouter disk, ordered `Before=vyos-router.service` so the file is in place before VyOS reads it.

There is no cloud-init in this appliance. The module set the chart needed is VyOS's fork, which lives in a repository that is not public, and the chart's entire use of cloud-init had already collapsed to a single `write_files` entry — the VyOS-native `vyos_config_commands:` module serialized single-value leaf nodes as the invalid braced `node { value }`, and `runcmd:`/`bootcmd:` never ran because `cloud_final_modules` was empty. A seed unit that copies one file replaces the dependency instead of working around it. KubeVirt still renders the `cloudInitNoCloud` volume, so the transport is unchanged; only the thing that reads it lives in the image.

Shipping the whole `config.boot` means the chart owns every baked default that must survive first boot — the locked `admin` login (`encrypted-password "*"`) with no `service ssh`, the ttyS0 console, and the `// vyos-config-version:` / `// Release version:` footer that pins the config to the image build. These are asserted in `tests/secret_cloudinit_test.yaml` and, being image-specific, are the sharpest edge of the atomic-advance invariant below.

A seed is still required even though the controller configures the gateway over the API, and the reason is a chicken-and-egg the API cannot break on its own: the controller's HTTPS `/configure` channel only answers once the gateway has an IP, the API running, **and** the per-instance api-key installed. That key is what the seed carries, which is also why it cannot be baked into the image. The one seed-free alternative — bootstrapping through `qemu-guest-agent` `guest-exec` — reopens a remote-exec surface on a deliberately locked-down appliance and needs `guest-exec` RBAC plus the KubeVirt feature gate plus a security review; it remains a deliberate non-goal.

Diagnostics travel separately. When `_logSerialConsole` is set (a platform-and-suite switch the aggregated API refuses from a tenant), the chart renders a second Secret and attaches it as its own `cozydiag` disk, which the same seed unit installs to `/usr/local/sbin/cozy-guest-diag.sh`. Its own disk on purpose: the `config.boot` is captured verbatim from VyOS `save`, and a diagnostics payload that broke it would leave the guest unconfigured and unreachable — the very outage the diagnostics exist to explain.

## Invariant — image and configuration advance atomically

The boot image and the `config.boot` it is seeded with are a matched pair: the config schema, the management-API seed layout, and any VyOS-version-specific syntax are tied to the exact image. Whenever the pinned image changes, the configuration contract must be re-validated and advanced in the same change — never bump one without the other.

Concretely, because the chart ships the whole `config.boot`, an image bump requires re-capturing it from the new image and updating the `// vyos-config-version:` / `// Release version:` footer, not merely re-testing a seed. The footer is the image's own component-version map plus its release version, and it is what decides whether VyOS migrates the config, reformats it, or rejects it outright. The authoritative way to obtain it is a `save` on the new image. It can also be read off the image without booting: unpack the squashfs from the release ISO and call `vyos.component_version.version_info_from_system()` against its `/usr/lib/python3/dist-packages`, which is what `save` writes; the release version is the barerouter version.

barerouter is built from the rolling line, so its component versions run ahead of the Stream `2026.03` image this chart shipped on first (`bgp@6`→`@8`, `system@31`→`@33`, `policy@8`→`@9`), and it declares `pki@1`, which that image did not, so the footer carries a `pki` entry again. A config claiming component versions the image does not carry is how a migration gets triggered rather than avoided, so the footer is always taken from the image and never edited by hand.
