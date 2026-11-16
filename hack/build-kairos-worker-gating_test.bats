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
  mkdir "$fixture/hack/lib"
  cp "$ROOT/hack/lib/image-refs.sh" "$fixture/hack/lib/image-refs.sh"
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
    hack/build-digest.sh \
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

# The disks a pull request does not build come from the committed refs, or
# from the base branch's packages artifact, which only main and the release
# lines publish. A pull request stacked on another one therefore has to build
# whenever its tree differs from main in a disk input, even when its own diff
# against the parent does not.
kairos_paths_fixture() {
  repo="$1"
  mkdir -p "$repo"
  git -C "$repo" init -q -b main
  git -C "$repo" config commit.gpgsign false
  mkdir -p "$repo/packages/apps/kubernetes-nodes/images/kairos-worker" "$repo/docs"
  printf 'a\n' > "$repo/packages/apps/kubernetes-nodes/images/kairos-worker/disk.yaml"
  printf 'a\n' > "$repo/docs/x.md"
  git -C "$repo" add .
  git -C "$repo" -c user.name=T -c user.email=t@example.org commit -qm main
  git -C "$repo" update-ref refs/remotes/origin/main HEAD
}

kairos_paths() {
  (cd "$1" && "$ROOT/hack/build-kairos-worker-paths.sh" "$2" "$1/changed.txt") || return
  "$CLASSIFIER" "$1/changed.txt"
}

@test "a pull request on main builds the disks only for its own disk changes" {
  repo="$BATS_TEST_TMPDIR/repo"
  kairos_paths_fixture "$repo"
  git -C "$repo" checkout -q -b pr
  printf 'b\n' > "$repo/docs/x.md"
  git -C "$repo" -c user.name=T -c user.email=t@example.org commit -qam docs
  [ "$(kairos_paths "$repo" main)" = false ]
  printf 'b\n' > "$repo/packages/apps/kubernetes-nodes/images/kairos-worker/disk.yaml"
  git -C "$repo" -c user.name=T -c user.email=t@example.org commit -qam disk
  [ "$(kairos_paths "$repo" main)" = true ]
}

@test "a pull request on a release line is judged against that line" {
  repo="$BATS_TEST_TMPDIR/repo"
  kairos_paths_fixture "$repo"
  git -C "$repo" checkout -q -b release-1.6
  printf 'b\n' > "$repo/packages/apps/kubernetes-nodes/images/kairos-worker/disk.yaml"
  git -C "$repo" -c user.name=T -c user.email=t@example.org commit -qam line
  git -C "$repo" update-ref refs/remotes/origin/release-1.6 HEAD
  git -C "$repo" checkout -q -b pr
  printf 'b\n' > "$repo/docs/x.md"
  git -C "$repo" -c user.name=T -c user.email=t@example.org commit -qam docs
  [ "$(kairos_paths "$repo" release-1.6)" = false ]
}

@test "a pull request on a branch only named like a release line is judged as stacked" {
  repo="$BATS_TEST_TMPDIR/repo"
  kairos_paths_fixture "$repo"
  git -C "$repo" checkout -q -b release-1.8.1
  printf 'b\n' > "$repo/packages/apps/kubernetes-nodes/images/kairos-worker/disk.yaml"
  git -C "$repo" -c user.name=T -c user.email=t@example.org commit -qam patch
  git -C "$repo" update-ref refs/remotes/origin/release-1.8.1 HEAD
  git -C "$repo" checkout -q -b pr
  printf 'b\n' > "$repo/docs/x.md"
  git -C "$repo" -c user.name=T -c user.email=t@example.org commit -qam docs
  [ "$(kairos_paths "$repo" release-1.8.1)" = true ]
}

@test "a pull request stacked on another builds the disks when the stack changed them against main" {
  repo="$BATS_TEST_TMPDIR/repo"
  kairos_paths_fixture "$repo"
  git -C "$repo" checkout -q -b parent
  printf 'b\n' > "$repo/packages/apps/kubernetes-nodes/images/kairos-worker/disk.yaml"
  git -C "$repo" -c user.name=T -c user.email=t@example.org commit -qam disk
  git -C "$repo" update-ref refs/remotes/origin/parent HEAD
  git -C "$repo" checkout -q -b child
  printf 'b\n' > "$repo/docs/x.md"
  git -C "$repo" -c user.name=T -c user.email=t@example.org commit -qam docs
  [ "$(kairos_paths "$repo" parent)" = true ]
}

