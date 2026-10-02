#!/usr/bin/env bats
# Behavioral coverage for the Talos build classifier and the generated
# matchbox-ref handoff into finalize. Workflow expression checks below pin only
# producer/consumer wiring; GitHub job-result propagation is CI-only behavior.

ROOT="$(pwd)"
CLASSIFIER="$ROOT/hack/build-talos-needed.sh"
WF="$ROOT/.github/workflows/pull-requests.yaml"

classify_paths() {
  list="$1"
  shift
  printf '%s\n' "$@" > "$list"
  "$CLASSIFIER" "$list"
}

cleanup_dir() {
  dir="$1"
  rm -rf "$dir"
}

run_workflow_overlay() {
  fixture="$1"
  matrix="$2"
  talos_result="$3"
  touched="$4"
  step="$(yq '.jobs.finalize.steps[] | select(.name == "Overlay base-branch refs for unbuilt packages").run' "$WF")"
  [ -n "$step" ]
  mkdir -p "$fixture/hack"
  ln -s "$ROOT/hack/overlay-main-images.sh" "$fixture/hack/overlay-main-images.sh"
  (
    cd "$fixture"
    BUILD_MATRIX="$matrix"
    TALOS_RESULT="$talos_result"
    PR_TOUCHED="$touched"
    export BUILD_MATRIX TALOS_RESULT PR_TOUCHED
    dash -c "$step"
  )
}

@test "classifies direct Talos paths by line, independent of order" {
  dir="$(mktemp -d)"
  list="$dir/changed"

  [ "$(classify_paths "$list" packages/core/talos/file docs/readme.md)" = true ]
  [ "$(classify_paths "$list" docs/readme.md packages/core/talos/file cmd/tool/main.go)" = true ]
  [ "$(classify_paths "$list" docs/readme.md packages/core/talos/file)" = true ]
  [ "$(classify_paths "$list" packages/core/talos/file)" = true ]

  cleanup_dir "$dir"
}

@test "rejects whitespace and package-name lookalikes" {
  dir="$(mktemp -d)"
  list="$dir/changed"

  [ "$(classify_paths "$list" packages/core/talos-something/file packages/core/talos docs/talos/file)" = false ]
  [ "$(classify_paths "$list" ' packages/core/talos/file' 'packages/core/talos/file ')" = false ]

  cleanup_dir "$dir"
}

@test "classifies every shared Talos build input and no unrelated root input" {
  dir="$(mktemp -d)"
  list="$dir/changed"

  for path in \
    hack/common-envs.mk \
    hack/buildkitd.toml \
    .github/workflows/pull-requests.yaml \
    .dockerignore
  do
    [ "$(classify_paths "$list" "$path")" = true ]
  done
  [ "$(classify_paths "$list" internal/controller/controller.go go.mod packages/apps/postgres/values.yaml)" = false ]
  : > "$list"
  [ "$("$CLASSIFIER" "$list")" = false ]

  cleanup_dir "$dir"
}

@test "fails closed when the changed-path input is missing or not a file" {
  dir="$(mktemp -d)"

  if "$CLASSIFIER" "$dir/missing" >/dev/null 2>&1; then
    echo "missing input was classified as a safe false" >&2
    exit 1
  fi
  if "$CLASSIFIER" "$dir" >/dev/null 2>&1; then
    echo "directory input was accepted as a changed-path file" >&2
    exit 1
  fi

  cleanup_dir "$dir"
}

@test "a real rename out of or into Talos is visible in the rename-blind list" {
  dir="$(mktemp -d)"
  repo="$dir/repo"
  list="$dir/changed"
  mkdir -p "$repo/packages/core/talos/profiles" "$repo/docs"
  git -C "$repo" init -q
  git -C "$repo" config user.email ci@example.invalid
  git -C "$repo" config user.name CI
  git -C "$repo" config commit.gpgsign false
  git -C "$repo" config core.hooksPath /dev/null
  printf 'profile\n' > "$repo/packages/core/talos/profiles/installer.yaml"
  git -C "$repo" add .
  git -C "$repo" commit -q -m seed

  git -C "$repo" mv packages/core/talos/profiles/installer.yaml docs/installer.yaml
  git -C "$repo" diff --name-status HEAD | grep -q '^R.*packages/core/talos/profiles/installer.yaml.*docs/installer.yaml$'
  git -C "$repo" diff --name-only --no-renames HEAD > "$list"
  [ "$("$CLASSIFIER" "$list")" = true ]

  git -C "$repo" commit -q -am 'rename out'
  git -C "$repo" mv docs/installer.yaml packages/core/talos/profiles/installer.yaml
  git -C "$repo" diff --name-status HEAD | grep -q '^R.*docs/installer.yaml.*packages/core/talos/profiles/installer.yaml$'
  git -C "$repo" diff --name-only --no-renames HEAD > "$list"
  [ "$("$CLASSIFIER" "$list")" = true ]

  cleanup_dir "$dir"
}

