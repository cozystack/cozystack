# LINSTOR Server Patches

Custom patches for piraeus-server (linstor-server) v1.35.3.

- **fix-k8s-create-already-exists.diff** — Adopt the existing object when a create in the Kubernetes CRD backend returns 409 AlreadyExists. The HTTP client retries a POST after a 5xx or a dropped connection, so a create the apiserver already persisted comes back as 409; a single-object transaction has no rollback entry, the key is dropped from memory while the CR stays, and every later write of that key fails until the controller restarts. Seen as a PV stuck in `Released` because linstor-csi could not set `BalanceResourcesEnabled` on the resource definition.
  - Source PR: [#531](https://github.com/LINBIT/linstor-server/pull/531)
  - Backported from commit: [`8badf8f05`](https://github.com/lexfrei/linstor-server/commit/8badf8f0526ff3b1b391857e4a046ed46b95a79c)
- **retry-adjust-after-stale-bitmap.diff** — Retry `drbdadm adjust` after detaching a stale local bitmap state
  - Source PR: [#491](https://github.com/LINBIT/linstor-server/pull/491)
  - Backported from commit: [`51ae50a84`](https://github.com/kvaps/linstor-server/commit/51ae50a84dcb98093f543b819652c750a94d96c9)
  - Rebased onto v1.35.3, where `DrbdAdm` gained the `DFLT_WINDOWS_TIMEOUT_MS` constant; `DrbdadmAdjust` still receives the failed `OutputData` through `ExtCmdFailedException`, so its own retry on "Resize not allowed during resync" keeps working on top of this one.
- **native-protoc.diff** — Download the protoc build for the build host's architecture instead of always `linux-x86_64`. The builder stage runs on `$BUILDPLATFORM`, so on a native arm64 builder the x86_64 protoc cannot execute and `./gradlew generateJava` fails with `Syntax error: ")" unexpected`.
  - Not proposed upstream yet.

Dropped on the move from v1.33.3, because v1.35.3 already carries them:

- `allow-toggle-disk-retry.diff` — upstream [`3d97f71c9`](https://github.com/LINBIT/linstor-server/commit/3d97f71c95a493588d3d521c63eac4d846935fb3), v1.34.0
- `fix-luks-header-size.diff` — upstream [`ccc85fbd2`](https://github.com/LINBIT/linstor-server/commit/ccc85fbd2c65f0b97c52403fa80f1efdb886ec4e) and [`71b601554`](https://github.com/LINBIT/linstor-server/commit/71b601554f41bcb50cd5bd06989c5b0d3a814acd), v1.34.0
- `snapshot-restore-drbd-initialized.diff` — upstream [`9d8cdf511`](https://github.com/LINBIT/linstor-server/commit/9d8cdf5118338fc72a34bb99799e452e031ea8f6), v1.34.0
- `fix-duplicate-tcp-ports.diff` — upstream [`521c6d211`](https://github.com/LINBIT/linstor-server/commit/521c6d21121dbbcc26cf6eda723c24a5b34889be) (from [#476](https://github.com/LINBIT/linstor-server/pull/476)), v1.34.0
- `retry-secondary-after-mkfs.diff` — upstream [`677789c02`](https://github.com/LINBIT/linstor-server/commit/677789c028ac), v1.34.0
- `fix-min-io-probe-device-wait.diff` — upstream [`6e5557839`](https://github.com/LINBIT/linstor-server/commit/6e5557839c911c7a1db240dcb6fc348383a74266) (from [#528](https://github.com/LINBIT/linstor-server/pull/528)), v1.35.1
