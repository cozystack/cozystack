#!/usr/bin/env bats
# Tests for hack/stitch-multiarch.sh, which joins the amd64 build and the arm64
# leg of every first-party image into one index and repins the tree on it.
#
# The registry is a stub: skopeo and docker on PATH read and write a flat table
# of "<repo> <tag> <digest>" lines plus one file per manifest, so digests are
# real sha256 sums of the stored bytes and an index the docker stub creates
# resolves like any other manifest.
#
# Harness note: run by hack/cozytest.sh, not real bats; each @test is a shell
# function under `set -eu`, sourced into POSIX sh (dash on CI).
#
# Run with: hack/cozytest.sh hack/stitch-multiarch_test.bats

REG=registry.example/cozy

_stub_registry() {
  MOCK_REG="$1"
  export MOCK_REG
  mkdir -p "$MOCK_REG/bin" "$MOCK_REG/blobs"
  : >"$MOCK_REG/tags"
  : >"$MOCK_REG/log"
  cat >"$MOCK_REG/bin/skopeo" <<'EOF'
#!/bin/sh
set -eu
printf 'skopeo %s\n' "$*" >>"$MOCK_REG/log"
resolve() {
  r="${1#docker://}"
  case "$r" in
    *@*) printf '%s' "${r##*@}" ;;
    *) awk -v r="${r%:*}" -v t="${r##*:}" '$1 == r && $2 == t { d = $3 } END { printf "%s", d }' "$MOCK_REG/tags" ;;
  esac
}
case "$1" in
  inspect)
    d="$(resolve "$3")"
    f="$MOCK_REG/blobs/${d#sha256:}"
    [ -n "$d" ] && [ -f "$f" ] || { echo "Error: reading manifest $3: manifest unknown" >&2; exit 1; }
    if [ "$2" = --config ]; then jq '{architecture: .arch}' "$f"; else cat "$f"; fi
    ;;
  list-tags)
    awk -v r="${2#docker://}" '$1 == r { print $2 }' "$MOCK_REG/tags" | sort -u | jq -R . | jq -s '{Tags: .}'
    ;;
  *) exit 2 ;;
esac
EOF
  cat >"$MOCK_REG/bin/docker" <<'EOF'
#!/bin/sh
set -eu
printf 'docker %s\n' "$*" >>"$MOCK_REG/log"
[ "$1 $2 $3" = "buildx imagetools create" ] || exit 2
shift 3
# MOCK_CREATE_LIMIT=N: the (N+1)th create fails, as a registry outage would.
n=$(grep -c 'imagetools create' "$MOCK_REG/log")
if [ -n "${MOCK_CREATE_LIMIT:-}" ] && [ "$n" -gt "$MOCK_CREATE_LIMIT" ]; then
  echo "ERROR: failed to push: 503 Service Unavailable" >&2
  exit 1
fi
tags=""
entries=""
srcs=$(printf '%s\n' "$@" | awk 'prev != "--tag" && $0 != "--tag" { print } { prev = $0 }')
# One source that is already an index is copied as is, like the real tool.
if [ "$(printf '%s\n' "$srcs" | wc -l | tr -d ' ')" -eq 1 ] \
  && jq -e '.manifests' "$MOCK_REG/blobs/$(printf '%s' "${srcs##*@}" | sed 's/^sha256://')" >/dev/null 2>&1; then
  while [ "$#" -gt 0 ]; do
    if [ "$1" = --tag ]; then printf '%s %s %s\n' "${2%:*}" "${2##*:}" "${srcs##*@}" >>"$MOCK_REG/tags"; shift; fi
    shift
  done
  exit 0
fi
while [ "$#" -gt 0 ]; do
  case "$1" in
    --tag) tags="$tags $2"; shift 2 ;;
    *)
      d="${1##*@}"
      arch="$(jq -r .arch "$MOCK_REG/blobs/${d#sha256:}")"
      entries="$entries${entries:+,}{\"digest\":\"$d\",\"platform\":{\"os\":\"linux\",\"architecture\":\"$arch\"}}"
      shift ;;
  esac
done
body="{\"mediaType\":\"application/vnd.oci.image.index.v1+json\",\"manifests\":[$entries]}"
hex="$(printf '%s' "$body" | sha256sum | cut -d' ' -f1)"
printf '%s' "$body" >"$MOCK_REG/blobs/$hex"
for t in $tags; do
  printf '%s %s sha256:%s\n' "${t%:*}" "${t##*:}" "$hex" >>"$MOCK_REG/tags"
done
EOF
  chmod +x "$MOCK_REG/bin/skopeo" "$MOCK_REG/bin/docker"
}

# _push <repo> <tag> <arch> [<config-media-type>] stores a single-platform
# manifest, tags it, and prints its digest.
_push() {
  body="{\"mediaType\":\"application/vnd.oci.image.manifest.v1+json\",\"config\":{\"mediaType\":\"${4:-application/vnd.oci.image.config.v1+json}\"},\"arch\":\"$3\",\"name\":\"$1:$2\"}"
  hex="$(printf '%s' "$body" | sha256sum | cut -d' ' -f1)"
  printf '%s' "$body" >"$MOCK_REG/blobs/$hex"
  printf '%s %s sha256:%s\n' "$1" "$2" "$hex" >>"$MOCK_REG/tags"
  printf 'sha256:%s' "$hex"
}