@test "a pull request stacked on another that leaves the disks as on main does not build them" {
  repo="$BATS_TEST_TMPDIR/repo"
  kairos_paths_fixture "$repo"
  git -C "$repo" checkout -q -b parent
  printf 'b\n' > "$repo/docs/x.md"
  git -C "$repo" -c user.name=T -c user.email=t@example.org commit -qam docs
  git -C "$repo" update-ref refs/remotes/origin/parent HEAD
  git -C "$repo" checkout -q -b child
  printf 'c\n' > "$repo/docs/x.md"
  git -C "$repo" -c user.name=T -c user.email=t@example.org commit -qam docs2
  [ "$(kairos_paths "$repo" parent)" = false ]
}

@test "a stacked pull request whose fork point from main is unknown builds the disks" {
  repo="$BATS_TEST_TMPDIR/repo"
  kairos_paths_fixture "$repo"
  git -C "$repo" checkout -q -b parent
  git -C "$repo" update-ref refs/remotes/origin/parent HEAD
  git -C "$repo" update-ref -d refs/remotes/origin/main
  rc=0; (cd "$repo" && "$ROOT/hack/build-kairos-worker-paths.sh" parent "$repo/changed.txt") || rc=$?
  [ "$rc" -ne 0 ]
}

@test "workflow wires the classifier output into the job gate" {
  [ "$(yq '.jobs.plan.outputs.kairos-worker' "$WF")" = '${{ steps.p.outputs.kairos-worker }}' ]
  plan="$(yq '.jobs.plan.steps[] | select(.id == "p").run' "$WF")"
  printf '%s\n' "$plan" | grep -qF 'hack/build-kairos-worker-paths.sh "$BASE_REF" /tmp/changed_kairos.txt'
  printf '%s\n' "$plan" | grep -qF 'hack/build-kairos-worker-needed.sh /tmp/changed_kairos.txt'
  # A list it could not compute builds the disks rather than skipping them.
  printf '%s\n' "$plan" | grep -qF 'kairos_worker=true'

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

@test "each architecture's OS image is loaded for AuroraBoot, also in an OCI export build, and no test stage is exported" {
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
  # The fork e2e pushes every archive it finds, so the test stage, whose
  # result never leaves the runner, must not write one either.
  tests="$(printf '%s\n' "$out" | grep -F -- '--target test')"
  [ "$(printf '%s\n' "$tests" | grep -c .)" -eq 2 ]
  if printf '%s\n' "$tests" | grep -qF 'type=oci'; then
    echo "the test stage is exported" >&2
    exit 1
  fi
}

@test "the test stage and the loaded OS image carry no attestation in an SBOM build" {
  out="$(make -n -C packages/apps/kubernetes-nodes image-kairos-worker-v1.35 SBOM=1 PUSH=0 BUILDER=b \
    PLATFORM=linux/amd64 COZYSTACK_VERSION=0 | sed -e ':a' -e '/\\$/N; s/\\\n//; ta')"
  tests="$(printf '%s\n' "$out" | grep -F -- '--target test' || true)"
  loads="$(printf '%s\n' "$out" | grep -F -- '--load=1' || true)"
  [ -n "$tests" ] || { echo "no test-stage build line" >&2; false; }
  [ -n "$loads" ] || { echo "no loaded OS image build line" >&2; false; }
  if printf '%s\n%s\n' "$tests" "$loads" | grep -qF -- '--attest'; then
    echo "a build whose result stays on the runner carries an attestation" >&2
    exit 1
  fi
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

@test "the worker disk forwards the journal to the serial console on a recovery boot only" {
  disk="$ROOT/packages/apps/kubernetes-nodes/images/kairos-worker/disk.yaml"
  conf=/etc/systemd/journald.conf.d/90-serial-console.conf
  # Every step that writes the drop-in, in any stage, with its whole guard. The
  # brackets keep an empty or missing guard on a line of its own, which command
  # substitution would otherwise drop at the end. A guard is compared whole, so
  # a negated one, which would forward the journal on every active boot, fails.
  guards="$(yq -r '.stages[][] | select(.files[]?.path == "'"$conf"'") | "<" + ((.if // "") | tostring) + ">"' "$disk")"
  [ -n "$guards" ]
  printf '%s\n' "$guards" | while IFS= read -r guard; do
    guard="$(printf '%s' "${guard#<}" | sed 's/>$//' | tr -s ' ' | sed 's/^ //; s/ $//')"
    [ "$guard" = '[ -f /run/cos/recovery_mode ]' ] \
      || { echo "a step writes $conf under the guard '$guard', want '[ -f /run/cos/recovery_mode ]'" >&2; exit 1; }
  done
  step='.stages."boot.before"[] | select(.files[]?.path == "'"$conf"'")'
  content="$(yq -r "$step"' | .files[] | select(.path == "'"$conf"'") | .content' "$disk")"
  printf '%s\n' "$content" | grep -qx 'ForwardToConsole=yes'
  printf '%s\n' "$content" | grep -qx 'TTYPath=/dev/ttyS0'
  # journald reads the drop-in only when it starts, and it is already running.
  yq -r "$step"' | .commands[]' "$disk" | grep -qx 'systemctl restart systemd-journald'
}

@test "the worker disk runs the first-boot reset itself, after the kernel is given back the partitions the layout added" {
  disk="$ROOT/packages/apps/kubernetes-nodes/images/kairos-worker/disk.yaml"
  guard='[ -f /run/cos/recovery_mode ] && [ -f /oem/01_reset.yaml ]'
  skip=/oem/.autoreset.skip
  takeover="$(yq -r '.stages."network.before"[] | select(.commands[]? == "touch '"$skip"'") | .if' "$disk")"
  [ "$takeover" = "$guard" ]
  step='.stages.network[] | select(.commands[]? | test("kairos-agent .*reset"))'
  [ "$(yq -r "$step | .if" "$disk")" = "$guard" ]
  script="$(yq -r "$step | .commands[]" "$disk")"
  partx="$(printf '%s\n' "$script" | grep -n 'partx -a ' | cut -d: -f1)"
  # The first settle drains the events of AuroraBoot's udevadm trigger before
  # partx reads what the kernel has, the last one waits for the labels.
  trigger_settle="$(printf '%s\n' "$script" | grep -nx ' *udevadm settle' | head -n 1 | cut -d: -f1)"
  settle="$(printf '%s\n' "$script" | grep -nx ' *udevadm settle' | tail -n 1 | cut -d: -f1)"
  reset="$(printf '%s\n' "$script" | grep -nx ' *kairos-agent reset --unattended --reboot' | cut -d: -f1)"
  [ -n "$trigger_settle" ]
  [ "$trigger_settle" -lt "$partx" ]
  [ -n "$partx" ]
  [ -n "$settle" ]
  [ -n "$reset" ]
  [ "$partx" -lt "$settle" ]
  [ "$settle" -lt "$reset" ]
  # --debug prints the whole collected config, the join token included, and the
  # recovery boot forwards that output to the serial console.
  if yq -r '.stages[][] | .commands[]?' "$disk" | grep -q -- '--debug'; then
    echo "a step in $disk runs with --debug" >&2
    false
  fi
  yq -r '.stages."after-reset"[] | .commands[]?' "$disk" | grep -qx "rm -f $skip"
}

# Runs the reset step of disk.yaml against stubbed tools: the console and the
# initramfs log become files, and a partition "node" is a regular file, so the
# block-device test is swapped for an existence test once its presence is
# checked.
run_reset_step() {
  local tmp=$1 script
  script="$(yq -r '.stages.network[] | select(.commands[]? | test("kairos-agent .*reset")) | .commands[]' \
    "$ROOT/packages/apps/kubernetes-nodes/images/kairos-worker/disk.yaml")"
  printf '%s\n' "$script" | grep -qF '! [ -b "$node" ]'
  printf '%s\n' "$script" \
    | sed -e "s#/dev/ttyS0#$tmp/console#g" -e "s#/run/immucore/immucore.log#$tmp/immucore.log#g" \
      -e 's#! \[ -b "$node" \]#! [ -e "$node" ]#' > "$tmp/step.sh"
  PATH="$tmp/bin:$PATH" TMPROOT="$tmp" sh "$tmp/step.sh" > "$tmp/stdout" 2>&1
}

reset_step_fixture() {
  local tmp=$1
  mkdir -p "$tmp/bin" "$tmp/dev"
  : > "$tmp/calls"
  : > "$tmp/console"
  printf '%s\n' \
    '2026-11-24T09:43:29Z INF Processing stage step '"'"'Expand disk layout and autoreset.Add state partition'"'"'.' \
    '2026-11-24T09:43:29Z DBG collected config: bootstrapToken: abcdef.0123456789abcdef' \
    '2026-11-24T09:43:30Z ERR Error formatting partition COS_STATE: mke2fs 1.47.4 (6-Mar-2025)' \
    'The file /dev/vda4 does not exist and no size was specified.' \
    '2026-11-24T09:43:30Z WRN (conditional) Skip partition step' \
    '2026-11-24T09:43:31Z INF done' > "$tmp/immucore.log"
  for tool in udevadm partx mkfs.ext2 mkfs.ext4 kairos-agent; do
    printf '#!/bin/sh\necho "%s $*" >> "$TMPROOT/calls"\n' "$tool" > "$tmp/bin/$tool"
  done
  # A node marked .ioerr fails to read, the way an unreadable device would.
  cat > "$tmp/bin/dd" <<'EOF'
#!/bin/sh
for arg; do case "$arg" in if=*) [ -e "${arg#if=}.ioerr" ] && exit 1 ;; esac; done
exit 0
EOF
  cat > "$tmp/bin/lsblk" <<'EOF'
#!/bin/sh
case "$*" in
  "-no PKNAME /dev/vda3") echo vda ;;
  "-rno PATH,PARTLABEL /dev/vda") printf '%s state\n%s persistent\n' "$TMPROOT/dev/vda4" "$TMPROOT/dev/vda5" ;;
  *) echo "NAME SIZE LABEL PARTLABEL" ;;
