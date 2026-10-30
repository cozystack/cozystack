## Release Candidates

Release candidates are Cozystack versions that introduce new features and are published before a stable release. Their purpose is to help validate stability before finalizing a new feature release.

Release candidates are given numbers `vX.Y.0-rc.N`, for example, `v1.2.0-rc.1`. The first one is cut from `main`, once all major features for the upcoming release have merged and the release enters its testing phase.

**Cutting the first rc freezes the line.** [`cut-prerelease.yaml`](../../.github/workflows/cut-prerelease.yaml) creates `release-X.Y` at the tagged commit, and from that point the release's content is closed:

- Every later cut for that line — `rc.2`, `rc.3`, an `alpha`/`beta`, or a patch-line rc — must be dispatched from `release-X.Y`. A dispatch from `main` is refused.
- Fixes reach the release only by cherry-pick or backport onto `release-X.Y` (see [Backports](./backports.md)).
- `main` reopens immediately for the next minor, so feature work never has to wait for the release to ship.

This is what makes an rc mean something. Previously `release-X.Y` was created only when the stable release merged, so `rc.2` was cut from `main`'s tip and silently absorbed everything that had landed since `rc.1` — the shipped release could contain code that no rc had ever validated.

**Un-freezing is manual and deliberate.** If an `rc.1` turns out to be unusable and the line has to be re-cut from `main`, delete `release-X.Y` by hand first; the refuse step blocks every other route back to a `main` dispatch, which is the point of it. Nothing automates the deletion, and nothing should — throwing away a frozen line is a decision worth making explicitly.

Each RC contributes to a cumulative set of release notes that will be finalized when `vX.Y.0` is released. After testing, if no critical issues remain, the last good rc is **promoted** to the regular release `vX.Y.0` — the exact rc images retagged by digest, never rebuilt and never tagged from a later commit (see [Regular Releases](./regular-releases.md#regular-releases)). Promotion cuts the write-once `vX.Y.0` tag at the promote PR's merge commit, fast-forwards the `release-X.Y` maintenance branch the freeze already created, and opens the way for patch releases.
