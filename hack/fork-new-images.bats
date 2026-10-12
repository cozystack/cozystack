#!/usr/bin/env bats
# hack/fork-new-images.sh refuses a fork PR that adds an image the base tree
# does not export, before the build. Its derivation has to stay the one
# e2e-fork.yaml uses for the push allowlist, or the early check and the push
# disagree about what counts as new.

load test_helper

ROOT="$(pwd)"
SCRIPT="$ROOT/hack/fork-new-images.sh"
FORK_WF="$ROOT/.github/workflows/e2e-fork.yaml"
PR_WF="$ROOT/.github/workflows/pull-requests.yaml"

# make_tree DIR NAME... — a git index holding one package per NAME whose image
# target exports NAME.oci.tar, the way the real recipes do under OCI_EXPORT_DIR.
make_tree() {
  dir="$1"
  shift
  mkdir -p "$dir"
  git -C "$dir" init --quiet
  for name in "$@"; do
    mkdir -p "$dir/packages/system/$name"
    printf 'image:\n\tdocker buildx build --output type=oci,dest=$(OCI_EXPORT_DIR)/%s.oci.tar .\n' "$name" \
      > "$dir/packages/system/$name/Makefile"
  done
  git -C "$dir" add --all
}

@test "an image only the head tree exports is refused and named" {
  tmp="$(mktemp -d)"
  make_tree "$tmp/base" alpha beta
  make_tree "$tmp/head" alpha beta gamma delta
  rc=0
  "$SCRIPT" "$tmp/base" "$tmp/head" > "$tmp/out" 2>&1 || rc=$?
  [ "$rc" -eq 1 ]
  grep -q '^::error title=New image in a fork PR::' "$tmp/out"
  grep -q 'does not build: delta gamma\.' "$tmp/out"
  rm -rf "$tmp"
}

@test "the same image set, or a removed image, passes" {
  tmp="$(mktemp -d)"
  make_tree "$tmp/base" alpha beta
  make_tree "$tmp/same" alpha beta
  make_tree "$tmp/fewer" alpha
  "$SCRIPT" "$tmp/base" "$tmp/same"
  "$SCRIPT" "$tmp/base" "$tmp/fewer"
  rm -rf "$tmp"
}

@test "a base tree that yields no names fails as a derivation error, not a new image" {
  tmp="$(mktemp -d)"
  make_tree "$tmp/base"
  make_tree "$tmp/head" alpha
  rc=0
  "$SCRIPT" "$tmp/base" "$tmp/head" > "$tmp/out" 2>&1 || rc=$?
  [ "$rc" -eq 1 ]
  grep -q 'produced no names for the base tree' "$tmp/out"
  if grep -q 'New image' "$tmp/out"; then echo "FAIL: blamed the PR for an empty base set"; false; fi
  rm -rf "$tmp"
}

@test "the repository tree derives image names and passes against itself" {
  "$SCRIPT" "$ROOT" "$ROOT"
}

@test "the derivation matches the e2e-fork.yaml push allowlist step" {
  allow="$(yq '.jobs.publish.steps[] | select(.name == "Derive trusted image allowlist (base tree)").run' "$FORK_WF")"
  [ -n "$allow" ]
  for frag in \
    "ls-files 'packages/*/*/Makefile'" \
    "grep -qE '^image:'" \
    "OCI_EXPORT_DIR=/oci REGISTRY=r COZYSTACK_VERSION=0 2>/dev/null || true" \
    "grep -oE '(dest=/oci/|oci-archive:/oci/)[a-z0-9._-]+\.oci\.tar'" \
    "sed -E 's#.*/##; s#\.oci\.tar\$##' | sort -u"; do
    grep -qF -- "$frag" "$SCRIPT" || { echo "fragment missing from the script: $frag"; false; }
    printf '%s\n' "$allow" | grep -qF -- "$frag" || { echo "fragment missing from e2e-fork.yaml: $frag"; false; }
  done
}

@test "plan runs the check for fork PRs only" {
  step="$(yq '.jobs.plan.steps[] | select(.run // "" | test("hack/fork-new-images\.sh"))' "$PR_WF")"
  [ -n "$step" ]
  [ "$(printf '%s\n' "$step" | yq '.if')" = '${{ github.event.pull_request.head.repo.fork }}' ]
}