esac
EOF
  cat > "$tmp/bin/blkid" <<'EOF'
#!/bin/sh
case "$1" in
  -L) [ "$2" = COS_RECOVERY ] && { echo /dev/vda3; exit 0; }; [ -e "$TMPROOT/$2-labelled" ] && exit 0; exit 2 ;;
  # Like the real one, -p without -D reports the partition entry and exits 0.
  -p) [ "$2" = -D ] || exit 0; [ -e "$3.fs" ] && exit 0; [ -e "$3.err" ] && exit 4; exit 2 ;;
esac
exit 4
EOF
  chmod +x "$tmp/bin/"*
}

@test "a worker disk with labelled state and persistent partitions resets without touching the disks or the console" {
  tmp=$(mktemp -d)
  reset_step_fixture "$tmp"
  touch "$tmp/COS_STATE-labelled" "$tmp/COS_PERSISTENT-labelled" "$tmp/dev/vda4" "$tmp/dev/vda5"
  run_reset_step "$tmp"
  if grep -q '^mkfs\.' "$tmp/calls"; then false; fi
  [ ! -s "$tmp/console" ]
  tail -n 1 "$tmp/calls" | grep -qx 'kairos-agent reset --unattended --reboot'
  rm -rf "$tmp"
}

@test "a worker disk without a state filesystem formats only the empty partitions, before the reset" {
  tmp=$(mktemp -d)
  reset_step_fixture "$tmp"
  touch "$tmp/dev/vda4" "$tmp/dev/vda5" "$tmp/dev/vda5.fs"
  run_reset_step "$tmp"
  [ "$(grep -c '^mkfs\.' "$tmp/calls")" -eq 1 ]
  grep -qx "mkfs.ext2 -L COS_STATE $tmp/dev/vda4" "$tmp/calls"
  # partx and a settle come before the format, a settle and the reset after it.
  order=$(grep -oE '^(partx|udevadm settle|mkfs.ext2|kairos-agent)' "$tmp/calls" | tr '\n' ' ')
  [ "$order" = "udevadm settle partx udevadm settle mkfs.ext2 udevadm settle kairos-agent " ]
  grep -qF 'NAME SIZE LABEL PARTLABEL' "$tmp/console"
  grep -qF 'Add state partition' "$tmp/console"
  grep -qx 'The file /dev/vda4 does not exist and no size was specified.' "$tmp/console"
  if grep -q 'bootstrapToken\|conditional\|INF done' "$tmp/console"; then false; fi
  rm -rf "$tmp"
}