@test "deleting a Talos input still selects the build" {
  dir="$(mktemp -d)"
  repo="$dir/repo"
  list="$dir/changed"
  mkdir -p "$repo/packages/core/talos/profiles"
  git -C "$repo" init -q
  git -C "$repo" config user.email ci@example.invalid
  git -C "$repo" config user.name CI
  git -C "$repo" config commit.gpgsign false
  git -C "$repo" config core.hooksPath /dev/null
  printf 'profile\n' > "$repo/packages/core/talos/profiles/installer.yaml"
  git -C "$repo" add .
  git -C "$repo" commit -q -m seed

  rm "$repo/packages/core/talos/profiles/installer.yaml"
  git -C "$repo" diff --name-only --no-renames HEAD > "$list"
  [ "$("$CLASSIFIER" "$list")" = true ]

  cleanup_dir "$dir"
}

@test "workflow wires the rename-blind classifier output and preserves job guards" {
  [ "$(yq '.jobs.plan.outputs.talos' "$WF")" = '${{ steps.p.outputs.talos }}' ]
  plan="$(yq '.jobs.plan.steps[] | select(.id == "p").run' "$WF")"
  printf '%s\n' "$plan" | grep -qF 'hack/build-talos-needed.sh /tmp/changed_norenames.txt'

  gate="$(yq '.jobs["build-talos"].if' "$WF")"
  printf '%s\n' "$gate" | grep -qF "needs.plan.outputs.code == 'true'"
  printf '%s\n' "$gate" | grep -qF "needs.plan.outputs.talos == 'true'"
  printf '%s\n' "$gate" | grep -qF 'github.event.pull_request.head.repo.fork'
  printf '%s\n' "$gate" | grep -qF "!contains(github.event.pull_request.labels.*.name, 'release')"
  if printf '%s\n' "$gate" | grep -qF 'needs.plan.outputs.touched'; then
    echo "build-talos still uses the whitespace-formatted touched output" >&2
    exit 1
  fi
}

@test "finalize accepts success or skipped and rejects other Talos results" {
  gate="$(yq '.jobs.finalize.if' "$WF")"

  printf '%s\n' "$gate" | grep -qF "needs.build-talos.result == 'success'"
  printf '%s\n' "$gate" | grep -qF "needs.build-talos.result == 'skipped'"
  if printf '%s\n' "$gate" | grep -qE "needs\.build-talos\.result == '(failure|cancelled)'"; then
    echo "finalize admits a failed or cancelled Talos build" >&2
    exit 1
  fi
}

@test "successful Talos output keeps the fresh bootbox digest during overlay" {
  dir="$(mktemp -d)"
  mkdir -p "$dir/packages/apps/foo/images" "$dir/_out/mainpkgs/apps/foo/images"
  mkdir -p "$dir/packages/extra/bootbox/images" "$dir/_out/mainpkgs/extra/bootbox/images"
  printf 'registry.example/foo:pr@sha256:foo-fresh\n' > "$dir/packages/apps/foo/images/foo.tag"
  printf 'registry.example/foo:main@sha256:foo-base\n' > "$dir/_out/mainpkgs/apps/foo/images/foo.tag"
  printf 'registry.example/matchbox:pr@sha256:fresh\n' > "$dir/packages/extra/bootbox/images/matchbox.tag"
  printf 'registry.example/matchbox:main@sha256:base\n' > "$dir/_out/mainpkgs/extra/bootbox/images/matchbox.tag"

  run_workflow_overlay "$dir" '["packages/apps/foo"]' success ''
  grep -q 'sha256:foo-fresh' "$dir/packages/apps/foo/images/foo.tag"
  grep -q 'sha256:fresh' "$dir/packages/extra/bootbox/images/matchbox.tag"

  cleanup_dir "$dir"
}

@test "skipped Talos output accepts the base bootbox digest" {
  dir="$(mktemp -d)"
  mkdir -p "$dir/packages/extra/bootbox/images" "$dir/_out/mainpkgs/extra/bootbox/images"
  printf 'registry.example/matchbox:release@sha256:old\n' > "$dir/packages/extra/bootbox/images/matchbox.tag"
  printf 'registry.example/matchbox:main@sha256:base\n' > "$dir/_out/mainpkgs/extra/bootbox/images/matchbox.tag"

  run_workflow_overlay "$dir" '[]' skipped ''
  grep -q 'sha256:base' "$dir/packages/extra/bootbox/images/matchbox.tag"

  cleanup_dir "$dir"
}

@test "an explicitly edited bootbox ref wins when Talos was skipped" {
  dir="$(mktemp -d)"
  mkdir -p "$dir/packages/extra/bootbox/images" "$dir/_out/mainpkgs/extra/bootbox/images"
  printf 'registry.example/matchbox:custom@sha256:edited\n' > "$dir/packages/extra/bootbox/images/matchbox.tag"
  printf 'registry.example/matchbox:main@sha256:base\n' > "$dir/_out/mainpkgs/extra/bootbox/images/matchbox.tag"

  run_workflow_overlay "$dir" '[]' skipped 'packages/extra/bootbox'
  grep -q 'sha256:edited' "$dir/packages/extra/bootbox/images/matchbox.tag"

  cleanup_dir "$dir"
}

@test "the PR path has no consumer of the removed nocloud artifact" {
  code="$(grep -v '^[[:space:]]*#' "$WF")"
  if printf '%s\n' "$code" | grep -E 'name: talos-image|nocloud-amd64\.raw\.xz' | grep -v 'const diskId'; then
    echo "a PR-path step consumes a Talos artifact that the job can skip" >&2
    exit 1
  fi
}