_tag() {
  awk -v r="$1" -v t="$2" '$1 == r && $2 == t { d = $3 } END { printf "%s", d }' "$MOCK_REG/tags"
}

# A tree stamped by an amd64 build with IMAGE_TAG=main, one image per storage
# shape and values.yaml sub-shape, each with an arm64 twin, plus the refs that
# must be left alone. Sets A_<name> to each amd64 digest.
_make_world() {
  t="$1/tree"
  mkdir -p "$t/system/api" "$t/system/cil" "$t/system/lin" "$t/system/ovn" \
           "$t/apps/kube/images" "$t/system/multus/templates" "$t/core/testing" \
           "$t/core/installer" "$t/system/capi-providers-cpprovider/images" \
           "$t/system/capi-providers-cpprovider/files" "$t/system/stray/files" "$t/system/zipped/files" \
           "$t/system/third" "$t/system/api/charts/up"
  for n in api cil lin ovn signer multus; do
    eval "A_$n=\$(_push $REG/$n main amd64)"
    _push "$REG/$n" main-arm64 arm64 >/dev/null
  done
  # The build pushed api's component version beside the build tag, though the
  # tree ref names only the build tag, as every package but kamaji stamps it;
  # the pushed-tags log is what records it, and it must move with the build
  # tag. Any other tag on the same bytes belongs to another build and must be
  # neither read nor moved.
  printf '%s v9.9.9 %s\n%s pr-1-abc %s\n%s v0.0.1-rc.1 %s\n' \
    "$REG/api" "$A_api" "$REG/api" "$A_api" "$REG/api" "$A_api" >>"$MOCK_REG/tags"
  PUSHED_TAGS_LOG="$1/pushed-tags"
  export PUSHED_TAGS_LOG
  for n in api cil lin ovn signer multus kamaji; do
    printf '%s/%s:main\n' "$REG" "$n" >>"$PUSHED_TAGS_LOG"
  done
  printf '%s/api:v9.9.9\n' "$REG" >>"$PUSHED_TAGS_LOG"
  A_sandbox=$(_push "$REG/sandbox" main amd64)
  for n in kamaji stray zipped; do
    eval "A_$n=\$(_push $REG/$n main amd64)"
    _push "$REG/$n" main-arm64 arm64 >/dev/null
  done
  A_pkg=$(_push "$REG/cozystack-packages" main amd64 application/vnd.cncf.flux.config.v1+json)
  _push "$REG/cozystack-packages" main-arm64 arm64 application/vnd.cncf.flux.config.v1+json >/dev/null
  THIRD="sha256:$(printf 'f%.0s' $(seq 1 64))"

  # values.yaml, single string
  printf 'image: %s/api:main@%s\n' "$REG" "$A_api" >"$t/system/api/values.yaml"
  # values.yaml, split map with a digest key
  printf 'image:\n  repository: %s/cil\n  tag: main\n  digest: %s\n' "$REG" "$A_cil" >"$t/system/cil/values.yaml"
  # values.yaml, split map with the digest inside tag
  printf 'image:\n  repository: %s/lin\n  tag: main@%s\n' "$REG" "$A_lin" >"$t/system/lin/values.yaml"
  # values.yaml, chart-global registry
  printf 'global:\n  registry:\n    address: %s\n  images:\n    kubeovn:\n      repository: ovn\n      tag: main@%s\n' "$REG" "$A_ovn" >"$t/system/ovn/values.yaml"
  # images/*.tag
  # a component-versioned ref: its tag was never pushed by this build, which
  # pushed the image as main like every other
  printf '%s/signer:v7.7.7-cozystack.0@%s\n' "$REG" "$A_signer" >"$t/apps/kube/images/signer.tag"
  # the declared file; the Helm conditional keeps yq from parsing it, as in the
  # real one
  printf '{{- if .Values.x }}\n        - image: %s/multus:main@%s\n{{- end }}\n' "$REG" "$A_multus" \
    >"$t/system/multus/templates/multus-daemonset-thick.yml"
  # no arm64 twin
  printf 'image: %s/sandbox:main@%s\n' "$REG" "$A_sandbox" >"$t/core/testing/values.yaml"
  # the chart ships this ref only inside a gzip built from a plain copy, as the
  # package's image target writes it
  cpp="$t/system/capi-providers-cpprovider"
  printf '%s/kamaji:v0.1.0-cozystack.0@%s\n' "$REG" "$A_kamaji" >"$cpp/images/kamaji.tag"
  printf 'kind: Deployment\nimage: %s/kamaji:v0.1.0-cozystack.0@%s\n' "$REG" "$A_kamaji" >"$cpp/files/control-plane-components.yaml"
  gzip -nc "$cpp/files/control-plane-components.yaml" >"$cpp/files/components.gz"
  # the digest also sits in a file, or a gzip, the rewrite does not reach
  printf '%s/stray:main@%s\n' "$REG" "$A_stray" >"$t/apps/kube/images/stray.tag"
  printf 'image: %s/stray:main@%s\n' "$REG" "$A_stray" >"$t/system/stray/files/copy.yaml"
  printf '%s/zipped:main@%s\n' "$REG" "$A_zipped" >"$t/apps/kube/images/zipped.tag"
  printf 'image: %s/zipped:main@%s\n' "$REG" "$A_zipped" | gzip -nc >"$t/system/zipped/files/copy.gz"
  # the packages artifact, and an image the build does not own
  printf 'cozystackOperator:\n  platformSourceUrl: oci://%s/cozystack-packages\n  platformSourceRef: digest=%s\n' "$REG" "$A_pkg" \
    >"$t/core/installer/values.yaml"
  printf 'image: docker.io/library/busybox:1@%s\n' "$THIRD" >"$t/system/third/values.yaml"
  # a vendored chart pinning the same digest: out of the rewrite and the check
  printf 'image: %s/api:main@%s\n' "$REG" "$A_api" >"$t/system/api/charts/up/values.yaml"
}