@test "a worker disk leaves an unreadable partition unformatted even when blkid reports no signature on it" {
  tmp=$(mktemp -d)
  reset_step_fixture "$tmp"
  touch "$tmp/dev/vda4" "$tmp/dev/vda4.ioerr" "$tmp/dev/vda5" "$tmp/dev/vda5.fs"
  run_reset_step "$tmp"
  if grep -q '^mkfs\.' "$tmp/calls"; then false; fi
  grep -qx 'the state partition cannot be read, left unformatted' "$tmp/console"
  tail -n 1 "$tmp/calls" | grep -qx 'kairos-agent reset --unattended --reboot'
  rm -rf "$tmp"
}

@test "a worker disk leaves a partition unformatted when blkid fails on it with anything but no signature" {
  tmp=$(mktemp -d)
  reset_step_fixture "$tmp"
  touch "$tmp/dev/vda4" "$tmp/dev/vda4.err" "$tmp/dev/vda5" "$tmp/dev/vda5.fs"
  run_reset_step "$tmp"
  if grep -q '^mkfs\.' "$tmp/calls"; then false; fi
  tail -n 1 "$tmp/calls" | grep -qx 'kairos-agent reset --unattended --reboot'
  rm -rf "$tmp"
}

@test "a worker disk whose state partition has no device node formats only the persistent one, as ext4, and says so" {
  tmp=$(mktemp -d)
  reset_step_fixture "$tmp"
  touch "$tmp/dev/vda5"
  run_reset_step "$tmp"
  grep -qx 'no block device for the state partition' "$tmp/console"
  if grep -q "^mkfs\.[a-z0-9]* -L COS_STATE" "$tmp/calls"; then false; fi
  grep -qx "mkfs.ext4 -L COS_PERSISTENT $tmp/dev/vda5" "$tmp/calls"
  tail -n 1 "$tmp/calls" | grep -qx 'kairos-agent reset --unattended --reboot'
  rm -rf "$tmp"
}

