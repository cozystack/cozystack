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
           "$t/core/installer" "$t/system/cpp/images" "$t/system/cpp/files" \
           "$t/system/third" "$t/system/api/charts/up"
  for n in api cil lin ovn signer multus; do
    eval "A_$n=\$(_push $REG/$n main amd64)"
    _push "$REG/$n" main-arm64 arm64 >/dev/null
  done
  # A component-versioned tag beside the build tag must move with it, and a
  # build-unique PR tag must not.
  printf '%s v9.9.9 %s\n%s pr-1-abc %s\n' "$REG/api" "$A_api" "$REG/api" "$A_api" >>"$MOCK_REG/tags"
  A_sandbox=$(_push "$REG/sandbox" main amd64)
  A_kamaji=$(_push "$REG/kamaji" main amd64)
  _push "$REG/kamaji" main-arm64 arm64 >/dev/null
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
  # the digest also sits in a file the rewrite does not reach
  printf '%s/kamaji:v0.1.0-cozystack.0@%s\n' "$REG" "$A_kamaji" >"$t/system/cpp/images/kamaji.tag"
  printf 'image: %s/kamaji:v0.1.0-cozystack.0@%s\n' "$REG" "$A_kamaji" >"$t/system/cpp/files/components.yaml"
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

  # the component-versioned tag moved with the build tag; the PR tag did not
  [ "$(_tag "$REG/api" v9.9.9)" = "$(_tag "$REG/api" main)" ]
  [ "$(_tag "$REG/api" pr-1-abc)" = "$A_api" ]
  # vendored charts are neither rewritten nor checked
  grep -q "$A_api" "$t/system/api/charts/up/values.yaml"

  # skipped: no arm64 twin, and a digest pinned outside the enumerated files
  [ "$(_tag "$REG/sandbox" main)" = "$A_sandbox" ]
  grep -q "$A_sandbox" "$t/core/testing/values.yaml"
  [ "$(_tag "$REG/kamaji" main)" = "$A_kamaji" ]
  grep -q "$A_kamaji" "$t/system/cpp/images/kamaji.tag"
  grep -q 'skipping sandbox: no main-arm64 image' "$tmp/err"
  grep -q 'skipping kamaji: digest also pinned in .*/system/cpp/files/components.yaml' "$tmp/err"
  if grep -q 'imagetools create.*\(kamaji\|sandbox\|cozystack-packages\)' "$MOCK_REG/log"; then
    echo "FAIL: a skipped ref was pushed"; false
  fi

  # the packages artifact and third-party refs are untouched
  grep -q "platformSourceRef: digest=$A_pkg" "$t/core/installer/values.yaml"
  grep -q "busybox:1@$THIRD" "$t/system/third/values.yaml"

  [ "$(wc -l <"$tmp/out" | tr -d ' ')" -eq 1 ]
  grep -q '^stitch: 6 stitched (.*); 2 skipped (.*kamaji.*)$' "$tmp/out"
  grep -q 'skipped (.*sandbox.*)$' "$tmp/out"
  rm -rf "$tmp"
}

@test "the stitch jobs republish the packages artifact and then the chart from the rewritten tree" {
  # The amd64 build pushed both before the stitch, pinned on amd64-only
  # digests. The chart's values pin the operator image and the digest
  # image-packages writes, so it must be packaged after that write.
  for wf in .github/workflows/build-main.yaml .github/workflows/build-release.yaml; do
    [ "$(yq -r '.jobs.stitch.needs | length' "$wf")" -eq 2 ]
    run=$(yq -r '.jobs.stitch.steps[] | select(.run // "" | test("stitch-multiarch")) | .run' "$wf")
    s=$(echo "$run" | grep -n 'hack/stitch-multiarch.sh' | cut -d: -f1)
    p=$(echo "$run" | grep -n 'make -C packages/core/installer image-packages chart$' | cut -d: -f1)
    [ -n "$s" ] && [ -n "$p" ] && [ "$s" -lt "$p" ] || { echo "FAIL: $wf does not republish artifact and chart after the stitch"; false; }
  done
  out=$(make -n -C packages/core/installer image-packages chart COZYSTACK_VERSION=0)
  ref=$(echo "$out" | grep -n 'platformSourceRef = ' | cut -d: -f1)
  pkg=$(echo "$out" | grep -n 'helm package' | cut -d: -f1)
  [ -n "$ref" ] && [ -n "$pkg" ] && [ "$ref" -lt "$pkg" ]
  echo "$out" | grep -q 'helm push'
}

