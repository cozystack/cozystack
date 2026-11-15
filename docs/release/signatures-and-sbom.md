## Release signatures and SBOM

A release cut from a tree that has [`hack/sign-release-assets.sh`](../../hack/sign-release-assets.sh) carries three files beside its other assets, which [`hack/upload-assets.sh`](../../hack/upload-assets.sh) has that script write before it uploads anything, and one cut from a tree that also has [`hack/sign-release-images.sh`](../../hack/sign-release-images.sh) and [`hack/collect-image-sboms.sh`](../../hack/collect-image-sboms.sh) carries two more, which `tags.yaml::prepare-release` writes before the upload. Releases cut before them have none of these files, and neither does a maintenance line until the scripts are backported to it (see below).

- `cozystack-source-sbom.spdx.json` is an SPDX SBOM, made with syft, of the committed source tree (the Go modules, npm lockfiles, GitHub Actions references and other manifests syft reads in it) and of the `cozypkg` binaries in the `cozypkg-*.tar.gz` assets. It does not describe what runs in the cluster: the container images carry their own SBOMs, see [SBOM of the container images](#sbom-of-the-container-images), and the Talos installer image, ISO, disk, kernel and initramfs images have none. Its name says what it covers, so that `cozystack-sbom` stays free for an SBOM of the release as a whole.
- `cozystack-images.txt` lists every first-party container image the release pins, one `<repository>@<digest>` per line: the multi-arch indexes [`hack/stitch-multiarch.sh`](../../hack/stitch-multiarch.sh) repinned the tree on, which is what a cluster installed from this release pulls. The packages artifact and the installer chart are not in it, and neither is `cozystack-scheduler`: its image is built by the [cozystack-scheduler](https://github.com/cozystack/cozystack-scheduler) repository, pinned by a vendored chart under `charts/`, which the shared enumeration leaves out, so this release neither builds nor signs it.
- `cozystack-images-sbom.tar.gz` holds the SPDX SBOM of each image in that list, as `<image>.<os>-<arch>.spdx.json`, one document per platform, read from the attestation BuildKit attached to the image at build time (`SBOM=1` in `tags.yaml`, the scanner pinned in [`hack/common-envs.mk`](../../hack/common-envs.mk)). These documents come from the syft inside that scanner, the source SBOM from the syft the workflow installs, so the two need not agree on a version. An image without a document for every platform its index serves fails the release instead of being left out.
- `cozystack-checksums.txt` holds the SHA-256 of every other asset, the SBOMs and the image list among them.
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

### SBOM of the container images

The container images a release pins by digest each carry an SPDX SBOM of the packages in them, which BuildKit attaches at build time as an attestation (`SBOM=1` in `tags.yaml`, scanner and flags in [`hack/common-envs.mk`](../../hack/common-envs.mk)). The attestation is an entry of the image's index, so the digest the release pins names both, and promotion copies it with the image. Read it with `docker buildx imagetools inspect <image>@<digest> --format '{{ json .SBOM }}'`, which prints one document per platform. They carry no signature of their own: the signature over the index covers them, see [Image signatures](#image-signatures). A release cut before `SBOM=1` was set has none, and neither has the Talos installer image, which the imager builds and `skopeo` pushes, nor the third-party images `packages/` vendors by digest.

### Image signatures

The images in `cozystack-images.txt` are signed too. After the stitch, `hack/sign-release-images.sh` signs each digest with cosign, keylessly, as the `tags.yaml` run that prepared the pre-release, and verifies every signature against the same identity as above before `hack/upload-assets.sh` runs, so a release is never published with unsigned images. A signature over an index covers every manifest the index lists by digest, the attestation manifest that holds the SBOM included, which is why the SBOM carries no signature of its own. cosign stores each signature as a Sigstore bundle, an OCI referrer of the digest it signs, in the same repository. GHCR serves no referrers API, so cosign lists the referrer under the fallback tag `sha256-<hex>` of that digest, where `cosign verify` finds it with no extra flags; the tag `sha256-<hex>.sig` is the legacy format, written only with `--new-bundle-format=false`, and a release has none. Every digest gets its own short-lived key, Fulcio certificate and Rekor entry. Promotion copies each digest to its stable tag inside that repository, so a stable release's images carry the signatures made for its rc, and their certificates name `tags.yaml` at the rc tag. `promote-rc.yaml::promote` checks every signature with the rc tree's `hack/sign-release-images.sh --verify-only` before it stages the stable release, and skips the check with a notice for an rc cut from a tree that has no such script. A re-run of `prepare-release` signs only the digests that do not verify yet, so it never stacks a second signature on an image. The packages artifact and the installer chart are not signed: promotion republishes both under a new digest.

### How to verify a release

With cosign v3.1.3 or later and the assets of the release in the current directory, this checks the signature over the checksums, every downloaded asset against them, the signature of every image the release pins, and unpacks the image SBOMs:

```sh
cosign verify-blob cozystack-checksums.txt \
  --bundle cozystack-checksums.txt.sigstore.json \
  --certificate-identity-regexp '^https://github\.com/cozystack/cozystack/\.github/workflows/(tags\.yaml@refs/tags/v|promote-rc\.yaml@refs/heads/main$)' \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com &&
  sha256sum --check --ignore-missing cozystack-checksums.txt &&
  [ "$(grep -cE '^[0-9a-f]{64} [ *]cozystack-images(\.txt|-sbom\.tar\.gz)$' cozystack-checksums.txt)" = 2 ] &&
  [ -s cozystack-images.txt ] && [ -s cozystack-images-sbom.tar.gz ] &&
  (
    while read -r image <&3; do
      cosign verify "$image" \
        --certificate-identity-regexp '^https://github\.com/cozystack/cozystack/\.github/workflows/(tags\.yaml@refs/tags/v|promote-rc\.yaml@refs/heads/main$)' \
        --certificate-oidc-issuer https://token.actions.githubusercontent.com >/dev/null ||
        { echo "not verified: $image" >&2; exit 1; }
    done 3<cozystack-images.txt
  ) &&
  mkdir -p image-sboms && tar -xzf cozystack-images-sbom.tar.gz -C image-sboms
```

The two `cozystack-images*` entries are required in the signed checksums, and both files non-empty, because `--ignore-missing` alone accepts a signed checksums file from another release that lists neither, with an image list and an archive that nothing signed vouches for. The loop stops at the first image that fails, names it and fails the chain, so the SBOMs are unpacked only after every image verified; the list is read from fd 3 so that cosign cannot read it from stdin. `cosign verify` prints the certificate of each signature; its `Subject` names the workflow and the tag, and the image is pulled only as far as its manifests. The same SBOM can be read from the registry without the archive, with `docker buildx imagetools inspect <image> --format '{{ json .SBOM }}'`, which prints the documents keyed by platform. Checking an image by the tag a release page shows rather than by the digest in `cozystack-images.txt` proves less: a tag can be moved, a digest cannot, and the signature is bound to the digest.

What fails, and what it means: `cosign verify-blob` rejecting the bundle, or `cosign verify` finding no matching signature, means the file or the image is not what the release workflow published, or cosign is older than the version above; `sha256sum` reporting a mismatch means the download is corrupt or replaced; an image in `cozystack-images.txt` that `cosign verify` cannot find at all means the registry no longer serves that digest. An image missing from the archive cannot happen for a release that has one, since `hack/collect-image-sboms.sh` fails the release instead.