# The reset formats no persistent partition it cannot find, and the active
# system then fails its boot on the missing persistent mounts.
@test "a worker disk with a labelled state but an empty persistent partition formats only the persistent one" {
  tmp=$(mktemp -d)
  reset_step_fixture "$tmp"
  touch "$tmp/COS_STATE-labelled" "$tmp/dev/vda4" "$tmp/dev/vda4.fs" "$tmp/dev/vda5"
  run_reset_step "$tmp"
  [ "$(grep -c '^mkfs\.' "$tmp/calls")" -eq 1 ]
  grep -qx "mkfs.ext4 -L COS_PERSISTENT $tmp/dev/vda5" "$tmp/calls"
  grep -qF 'NAME SIZE LABEL PARTLABEL' "$tmp/console"
  tail -n 1 "$tmp/calls" | grep -qx 'kairos-agent reset --unattended --reboot'
  rm -rf "$tmp"
}

@test "the kubelet source is checked against the checksum its release publishes before it is unpacked" {
  dockerfile="$ROOT/packages/apps/kubernetes-nodes/images/kairos-worker/Dockerfile"
  stage="$(sed -n '/^FROM .* AS kubelet$/,/^FROM /p' "$dockerfile" | sed 's/\\$//' | tr -s ' \n' ' ')"
  src='https://dl.k8s.io/release/${KUBERNETES_VERSION}/kubernetes-src.tar.gz'
  printf '%s\n' "$stage" | grep -qF "curl -fsSL \"$src\" -o /tmp/src.tgz"
  printf '%s\n' "$stage" | grep -qF "\$(curl -fsSL \"$src.sha512\") /tmp/src.tgz\" | sha512sum -c - && tar -xzf /tmp/src.tgz"
  if grep -qE 'curl [^|]*\| *tar' "$dockerfile"; then
    echo "a download is unpacked straight from curl, before any checksum" >&2
    false
  fi
}

# Release assets on GitHub can be replaced after publication, so a checksum
# fetched beside them proves only that the two still match each other.
@test "GitHub release assets are checked against digests pinned for both architectures" {
  dockerfile="$ROOT/packages/apps/kubernetes-nodes/images/kairos-worker/Dockerfile"
  for name in CONTAINERD RUNC CNI_PLUGINS; do
    for arch in AMD64 ARM64; do
      grep -qE "^ARG ${name}_SHA256_${arch}=[0-9a-f]{64}$" "$dockerfile" || { echo "no pinned ${name}_SHA256_${arch}" >&2; false; }
    done
  done
  for file in /tmp/containerd.tgz /tmp/runc /tmp/cni.tgz; do
    grep -qF "  ${file}\" | sha256sum -c -" "$dockerfile" || { echo "$file is not checked against a pinned digest" >&2; false; }
  done
  if grep -qE 'github\.com/[^ ]*releases/download/[^ ]*(sha256sum|\.sha256)' "$dockerfile"; then
    echo "a checksum is still fetched from the release that serves the asset" >&2
    false
  fi
}

@test "the containerd unit is checked against a digest pinned for its containerd release" {
  dockerfile="$ROOT/packages/apps/kubernetes-nodes/images/kairos-worker/Dockerfile"
  grep -qE '^ARG CONTAINERD_SERVICE_SHA256=[0-9a-f]{64}$' "$dockerfile"
  grep -qF 'echo "${CONTAINERD_SERVICE_SHA256}  usr/lib/systemd/system/containerd.service" | sha256sum -c -' "$dockerfile"
}
