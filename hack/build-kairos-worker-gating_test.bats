#!/usr/bin/env bats
# Behavioral coverage for the Kairos worker disk build classifier and the
# handoff of its generated refs into finalize. Workflow expression checks pin
# only producer/consumer wiring; GitHub job-result propagation is CI-only.

load test_helper

ROOT="$(pwd)"
CLASSIFIER="$ROOT/hack/build-kairos-worker-needed.sh"
WF="$ROOT/.github/workflows/pull-requests.yaml"

classify_paths() {
  list="$1"
  shift
  printf '%s\n' "$@" > "$list"
  "$CLASSIFIER" "$list"
}

run_workflow_overlay() {
  fixture="$1"
  matrix="$2"
  kairos_result="$3"
  touched="$4"
  step="$(yq '.jobs.finalize.steps[] | select(.name == "Overlay base-branch refs for unbuilt packages").run' "$WF")"
  [ -n "$step" ]
  mkdir -p "$fixture/hack"
  ln -s "$ROOT/hack/overlay-main-images.sh" "$fixture/hack/overlay-main-images.sh"
  (
    cd "$fixture"
    BUILD_MATRIX="$matrix"
    TALOS_RESULT=skipped
    KAIROS_WORKER_RESULT="$kairos_result"
    PR_TOUCHED="$touched"
    export BUILD_MATRIX TALOS_RESULT KAIROS_WORKER_RESULT PR_TOUCHED
    dash -c "$step"
  )
}

@test "selects the build for every input of the worker disks" {
  dir="$(mktemp -d)"
  list="$dir/changed"

  for path in \
    packages/apps/kubernetes-nodes/images/kairos-worker/Dockerfile \
    packages/apps/kubernetes-nodes/images/kairos-worker/rootfs/usr/lib/systemd/system/kubelet.service \
    packages/apps/kubernetes-nodes/images/kairos-worker-disk/Dockerfile \
    packages/apps/kubernetes-nodes/files/versions.yaml \
    packages/apps/kubernetes-nodes/Makefile \
    hack/common-envs.mk \
    hack/buildkitd.toml \
    .github/workflows/pull-requests.yaml \
    .dockerignore
  do
    [ "$(classify_paths "$list" docs/readme.md "$path")" = true ]
  done

  rm -rf "$dir"
}

@test "a chart-only change to the pool does not rebuild the disks" {
  dir="$(mktemp -d)"
  list="$dir/changed"

  [ "$(classify_paths "$list" \
    packages/apps/kubernetes-nodes/templates/nodegroup.yaml \
    packages/apps/kubernetes-nodes/values.yaml \
    packages/apps/kubernetes-nodes/images/kairos-worker-v1.35.tag \
    packages/apps/kubernetes-nodes/images/kubectl.tag \
    packages/apps/kubernetes/files/versions.yaml \
    internal/controller/controller.go)" = false ]
  [ "$(classify_paths "$list" ' packages/apps/kubernetes-nodes/Makefile' 'packages/apps/kubernetes-nodes-rd/Makefile')" = false ]
  : > "$list"
  [ "$("$CLASSIFIER" "$list")" = false ]

  rm -rf "$dir"
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

  rm -rf "$dir"
}

@test "the disks are built by their own leg, not by the parallel matrix" {
  out="$(hack/build-matrix.sh FULL)"
  if printf '%s\n' "$out" | grep -qF '"packages/apps/kubernetes-nodes"'; then
    echo "kubernetes-nodes is still a parallel matrix unit" >&2
    exit 1
  fi
}

@test "workflow wires the classifier output into the job gate" {
  [ "$(yq '.jobs.plan.outputs.kairos-worker' "$WF")" = '${{ steps.p.outputs.kairos-worker }}' ]
  plan="$(yq '.jobs.plan.steps[] | select(.id == "p").run' "$WF")"
  printf '%s\n' "$plan" | grep -qF 'hack/build-kairos-worker-needed.sh /tmp/changed_norenames.txt'

  gate="$(yq '.jobs["build-kairos-worker"].if' "$WF")"
  printf '%s\n' "$gate" | grep -qF "needs.plan.outputs.code == 'true'"
  printf '%s\n' "$gate" | grep -qF "needs.plan.outputs.kairos-worker == 'true'"
  printf '%s\n' "$gate" | grep -qF "!contains(github.event.pull_request.labels.*.name, 'release')"
  yq '.jobs["build-kairos-worker"].steps[].run' "$WF" | grep -qF 'make -C packages/apps/kubernetes-nodes image'
}

