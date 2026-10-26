## Release signatures and SBOM

A release cut from a tree that has [`hack/sign-release-assets.sh`](../../hack/sign-release-assets.sh) carries three files beside its other assets, which [`hack/upload-assets.sh`](../../hack/upload-assets.sh) has that script write before it uploads anything. Releases cut before it have none of them, and neither does a maintenance line until the script is backported to it (see below).

- `cozystack-source-sbom.spdx.json` is an SPDX SBOM, made with syft, of the committed source tree (the Go modules, npm lockfiles, GitHub Actions references and other manifests syft reads in it) and of the `cozypkg` binaries in the `cozypkg-*.tar.gz` assets. It does not describe what runs in the cluster: neither the container images that `packages/` pins by digest, whose SBOMs are [#2937](https://github.com/cozystack/cozystack/issues/2937) (item A2 in [#3278](https://github.com/cozystack/cozystack/issues/3278)), nor the Talos ISO, disk, kernel and initramfs images. Its name says what it covers, so that `cozystack-sbom` stays free for an SBOM of the release as a whole.
- `cozystack-checksums.txt` holds the SHA-256 of every other asset and of the SBOM.
- `cozystack-checksums.txt.sigstore.json` is a Sigstore bundle with a keyless cosign signature over the checksums. Its certificate names the workflow that published the release: `tags.yaml` for a pre-release, `promote-rc.yaml` for a stable one.

One signature covers every asset through the checksums. To check a download, verify the checksums, then the file against them, with cosign v3.1.3 or later (v2.6.5 or later on the v2 line):

```sh
cosign verify-blob cozystack-checksums.txt \
  --bundle cozystack-checksums.txt.sigstore.json \
  --certificate-identity-regexp '^https://github\.com/cozystack/cozystack/\.github/workflows/(tags\.yaml@refs/tags/v|promote-rc\.yaml@refs/heads/main$)' \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com &&
  sha256sum --check --ignore-missing cozystack-checksums.txt
```

Older cosign accepts a legacy-format bundle that carries a bare public key in place of a certificate and skips the identity and issuer checks for it ([GHSA-fx35-mq7g-6g98](https://github.com/sigstore/cosign/security/advisories/GHSA-fx35-mq7g-6g98)), so a bundle swapped in next to the download, by a mirror or a proxy, passes there. The bundles this repository writes are in the new format, but cosign picks the format from the file, and `--new-bundle-format` does not stop that on the v3 line (v3.1.2 still accepts such a bundle with it), so the cosign version is what protects the check.

The regexp pins the ref each workflow runs from: `tags.yaml` on a `v*` tag push, and `promote-rc.yaml` dispatched from `main`, the only branch it is dispatched from. A passing check proves that the checksums were signed by one of those runs in `cozystack/cozystack`. A `promote-rc.yaml` dispatched from another branch fails this check in its own upload step, with no asset uploaded, and an edited `promote-rc.yaml` has to be merged into `main` before it can sign. Until the `v*` [tag rulesets](./tag-immutability.md#repo-side-enforcement-tag-protection-rulesets) restrict tag creation to the CI app, though, anyone with write access can push a `v*` tag whose tree carries an edited `tags.yaml`, and its signature passes.

The script runs the same `cosign verify-blob` before it returns, so a signature this check would reject fails the job before the first upload, and the release is not published with assets but without a signature. Both jobs that call `make upload_assets`, `tags.yaml::prepare-release` and `promote-rc.yaml::promote`, hold `id-token: write` for the signing identity and install syft and cosign v3.1.3, the version users are asked for above. They install syft from its release tarball, checked against a SHA-256 committed in the workflow, and not with `anchore/sbom-action/download-syft`, which runs an install script fetched at run time in a job that holds the release credentials; [`hack/ci-tool-installs-contract.bats`](../../hack/ci-tool-installs-contract.bats) keeps it that way. A stable release gets its own three files rather than its rc's: promotion restamps several assets, so the checksums are recomputed and signed again.

`promote-rc.yaml` runs the `hack/upload-assets.sh` of the rc tree, not the dispatch ref's. A stable release is therefore signed only when its rc was tagged from a tree that has `hack/sign-release-assets.sh`, and a maintenance line starts carrying signatures once that script is backported to its `release-X.Y` branch.
