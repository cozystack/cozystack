# LINSTOR Server Patches

Custom patches for piraeus-server (linstor-server) v1.33.2.

- **allow-toggle-disk-retry.diff** — Backport maintainer implementation of toggle-disk retry/abort
  - Source PR/comment: [#475](https://github.com/LINBIT/linstor-server/pull/475), [maintainer note](https://github.com/LINBIT/linstor-server/pull/475#issuecomment-3949630419)
  - Backported from upstream commit: [`3d97f71c9`](https://github.com/LINBIT/linstor-server/commit/3d97f71c95a493588d3d521c63eac4d846935fb3)
- **fix-duplicate-tcp-ports.diff** — Preserve DRBD TCP ports during toggle-disk and avoid redundant `ensureStackDataExists()`
  - Source PR/review: [#476](https://github.com/LINBIT/linstor-server/pull/476), [review suggestion](https://github.com/LINBIT/linstor-server/pull/476#discussion_r3007725079)
  - Backported from commits: [`79d6375c5`](https://github.com/kvaps/linstor-server/commit/79d6375c55d6181b35a7b7f0fe8dbdfb86e126cd), [`bcc89902f`](https://github.com/kvaps/linstor-server/commit/bcc89902f4f61ac1589dd07ebb7f5aae1935370d)
- **fix-luks-header-size.diff** — Account for LUKS2 `--offset`, metadata/keyslots sizing, and device `optimal_io_size`
  - Source PR/comment: [#472](https://github.com/LINBIT/linstor-server/pull/472), [maintainer note](https://github.com/LINBIT/linstor-server/pull/472#issuecomment-3949687603)
  - Backported from commits: [`ccc85fbd2`](https://github.com/LINBIT/linstor-server/commit/ccc85fbd2c65f0b97c52403fa80f1efdb886ec4e), [`71b601554`](https://github.com/LINBIT/linstor-server/commit/71b601554f41bcb50cd5bd06989c5b0d3a814acd)
  - Note: upstream commit [`3d0402a0c`](https://github.com/LINBIT/linstor-server/commit/3d0402a0c25f0a4b57b380321f10e89982f26e7a) is already included in `v1.33.1`
- **retry-adjust-after-stale-bitmap.diff** — Retry `drbdadm adjust` after detaching a stale local bitmap state
  - Source PR: [#491](https://github.com/LINBIT/linstor-server/pull/491)
  - Backported from commit: [`51ae50a84`](https://github.com/kvaps/linstor-server/commit/51ae50a84dcb98093f543b819652c750a94d96c9)
- **retry-secondary-after-mkfs.diff** — Retry `drbdadm secondary` after mkfs when DRBD reports the device as held open by an external probe (Talos block-controller, udev, multipathd, etc.). Without this retry, a transient `Device is held open by someone` aborts resource initialization, leaves the satellite in an intermediate state, and prevents the controller from receiving the final `UpToDate` event — orphan PVs in `Released` then cannot be cleaned up.
  - Related upstream issue: [#268](https://github.com/LINBIT/linstor-server/issues/268)
  - Related upstream issue: [drbd #74](https://github.com/LINBIT/drbd/issues/74) (same EBUSY pattern with multipathd)