@test "an arm64 tag that holds no arm64 image is skipped, not stitched" {
  # The talos and testing packages pin amd64, so a stray <tag>-arm64 of theirs
  # holds a second amd64 image. An index of two amd64 manifests would be wrong.
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

  for n in api cil lin ovn signer multus; do
    eval "a=\$A_$n"
    i="$(_tag "$REG/$n" main)"
    [ "$i" != "$a" ]
    if grep -rq --exclude-dir=charts "$a" "$tmp/tree"; then echo "FAIL: $n still pinned on its amd64 digest"; false; fi
    grep -rqF "$i" "$tmp/tree"
  done
  grep -q '^stitch: 6 stitched ' "$tmp/out"
  if grep -q 'no longer points\|skipping \(api\|cil\|lin\|ovn\|signer\|multus\)' "$tmp/err"; then
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

@test "a line whose tag main's stitch already moved is repinned on main's index" {
  # A line cut from main starts with its tag on main's amd64 images, and main's
  # stitch moves every non-PR tag on those images to its index. The line's own
  # arm64 leg pushes a different arm64 image (no full cache hit), so the line's
  # stitch finds its tag on an index of the right amd64 image and someone
  # else's arm64 half. That is a valid index of the pinned image.
  tmp=$(mktemp -d)
  _stub_registry "$tmp/reg"
  _make_world "$tmp"
  cp -R "$tmp/tree" "$tmp/stamped"
  printf '%s release-1.3 %s\n' "$REG/api" "$A_api" >>"$MOCK_REG/tags"

  PATH="$MOCK_REG/bin:$PATH" hack/stitch-multiarch.sh "$tmp/tree" "$REG" main >/dev/null 2>&1
  main_index="$(_tag "$REG/api" main)"
  [ "$(_tag "$REG/api" release-1.3)" = "$main_index" ]

  # The line's stitch: api's line tag already moved, the rest are fresh.
  for n in cil lin ovn signer multus; do
    eval "a=\$A_$n"
    printf '%s release-1.3 %s\n' "$REG/$n" "$a" >>"$MOCK_REG/tags"
  done
  for n in api cil lin ovn signer multus; do
    _push "$REG/$n" release-1.3-arm64 arm64 >/dev/null
  done
  rm -rf "$tmp/tree"
  cp -R "$tmp/stamped" "$tmp/tree"
  PATH="$MOCK_REG/bin:$PATH" hack/stitch-multiarch.sh "$tmp/tree" "$REG" release-1.3 >"$tmp/out" 2>"$tmp/err"

  grep -q "api:main@$main_index\$" "$tmp/tree/system/api/values.yaml"
  grep -q '^stitch: 6 stitched ' "$tmp/out"
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
  # reports success. Each lookup the stitch makes for one image fails in turn:
  # the arm64 twin, the build tag, the architecture of either half, and a tag
  # read while collecting the tags to move. api is processed first, so the
  # failure lands before anything is pushed and the world can be reused.
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
    PATH="$MOCK_REG/bin:$PATH" MOCK_FAIL="$fail" hack/stitch-multiarch.sh "$tmp/tree" "$REG" main >"$tmp/out" 2>"$tmp/err" || rc=$?
    [ "$rc" -ne 0 ] || { echo "FAIL: a 429 on '$fail' did not fail the run"; false; }
    grep -q '429 Too Many Requests' "$tmp/err"
    if grep -q 'skipping' "$tmp/err"; then echo "FAIL: a 429 on '$fail' became a skip"; false; fi
  done
  grep -q "$A_api" "$tmp/tree/system/api/values.yaml"
  if grep -q 'imagetools create' "$MOCK_REG/log"; then echo "FAIL: pushed despite a registry error"; false; fi
  rm -rf "$tmp"
}