@test "every storage shape is repinned on an index of its amd64 and arm64 builds" {
  tmp=$(mktemp -d)
  _stub_registry "$tmp/reg"
  _make_world "$tmp"
  t="$tmp/tree"

  PATH="$MOCK_REG/bin:$PATH" hack/stitch-multiarch.sh "$t/" "$REG" main >"$tmp/out" 2>"$tmp/err"

  for n in api cil lin ovn signer multus; do
    eval "a=\$A_$n"
    i="$(_tag "$REG/$n" main)"
    [ "$i" != "$a" ]
    jq -e --arg a "$a" '.mediaType == "application/vnd.oci.image.index.v1+json" and .manifests[0].digest == $a and .manifests[0].platform.architecture == "amd64" and .manifests[1].platform.architecture == "arm64"' \
      "$MOCK_REG/blobs/${i#sha256:}" >/dev/null
    if grep -rq --exclude-dir=charts "$a" "$t"; then echo "FAIL: $n still pinned on its amd64 digest"; false; fi
    grep -rqF "$i" "$t"
  done
  grep -q "^  digest: $(_tag "$REG/cil" main)\$" "$t/system/cil/values.yaml"
  grep -q "^      tag: main@$(_tag "$REG/ovn" main)\$" "$t/system/ovn/values.yaml"
  grep -q "multus:main@$(_tag "$REG/multus" main)\$" "$t/system/multus/templates/multus-daemonset-thick.yml"

  # the component tag the build pushed moved with the build tag; tags of other
  # builds on the same bytes were never read, let alone moved
  [ "$(_tag "$REG/api" v9.9.9)" = "$(_tag "$REG/api" main)" ]
  [ "$(_tag "$REG/api" pr-1-abc)" = "$A_api" ]
  [ "$(_tag "$REG/api" v0.0.1-rc.1)" = "$A_api" ]
  if grep -q 'list-tags\|api:pr-1-abc\|api:v0.0.1-rc.1' "$MOCK_REG/log"; then echo "FAIL: a tag this build did not push was read"; false; fi
  # vendored charts are neither rewritten nor checked
  grep -q "$A_api" "$t/system/api/charts/up/values.yaml"

  # the gzip is rewritten with its plain copy, to what the image target builds
  i="$(_tag "$REG/kamaji" main)"
  [ "$i" != "$A_kamaji" ]
  grep -q "kamaji:v0.1.0-cozystack.0@$i\$" "$cpp/images/kamaji.tag"
  grep -q "kamaji:v0.1.0-cozystack.0@$i\$" "$cpp/files/control-plane-components.yaml"
  gzip -dc "$cpp/files/components.gz" | grep -q "kamaji:v0.1.0-cozystack.0@$i\$"
  if gzip -dc "$cpp/files/components.gz" | grep -q "$A_kamaji"; then echo "FAIL: the gzip still pins the amd64 digest"; false; fi
  gzip -nc "$cpp/files/control-plane-components.yaml" | cmp - "$cpp/files/components.gz"

  # skipped: no arm64 twin, and a digest pinned outside the enumerated files
  [ "$(_tag "$REG/sandbox" main)" = "$A_sandbox" ]
  grep -q "$A_sandbox" "$t/core/testing/values.yaml"
  for n in stray zipped; do
    eval "a=\$A_$n"
    [ "$(_tag "$REG/$n" main)" = "$a" ]
    grep -q "$a" "$t/apps/kube/images/$n.tag"
  done
  grep -q 'skipping sandbox: no main-arm64 image' "$tmp/err"
  grep -q 'skipping stray: digest also pinned in .*/system/stray/files/copy.yaml' "$tmp/err"
  grep -q 'skipping zipped: digest also pinned in .*/system/zipped/files/copy.gz' "$tmp/err"
  if grep -q 'imagetools create.*\(stray\|zipped\|sandbox\|cozystack-packages\)' "$MOCK_REG/log"; then
    echo "FAIL: a skipped ref was pushed"; false
  fi

  # the packages artifact and third-party refs are untouched
  grep -q "platformSourceRef: digest=$A_pkg" "$t/core/installer/values.yaml"
  grep -q "busybox:1@$THIRD" "$t/system/third/values.yaml"

  [ "$(wc -l <"$tmp/out" | tr -d ' ')" -eq 1 ]
  grep -q '^stitch: 7 stitched (.*kamaji.*); 3 skipped (.*stray.*zipped.*)$' "$tmp/out"
  grep -q 'skipped (.*sandbox.*)$' "$tmp/out"
  rm -rf "$tmp"
}