@test "finalize accepts success or skipped and rejects other disk build results" {
  gate="$(yq '.jobs.finalize.if' "$WF")"

  printf '%s\n' "$gate" | grep -qF "needs.build-kairos-worker.result == 'success'"
  printf '%s\n' "$gate" | grep -qF "needs.build-kairos-worker.result == 'skipped'"
  if printf '%s\n' "$gate" | grep -qE "needs\.build-kairos-worker\.result == '(failure|cancelled)'"; then
    echo "finalize admits a failed or cancelled disk build" >&2
    exit 1
  fi
}

@test "a successful disk build keeps its fresh refs during overlay" {
  dir="$(mktemp -d)"
  mkdir -p "$dir/packages/apps/kubernetes-nodes/images" "$dir/_out/mainpkgs/apps/kubernetes-nodes/images"
  printf 'registry.example/kairos-worker-v1.35:pr@sha256:fresh\n' > "$dir/packages/apps/kubernetes-nodes/images/kairos-worker-v1.35.tag"
  printf 'registry.example/kairos-worker-v1.35:main@sha256:base\n' > "$dir/_out/mainpkgs/apps/kubernetes-nodes/images/kairos-worker-v1.35.tag"

  run_workflow_overlay "$dir" '[]' success ''
  grep -q 'sha256:fresh' "$dir/packages/apps/kubernetes-nodes/images/kairos-worker-v1.35.tag"

  rm -rf "$dir"
}

@test "a skipped disk build accepts the base refs" {
  dir="$(mktemp -d)"
  mkdir -p "$dir/packages/apps/kubernetes-nodes/images" "$dir/_out/mainpkgs/apps/kubernetes-nodes/images"
  printf 'registry.example/kairos-worker-v1.35:release@sha256:old\n' > "$dir/packages/apps/kubernetes-nodes/images/kairos-worker-v1.35.tag"
  printf 'registry.example/kairos-worker-v1.35:main@sha256:base\n' > "$dir/_out/mainpkgs/apps/kubernetes-nodes/images/kairos-worker-v1.35.tag"

  run_workflow_overlay "$dir" '[]' skipped ''
  grep -q 'sha256:base' "$dir/packages/apps/kubernetes-nodes/images/kairos-worker-v1.35.tag"

  rm -rf "$dir"
}

@test "the release arm64 legs build the disks the matrix no longer lists" {
  for wf in tags.yaml build-arm64-nightly.yaml; do
    grep -qF 'make -C packages/apps/kubernetes-nodes image' "$ROOT/.github/workflows/$wf" \
      || { echo "$wf builds no arm64 Kairos worker disks" >&2; exit 1; }
  done
}

@test "make image fails instead of building nothing when the minors cannot be read" {
  bin="$(mktemp -d)"
  for t in make sh bash awk git sed grep cat tr uname dirname; do
    p="$(command -v "$t")" && ln -s "$p" "$bin/$t"
  done
  if out="$(PATH="$bin" make -n -C packages/apps/kubernetes-nodes image COZYSTACK_VERSION=0 2>&1)"; then
    rm -rf "$bin"
    echo "make image succeeded without yq to read files/versions.yaml" >&2
    exit 1
  fi
  rm -rf "$bin"
  printf '%s\n' "$out" | grep -qF 'no Kubernetes minors read from files/versions.yaml'
}

@test "LOAD=1 builds the host architecture only" {
  case "$(uname -m)" in x86_64) host=amd64 ;; aarch64|arm64) host=arm64 ;; *) skip "unknown host arch" ;; esac
  out="$(make -n -C packages/apps/kubernetes-nodes image-kairos-worker-v1.35 LOAD=1 COZYSTACK_VERSION=0)"
  printf '%s\n' "$out" | grep -qF "for a in $host; do"
}

@test "each minor keeps a build cache of its own, written only by the final build" {
  out="$(make -n -C packages/apps/kubernetes-nodes image-kairos-worker-v1.35 WRITE_CACHE=1 PLATFORM=linux/amd64 COZYSTACK_VERSION=0)"
  printf '%s\n' "$out" | grep -qF 'kairos-worker-os-v1.35:buildcache'
  if printf '%s\n' "$out" | grep -qE 'kairos-worker-os:buildcache'; then
    echo "a cache ref is shared across minors" >&2
    exit 1
  fi
  test_cmd="$(printf '%s\n' "$out" | sed -e ':a' -e '/\\$/N; s/\\\n//; ta' | grep -F -- '--target test')"
  printf '%s\n' "$test_cmd" | grep -qF -- '--cache-from'
  if printf '%s\n' "$test_cmd" | grep -qF -- '--cache-to'; then
    echo "the test build writes the cache" >&2
    exit 1
  fi
}

