#!/usr/bin/env bats
# Tests for hack/promote-retag.sh — the rc->stable retag selector.
#
# Guards the regression where collect_refs scraped *every* @sha256 ref from the
# package values.yaml — including third-party images (docker.io/clastix/kubectl,
# ghcr.io/kvaps/...), bare upstream tags (kube-ovn/keycloak/kilo) and a
# "--migrate-image=..." arg string — so the first skopeo copy to a registry CI
# cannot push to aborted the whole promotion. The selector must emit only
# cozystack-owned ($REGISTRY/...) refs.

setup() {
  command -v yq >/dev/null 2>&1 || skip "yq (mikefarah) not installed"
  yq --version 2>&1 | grep -q mikefarah || skip "yq is not mikefarah"
}

@test "dry-run over the real tree retags only cozystack-owned refs" {
  run hack/promote-retag.sh v9.9.9 --dry-run
  [ "$status" -eq 0 ]

  # At least one cozystack-owned image is selected.
  echo "$output" | grep -q 'docker://ghcr.io/cozystack/cozystack/'

  # Every docker:// ref in the copy plan is under the cozystack registry — no
  # third-party repos and no malformed arg-string refs leak through.
  bad=$(echo "$output" | grep -oE 'docker://[^ ]+' | sed 's|docker://||' \
        | grep -vE '^ghcr\.io/cozystack/cozystack/' || true)
  [ -z "$bad" ]
}

@test "REGISTRY override scopes the selection" {
  REGISTRY="example.com/nope" run hack/promote-retag.sh v9.9.9 --dry-run
  # No cozystack images live under example.com/nope, so the selector finds
  # nothing and exits non-zero rather than silently promoting the wrong set.
  [ "$status" -ne 0 ]
  echo "$output" | grep -q 'No cozystack-owned digest-pinned image refs found'
}