@test "the rewritten gzip is byte-identical across two stitches of one tree" {
  tmp=$(mktemp -d)
  _stub_registry "$tmp/reg"
  _make_world "$tmp"
  cp -R "$tmp/tree" "$tmp/stamped"
  PATH="$MOCK_REG/bin:$PATH" hack/stitch-multiarch.sh "$tmp/tree" "$REG" main >/dev/null 2>&1
  cp "$tmp/tree/system/capi-providers-cpprovider/files/components.gz" "$tmp/first.gz"
  # The second run finds every tag already on its index and repins on it.
  rm -rf "$tmp/tree"
  cp -R "$tmp/stamped" "$tmp/tree"
  sleep 1
  PATH="$MOCK_REG/bin:$PATH" hack/stitch-multiarch.sh "$tmp/tree" "$REG" main >/dev/null 2>&1
  cmp "$tmp/first.gz" "$tmp/tree/system/capi-providers-cpprovider/files/components.gz"
  if cmp -s "$tmp/first.gz" "$tmp/stamped/system/capi-providers-cpprovider/files/components.gz"; then echo "FAIL: the gzip was not rewritten"; false; fi
  rm -rf "$tmp"
}

@test "a digest left inside the gzip fails the run" {
  # A sed that rewrites files but passes stdin through stands in for a rewrite
  # that misses the gzip payload alone; grep -I would never look inside it.
  tmp=$(mktemp -d)
  _stub_registry "$tmp/reg"
  _make_world "$tmp"
  real_sed="$(command -v sed)"
  printf '#!/bin/sh\ncase "$1" in s\\|*) [ -n "${2:-}" ] || exec cat ;; esac\nexec %s "$@"\n' "$real_sed" >"$MOCK_REG/bin/sed"
  chmod +x "$MOCK_REG/bin/sed"
  rc=0
  PATH="$MOCK_REG/bin:$PATH" hack/stitch-multiarch.sh "$tmp/tree" "$REG" main >"$tmp/out" 2>"$tmp/err" || rc=$?
  [ "$rc" -ne 0 ]
  grep -q "${A_kamaji} is still pinned after the rewrite, in: .*components.gz" "$tmp/err"
  if grep -q "is still pinned after the rewrite, in: .*control-plane-components.yaml" "$tmp/err"; then echo "FAIL: the plain copy was not rewritten"; false; fi
  rm -rf "$tmp"
}

@test "prepare-release applies the amd64 build's tree, stitches, gates, then republishes before the commit" {
  # The amd64 build pushed the artifact and chart pinned on amd64-only
  # digests. The chart's values pin the operator image and the digest
  # image-packages writes, so it must be packaged after that write, and the
  # gate must see the stitched tree before anything is published from it.
  wf=.github/workflows/tags.yaml
  names=$(yq -r '.jobs.prepare-release.steps[].name' "$wf")
  d=$(echo "$names" | grep -nx 'Download the amd64 build' | cut -d: -f1)
  a=$(echo "$names" | grep -nx 'Apply the stamped tree' | cut -d: -f1)
  st=$(echo "$names" | grep -nx 'Stitch multi-arch indexes and republish' | cut -d: -f1)
  c=$(echo "$names" | grep -nx 'Commit release artifacts' | cut -d: -f1)
  u=$(echo "$names" | grep -nx 'Upload assets' | cut -d: -f1)
  [ -n "$d" ] && [ -n "$a" ] && [ -n "$st" ] && [ -n "$c" ] && [ -n "$u" ] \
    && [ "$d" -lt "$a" ] && [ "$a" -lt "$st" ] && [ "$st" -lt "$c" ] && [ "$c" -lt "$u" ] \
    || { echo "FAIL: prepare-release steps are out of order"; false; }
  run=$(yq -r '.jobs.prepare-release.steps[] | select(.name == "Stitch multi-arch indexes and republish") | .run' "$wf")
  s=$(echo "$run" | grep -n 'hack/stitch-multiarch.sh packages ' | cut -d: -f1)
  v=$(echo "$run" | grep -n 'hack/verify-multiarch.sh packages$' | cut -d: -f1)
  p=$(echo "$run" | grep -n 'make -C packages/core/installer image-packages chart$' | cut -d: -f1)
  [ -n "$s" ] && [ -n "$v" ] && [ -n "$p" ] && [ "$s" -lt "$v" ] && [ "$v" -lt "$p" ] \
    || { echo "FAIL: stitch, gate and republish are out of order"; false; }
  out=$(make -n -C packages/core/installer image-packages chart COZYSTACK_VERSION=0)
  ref=$(echo "$out" | grep -n 'platformSourceRef = ' | cut -d: -f1)
  pkg=$(echo "$out" | grep -n 'helm package' | cut -d: -f1)
  [ -n "$ref" ] && [ -n "$pkg" ] && [ "$ref" -lt "$pkg" ]
  echo "$out" | grep -q 'helm push'
}