@test "each architecture's OS image is loaded for AuroraBoot, also in an OCI export build" {
  out="$(make -n -C packages/apps/kubernetes-nodes image-kairos-worker-v1.35 OCI_EXPORT_DIR=/x BUILDER=b \
    PLATFORM=linux/amd64,linux/arm64 COZYSTACK_VERSION=0 | sed -e ':a' -e '/\\$/N; s/\\\n//; ta')"
  for a in amd64 arm64; do
    os="$(printf '%s\n' "$out" | grep -F -- "--tag \"cozystack.local/kairos-worker-os:v1.35-$a\"")"
    printf '%s\n' "$os" | grep -qF -- '--load=1'
    printf '%s\n' "$os" | grep -qF -- '--builder=b'
    [ "$(printf '%s\n' "$os" | grep -oE -- '--platform[= ][^ ]+')" = "--platform=linux/$a" ]
    if printf '%s\n' "$os" | grep -qF 'type=oci'; then
      echo "the $a OS image is exported instead of loaded" >&2
      exit 1
    fi
  done
  disk="$(printf '%s\n' "$out" | grep -F -- '--file images/kairos-worker-disk/Dockerfile')"
  printf '%s\n' "$disk" | grep -qF -- 'type=oci,dest=/x/kairos-worker-v1.35.oci.tar'
  printf '%s\n' "$disk" | grep -qF -- '--load=0'
}

@test "a pushing build makes each disk from the published OS image pulled back by digest" {
  out="$(make -n -C packages/apps/kubernetes-nodes image-kairos-worker-v1.35 PUSH=1 IMAGE_TAG=t REGISTRY=r.example \
    PLATFORM=linux/amd64 COZYSTACK_VERSION=0 | sed -e ':a' -e '/\\$/N; s/\\\n//; ta')"
  base="$(printf '%s\n' "$out" | grep -nF -- '--tag r.example/kairos-worker-base-v1.35:t')"
  printf '%s\n' "$base" | grep -qF -- '--push=1'
  printf '%s\n' "$out" | grep -qF 'images/kairos-worker-base-v1.35.tag'
  pull="$(printf '%s\n' "$out" | grep -nF 'docker pull --platform "linux/amd64"')"
  [ "${base%%:*}" -lt "${pull%%:*}" ] || { echo "the OS image is pulled before it is pushed" >&2; exit 1; }
  printf '%s\n' "$out" | grep -qF "s/:[^/:@]+@/@/' images/kairos-worker-base-v1.35.tag"
  if printf '%s\n' "$out" | grep -qF -- '--tag "cozystack.local/kairos-worker-os:v1.35-amd64"'; then
    echo "a pushing build makes the disk from a local OS build" >&2
    exit 1
  fi
}

@test "a build that does not push publishes no OS image and makes the disk from a local build" {
  out="$(make -n -C packages/apps/kubernetes-nodes image-kairos-worker-v1.35 PUSH=0 LOAD=0 \
    PLATFORM=linux/amd64 COZYSTACK_VERSION=0 | sed -e ':a' -e '/\\$/N; s/\\\n//; ta')"
  if printf '%s\n' "$out" | grep -qE 'kairos-worker-base-|docker pull'; then
    echo "a build that does not push publishes or pulls the OS image" >&2
    exit 1
  fi
  printf '%s\n' "$out" | grep -qF -- '--tag "cozystack.local/kairos-worker-os:v1.35-amd64"'
}

@test "the captured fragment carries a disk ref the build created" {
  dir="$(mktemp -d)"
  repo="$dir/repo"
  mkdir -p "$repo/packages/apps/kubernetes-nodes/images"
  git -C "$repo" init -q
  git -C "$repo" config user.email ci@example.invalid
  git -C "$repo" config user.name CI
  git -C "$repo" config commit.gpgsign false
  git -C "$repo" config core.hooksPath /dev/null
  printf 'registry.example/kairos-worker-v1.35:old@sha256:old\n' > "$repo/packages/apps/kubernetes-nodes/images/kairos-worker-v1.35.tag"
  git -C "$repo" add .
  git -C "$repo" commit -q -m seed
  printf 'registry.example/kairos-worker-v1.35:pr@sha256:changed\n' > "$repo/packages/apps/kubernetes-nodes/images/kairos-worker-v1.35.tag"
  printf 'registry.example/kairos-worker-v1.99:pr@sha256:created\n' > "$repo/packages/apps/kubernetes-nodes/images/kairos-worker-v1.99.tag"

  step="$(yq '.jobs["build-kairos-worker"].steps[] | select(.name == "Capture Kairos worker patch fragment").run' "$WF")"
  [ -n "$step" ]
  (cd "$repo" && sh -c "$step") >/dev/null
  grep -qF 'sha256:changed' "$repo/_out/fragments/kairos-worker.patch"
  grep -qF 'sha256:created' "$repo/_out/fragments/kairos-worker.patch"

  rm -rf "$dir"
}
