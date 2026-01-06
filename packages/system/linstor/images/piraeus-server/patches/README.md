# LINSTOR Server Patches

Custom patches for piraeus-server (linstor-server) v1.32.3.

- **adjust-on-resfile-change.diff** — Use actual device path in res file during toggle-disk; fix LUKS data offset
  - Upstream: [#473](https://github.com/LINBIT/linstor-server/pull/473), [#472](https://github.com/LINBIT/linstor-server/pull/472)
- **allow-toggle-disk-retry.diff** — Allow retry and cancellation of failed toggle-disk operations
  - Upstream: [#475](https://github.com/LINBIT/linstor-server/pull/475)
- **force-metadata-check-on-disk-add.diff** — Create metadata during toggle-disk from diskless to diskful
  - Upstream: [#474](https://github.com/LINBIT/linstor-server/pull/474)
- **skip-adjust-when-device-inaccessible.diff** — Skip DRBD adjust/res file regeneration when child layer device is inaccessible
  - Upstream: [#471](https://github.com/LINBIT/linstor-server/pull/471)