@test "the amd64 build hands its tree, tag log and assets to prepare-release, and a missing hand-off fails" {
  wf=.github/workflows/tags.yaml
  up='.jobs.build-amd64.steps[] | select(.uses // "" | test("actions/upload-artifact@"))'
  down='.jobs.prepare-release.steps[] | select(.uses // "" | test("actions/download-artifact@"))'
  [ "$(yq -r "[$up] | length" "$wf")" -ge 2 ]
  [ "$(yq -r "[$up | select(.with.\"if-no-files-found\" != \"error\")] | length" "$wf")" -eq 0 ]
  # Every artifact uploaded is downloaded under the same name.
  [ "$(yq -r "$up | .with.name" "$wf" | sort)" = "$(yq -r "$down | .with.name" "$wf" | sort)" ]
  # The tag log the Build step writes is the file the stitch reads, and it
  # travels in an uploaded artifact that lands at that same path.
  build_log=$(yq -r '.jobs.build-amd64.steps[] | select(.name == "Build") | .env.PUSHED_TAGS_LOG' "$wf")
  stitch_log=$(yq -r '.jobs.prepare-release.steps[] | select(.name == "Stitch multi-arch indexes and republish") | .env.PUSHED_TAGS_LOG' "$wf")
  [ -n "$build_log" ] && [ "$build_log" != null ] && [ "$build_log" = "$stitch_log" ]
  dir="${build_log%/*}"
  yq -r "$up | .with.path" "$wf" | grep -qxF "$dir/"
  yq -r "$down | .with.path" "$wf" | grep -qxF "$dir"
  # The stamped tree is captured after the build and applied before the stitch.
  capture=$(yq -r '.jobs.build-amd64.steps[] | select(.name == "Capture the stamped tree") | .run' "$wf")
  echo "$capture" | grep -q "git diff --cached --binary HEAD >\"$dir/stamped-tree.patch\""
  yq -r '.jobs.prepare-release.steps[] | select(.name == "Apply the stamped tree") | .run' "$wf" \
    | grep -q "git apply --binary \"$dir/stamped-tree.patch\""
}

@test "the amd64 and arm64 builds run side by side, prepare-release waits for both, and a stable tag runs without arm64" {
  wf=.github/workflows/tags.yaml
  [ "$(yq -r '.jobs.build-amd64.needs // "none"' "$wf")" = none ]
  [ "$(yq -r '.jobs.build-arm64.needs // "none"' "$wf")" = none ]
  [ "$(yq -r '.jobs.build-arm64.if' "$wf")" = "contains(github.ref_name, '-')" ]
  # The amd64 job runs for every tag: it holds the release-exists no-op and the
  # hand-pushed stable tag rejection.
  [ "$(yq -r '.jobs.build-amd64.if // "none"' "$wf")" = none ]
  amd_steps=$(yq -r '.jobs.build-amd64.steps[].name' "$wf")
  echo "$amd_steps" | grep -qx 'Check if release already exists'
  echo "$amd_steps" | grep -qx 'Reject hand-pushed stable tags'
  [ "$(yq -r '.jobs.build-arm64.steps[] | select(.run // "" | test("build-matrix")) | .env.IMAGE_TAG' "$wf")" = '${{ github.ref_name }}-arm64' ]
  [ "$(yq -r '.jobs.prepare-release.needs | sort | join(" ")' "$wf")" = 'build-amd64 build-arm64' ]
  cond=$(yq -r '.jobs.prepare-release.if' "$wf")
  echo "$cond" | grep -qF '!cancelled()'
  echo "$cond" | grep -qF "needs.build-amd64.result == 'success'"
  echo "$cond" | grep -qF "needs.build-arm64.result == 'success'"
  echo "$cond" | grep -qF "needs.build-arm64.result == 'skipped'"
  if echo "$cond" | grep -qE "always\(\)|'failure'"; then echo "FAIL: a failed build must not let the release through"; false; fi
  # prepare-release no-ops exactly when the amd64 job found the release.
  [ "$(yq -r '.jobs.prepare-release.outputs.release_exists' "$wf")" = '${{ needs.build-amd64.outputs.release_exists }}' ]
  # A stable tag skips build-arm64, and a skipped ancestor makes the implicit
  # success() false for every job below it, so each one needs a status
  # function of its own or it silently never runs on a stable tag.
  down=prepare-release
  while :; do
    more=$(yq -r '.jobs | keys | .[]' "$wf" | while read -r j; do
      needs=$(yq -r "[.jobs.\"$j\".needs // []] | flatten | .[]" "$wf")
      for d in $down; do
        if echo "$needs" | grep -qxF "$d" && ! echo " $down " | grep -qF " $j "; then echo "$j"; break; fi
      done
    done | sort -u)
    [ -n "$more" ] || break
    down="$down $(echo "$more" | tr '\n' ' ')"
  done
  for j in $down; do
    yq -r ".jobs.\"$j\".if // \"\"" "$wf" | grep -qE '!cancelled\(\)|always\(\)' \
      || { echo "FAIL: $j runs below build-arm64 without a status function"; false; }
  done
  echo " $down " | grep -qF ' generate-changelog '
  echo " $down " | grep -qF ' rc-e2e '
  echo " $down " | grep -qF ' update-website-docs '
  # A re-run of an rc whose release is already prepared rebuilds nothing.
  [ "$(yq -r '.jobs.build-arm64.steps[] | select(.name == "Build arm64 images") | .if' "$wf")" = "steps.check_release.outputs.release_exists == 'false'" ]
  [ "$(yq -r '.jobs.build-amd64.steps[] | select(.name == "Build") | .if' "$wf")" = "steps.check_release.outputs.release_exists == 'false'" ]
  # The stitch and the gate need skopeo and mikefarah yq on the release runner.
  tool=$(yq -r '.jobs.prepare-release.steps[] | select(.name == "Set up build toolchain") | .run' "$wf")
  echo "$tool" | grep -q skopeo
  echo "$tool" | grep -q 'yq --version | grep -q mikefarah'
  # prepare-release pushes the packages artifact with flux, so it installs the
  # same flux build-amd64 does rather than whatever is latest.
  want=$(yq -r '.jobs.build-amd64.steps[] | select(.name == "Set up flux") | .with.version' "$wf")
  [ -n "$want" ] && [ "$want" != null ]
  [ "$(yq -r '.jobs.prepare-release.steps[] | select(.name == "Set up flux") | .with.version' "$wf")" = "$want" ]
}

@test "the amd64 release job rebuilds matchbox as a two-arch index before it captures the tree" {
  # matchbox has no arm64 twin to stitch, so this rebuild is the only thing
  # that makes the ref the gate checks an index. It needs a builder that can
  # push an index while make build stays on the default docker driver.
  wf=.github/workflows/tags.yaml
  steps=$(yq -r '.jobs.build-amd64.steps[].name' "$wf")
  pos() { echo "$steps" | grep -nxF "$1" | cut -d: -f1; }
  bx=$(pos 'Set up Buildx for matchbox (docker-container driver)')
  build=$(pos 'Build')
  capture=$(pos 'Capture the stamped tree')
  [ -n "$bx" ] && [ -n "$build" ] && [ -n "$capture" ]
  [ "$bx" -lt "$build" ] && [ "$build" -lt "$capture" ]
  sel='.jobs.build-amd64.steps[] | select(.id == "matchbox_buildx")'
  [ "$(yq -r "$sel | .with.driver" "$wf")" = docker-container ]
  [ "$(yq -r "$sel | .with.use" "$wf")" = false ]
  run_=$(yq -r '.jobs.build-amd64.steps[] | select(.name == "Build") | .run' "$wf")
  echo "$run_" | grep -qxE '[[:space:]]*make -C packages/core/talos image-matchbox PLATFORM=linux/amd64,linux/arm64 BUILDER="\$MATCHBOX_BUILDER"'
  [ "$(yq -r '.jobs.build-amd64.steps[] | select(.name == "Build") | .env.MATCHBOX_BUILDER' "$wf")" = '${{ steps.matchbox_buildx.outputs.name }}' ]
}

@test "an arm64 tag that holds no arm64 image is skipped, not stitched" {
  # The testing package pins amd64 (an x86 KVM sandbox), so a stray
  # <tag>-arm64 of it holds a second amd64 image. An index of two amd64
  # manifests would be wrong.
  tmp=$(mktemp -d)
  _stub_registry "$tmp/reg"
  _make_world "$tmp"
  _push "$REG/sandbox" main-arm64 amd64 >/dev/null

  PATH="$MOCK_REG/bin:$PATH" hack/stitch-multiarch.sh "$tmp/tree" "$REG" main >"$tmp/out" 2>"$tmp/err"

  [ "$(_tag "$REG/sandbox" main)" = "$A_sandbox" ]
  grep -q "$A_sandbox" "$tmp/tree/core/testing/values.yaml"
  grep -q 'skipping sandbox: main and main-arm64 are not one amd64 and one arm64 image' "$tmp/err"
  rm -rf "$tmp"
}

@test "a rerun after a stitch that failed partway repins every ref" {
  # The stitch job starts from the amd64 build's stamped tree every time, while
  # the registry keeps what the failed attempt already pushed: some tags are
  # indexes by then. Those must be repinned on the existing index, not skipped,
  # or the rerun goes green with those images left amd64 only.
  tmp=$(mktemp -d)
  _stub_registry "$tmp/reg"
  _make_world "$tmp"
  cp -R "$tmp/tree" "$tmp/stamped"

  rc=0
  PATH="$MOCK_REG/bin:$PATH" MOCK_CREATE_LIMIT=2 hack/stitch-multiarch.sh "$tmp/tree" "$REG" main >"$tmp/out" 2>"$tmp/err" || rc=$?
  [ "$rc" -ne 0 ]
  [ "$(grep -c 'imagetools create' "$MOCK_REG/log")" -eq 3 ]

  # An attempt that died inside imagetools create may leave a tag behind on A.
  printf '%s v9.9.9 %s\n' "$REG/api" "$A_api" >>"$MOCK_REG/tags"

  rm -rf "$tmp/tree"
  cp -R "$tmp/stamped" "$tmp/tree"
  PATH="$MOCK_REG/bin:$PATH" hack/stitch-multiarch.sh "$tmp/tree" "$REG" main >"$tmp/out" 2>"$tmp/err"
  [ "$(_tag "$REG/api" v9.9.9)" = "$(_tag "$REG/api" main)" ]

  for n in api cil lin ovn signer multus kamaji; do
    eval "a=\$A_$n"
    i="$(_tag "$REG/$n" main)"
    [ "$i" != "$a" ]
    if grep -rq --exclude-dir=charts "$a" "$tmp/tree"; then echo "FAIL: $n still pinned on its amd64 digest"; false; fi
    grep -rqF "$i" "$tmp/tree"
  done
  grep -q '^stitch: 7 stitched ' "$tmp/out"
  if grep -q 'no longer points\|skipping \(api\|cil\|lin\|ovn\|signer\|multus\|kamaji\)' "$tmp/err"; then
    echo "FAIL: the rerun skipped an image"; false
  fi

  # A tag moved to anything but an index of the pinned image and an arm64
  # image fails: a plain other image; another build's index; an index holding
  # the twin but another amd64 image, as after a newer main run moved both
  # tags; an index holding the pinned image and no arm64 image.
  other_amd=$(_push "$REG/api" other amd64)
  other_arm=$(_push "$REG/api" other-arm64 arm64)
  b_api=$(_tag "$REG/api" main-arm64)
  for sources in "single" "$REG/api@$other_amd $REG/api@$other_arm" "$REG/api@$other_amd $REG/api@$b_api" "$REG/api@$A_api $REG/api@$other_amd"; do
    if [ "$sources" = single ]; then
      _push "$REG/api" main amd64-other >/dev/null
    else
      # shellcheck disable=SC2086
      PATH="$MOCK_REG/bin:$PATH" docker buildx imagetools create --tag "$REG/api:main" $sources
    fi
    rm -rf "$tmp/tree"
    cp -R "$tmp/stamped" "$tmp/tree"
    rc=0
    PATH="$MOCK_REG/bin:$PATH" hack/stitch-multiarch.sh "$tmp/tree" "$REG" main >"$tmp/out" 2>"$tmp/err" || rc=$?
    [ "$rc" -ne 0 ] || { echo "FAIL: api:main on '$sources' was accepted"; false; }
    grep -q "api:main points at .*neither the digest the tree pins" "$tmp/err"
  done
  rm -rf "$tmp"
}

@test "a rerun whose arm64 leg rebuilt the image repins on the index already there" {
  # A rerun starts again from the amd64 build's stamped tree, but its arm64 leg
  # pushes a new arm64 image unless the cache reproduces it byte for byte. The
  # build tag then sits on an index of the pinned amd64 image and the previous
  # arm64 half. That is a valid index of the pinned image.
  tmp=$(mktemp -d)
  _stub_registry "$tmp/reg"
  _make_world "$tmp"
  cp -R "$tmp/tree" "$tmp/stamped"
  PATH="$MOCK_REG/bin:$PATH" hack/stitch-multiarch.sh "$tmp/tree" "$REG" main >/dev/null 2>&1
  first="$(_tag "$REG/api" main)"

  for n in api cil lin ovn signer multus kamaji; do
    _push "$REG/$n" main-arm64 arm64 application/vnd.docker.container.image.v1+json >/dev/null
  done
  rm -rf "$tmp/tree"
  cp -R "$tmp/stamped" "$tmp/tree"
  PATH="$MOCK_REG/bin:$PATH" hack/stitch-multiarch.sh "$tmp/tree" "$REG" main >"$tmp/out" 2>"$tmp/err"

  [ "$(_tag "$REG/api" main)" = "$first" ]
  grep -q "api:main@$first\$" "$tmp/tree/system/api/values.yaml"
  grep -q '^stitch: 7 stitched ' "$tmp/out"
  rm -rf "$tmp"
}

@test "a digest the rewrite leaves behind fails the run" {
  # Scan wider than you rewrite. A sed that rewrites nothing stands in for any
  # way the rewrite can miss a copy of a digest it has already re-pointed.
  tmp=$(mktemp -d)
  _stub_registry "$tmp/reg"
  _make_world "$tmp"
  real_sed="$(command -v sed)"
  printf '#!/bin/sh\ncase "$1" in s\\|*) cat "$2" ;; *) exec %s "$@" ;; esac\n' "$real_sed" >"$MOCK_REG/bin/sed"
  chmod +x "$MOCK_REG/bin/sed"

  rc=0
  PATH="$MOCK_REG/bin:$PATH" hack/stitch-multiarch.sh "$tmp/tree" "$REG" main >"$tmp/out" 2>"$tmp/err" || rc=$?

  [ "$rc" -ne 0 ]
  grep -q "$A_api is still pinned after the rewrite, in: .*/system/api/values.yaml" "$tmp/err"
  rm -rf "$tmp"
}

@test "a registry error fails the run at every lookup instead of becoming a skip" {
  # Skipping on a rate limit would leave the image amd64 only while the run
  # reports success. Each lookup the stitch makes for one image fails in turn,
  # on every try: the arm64 twin, the build tag, the architecture of either
  # half, and the ref's own tag read while collecting the tags to move. api is
  # processed first, so the failure lands before anything is pushed and the
  # world can be reused.
  tmp=$(mktemp -d)
  _stub_registry "$tmp/reg"
  _make_world "$tmp"
  mv "$MOCK_REG/bin/skopeo" "$MOCK_REG/bin/skopeo.real"
  {
    echo '#!/bin/sh'
    echo 'case "$*" in'
    echo '  $MOCK_FAIL) echo "received unexpected HTTP status: 429 Too Many Requests" >&2; exit 1 ;;'
    echo 'esac'
    echo 'exec "$MOCK_REG/bin/skopeo.real" "$@"'
  } >"$MOCK_REG/bin/skopeo"
  chmod +x "$MOCK_REG/bin/skopeo"

  for fail in \
    "inspect --raw docker://$REG/api:main-arm64" \
    "inspect --raw docker://$REG/api:main" \
    "inspect --raw docker://$REG/api@$A_api" \
    "inspect --config docker://$REG/api@*" \
    "inspect --raw docker://$REG/api:v9.9.9"; do
    rc=0
    PATH="$MOCK_REG/bin:$PATH" MOCK_FAIL="$fail" STITCH_RETRY_DELAY=0 hack/stitch-multiarch.sh "$tmp/tree" "$REG" main >"$tmp/out" 2>"$tmp/err" || rc=$?
    [ "$rc" -ne 0 ] || { echo "FAIL: a 429 on '$fail' did not fail the run"; false; }
    grep -q '429 Too Many Requests' "$tmp/err"
    if grep -q 'skipping' "$tmp/err"; then echo "FAIL: a 429 on '$fail' became a skip"; false; fi
  done
  grep -q "$A_api" "$tmp/tree/system/api/values.yaml"
  if grep -q 'imagetools create' "$MOCK_REG/log"; then echo "FAIL: pushed despite a registry error"; false; fi
  rm -rf "$tmp"
}

@test "only a rate limit or a 5xx is retried, and a missing tag log fails the run" {
  tmp=$(mktemp -d)
  _stub_registry "$tmp/reg"
  _make_world "$tmp"
  mv "$MOCK_REG/bin/skopeo" "$MOCK_REG/bin/skopeo.real"
  # MOCK_ERR is answered to the first MOCK_TIMES lookups of api's arm64 twin;
  # every lookup of it is counted.
  {
    echo '#!/bin/sh'
    echo 'if [ "$*" = "inspect --raw docker://$MOCK_TWIN" ]; then'
    echo '  echo x >>"$MOCK_REG/twin-calls"'
    echo '  if [ "$(wc -l <"$MOCK_REG/twin-calls")" -le "$MOCK_TIMES" ]; then echo "$MOCK_ERR" >&2; exit 1; fi'
    echo 'fi'
    echo 'exec "$MOCK_REG/bin/skopeo.real" "$@"'
  } >"$MOCK_REG/bin/skopeo"
  chmod +x "$MOCK_REG/bin/skopeo"
  cp -R "$tmp/tree" "$tmp/stamped"

  # Deterministic: asked once, and the run fails.
  rc=0
  PATH="$MOCK_REG/bin:$PATH" MOCK_TWIN="$REG/api:main-arm64" MOCK_TIMES=99 STITCH_RETRY_DELAY=0 \
    MOCK_ERR="Error: reading manifest main-arm64: unauthorized: authentication required" \
    hack/stitch-multiarch.sh "$tmp/tree" "$REG" main >"$tmp/out" 2>"$tmp/err" || rc=$?
  [ "$rc" -ne 0 ]
  grep -q 'unauthorized: authentication required' "$tmp/err"
  [ "$(wc -l <"$MOCK_REG/twin-calls" | tr -d ' ')" -eq 1 ]

  # A 5xx that clears on the second try.
  rm -f "$MOCK_REG/twin-calls"
  PATH="$MOCK_REG/bin:$PATH" MOCK_TWIN="$REG/api:main-arm64" MOCK_TIMES=1 STITCH_RETRY_DELAY=0 \
    MOCK_ERR="Error: reading manifest main-arm64: received unexpected HTTP status: 503 Service Unavailable" \
    hack/stitch-multiarch.sh "$tmp/tree" "$REG" main >"$tmp/out" 2>"$tmp/err"
  [ "$(wc -l <"$MOCK_REG/twin-calls" | tr -d ' ')" -eq 2 ]
  grep -q '^stitch: 7 stitched (api, ' "$tmp/out"

  # A tag log that was asked for and never written.
  rm -rf "$tmp/tree"
  cp -R "$tmp/stamped" "$tmp/tree"
  rc=0
  PATH="$MOCK_REG/bin:$PATH" PUSHED_TAGS_LOG="$tmp/missing" hack/stitch-multiarch.sh "$tmp/tree" "$REG" main >"$tmp/out" 2>"$tmp/err" || rc=$?
  [ "$rc" -ne 0 ]
  grep -q "PUSHED_TAGS_LOG '$tmp/missing' does not exist" "$tmp/err"
  rm -rf "$tmp"
}

@test "a rate limit that clears within the tries does not fail the run" {
  tmp=$(mktemp -d)
  _stub_registry "$tmp/reg"
  _make_world "$tmp"
  mv "$MOCK_REG/bin/skopeo" "$MOCK_REG/bin/skopeo.real"
  # The first two lookups of api's arm64 twin are refused, the third answered.
  {
    echo '#!/bin/sh'
    echo 'if [ "$*" = "inspect --raw docker://$MOCK_TWIN" ]; then'
    echo '  n=$(cat "$MOCK_REG/refused" 2>/dev/null || echo 0)'
    echo '  if [ "$n" -lt 2 ]; then echo $((n + 1)) >"$MOCK_REG/refused"; echo "toomanyrequests: rate limit" >&2; exit 1; fi'
    echo 'fi'
    echo 'exec "$MOCK_REG/bin/skopeo.real" "$@"'
  } >"$MOCK_REG/bin/skopeo"
  chmod +x "$MOCK_REG/bin/skopeo"
  PATH="$MOCK_REG/bin:$PATH" MOCK_TWIN="$REG/api:main-arm64" STITCH_RETRY_DELAY=0 \
    hack/stitch-multiarch.sh "$tmp/tree" "$REG" main >"$tmp/out" 2>"$tmp/err"
  [ "$(cat "$MOCK_REG/refused")" -eq 2 ]
  grep -q '^stitch: 7 stitched (api, ' "$tmp/out"
  rm -rf "$tmp"
}
