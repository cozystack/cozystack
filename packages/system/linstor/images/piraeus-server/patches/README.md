# LINSTOR Server Patches

Custom patches for piraeus-server (linstor-server) v1.32.3.

- **adjust-on-resfile-change.diff** — Use actual device path in res file during toggle-disk; fix LUKS data offset
  - Upstream: [#473](https://github.com/LINBIT/linstor-server/pull/473), [#472](https://github.com/LINBIT/linstor-server/pull/472)
- **allow-toggle-disk-retry.diff** — Allow retry and cancellation of failed toggle-disk operations
  - Upstream: [#475](https://github.com/LINBIT/linstor-server/pull/475)
- **force-metadata-check-on-disk-add.diff** — Create metadata during toggle-disk from diskless to diskful
  - Upstream: [#474](https://github.com/LINBIT/linstor-server/pull/474)
- **fix-duplicate-tcp-ports.diff** — Prevent duplicate TCP ports after toggle-disk operations
  - Upstream: [#476](https://github.com/LINBIT/linstor-server/pull/476)
- **skip-adjust-when-device-inaccessible.diff** — Fix resources stuck in StandAlone after reboot, Unknown state race condition, and encrypted resource deletion
  - Upstream: [#477](https://github.com/LINBIT/linstor-server/pull/477)
