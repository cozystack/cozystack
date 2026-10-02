#!/usr/bin/env bats
# Tests for hack/collect-images.sh, which lists the images the e2e sandbox's
# Talos nodes hold. Its image-refs.txt feeds the runtime multi-arch audit, so
# it must name every pulled image by the digest the node resolved, and must not
# be written at all when a node could not be read.
#
# talosctl is a stub: it prints the `images ls` table held in
# $MOCK_TALOS/<node>-<namespace>, or fails for a node listed in $MOCK_TALOS/down.
#
# Harness note: the unit lane runs this file under Bats. The bodies stay POSIX
# sh (no `run`, no [[ ]]; each @test a shell function under `set -eu`), so
# hack/cozytest.sh can still run it under dash by hand.
#
# Run with: bats hack/collect-images_test.bats

load test_helper

_stub_talos() {
  MOCK_TALOS="$1"
  export MOCK_TALOS
  mkdir -p "$MOCK_TALOS/bin"
  : >"$MOCK_TALOS/down"
  cat >"$MOCK_TALOS/bin/talosctl" <<'EOF'
#!/bin/sh
set -eu
node="$2"
ns=cri
[ "$6" = --namespace ] && ns="$7"
if grep -qxF "$node" "$MOCK_TALOS/down"; then echo "rpc error: connection refused" >&2; exit 1; fi
printf 'NODE IMAGE DIGEST SIZE LABELS CREATED\n'
[ -f "$MOCK_TALOS/$node-$ns" ] && cat "$MOCK_TALOS/$node-$ns"
exit 0
EOF
  chmod +x "$MOCK_TALOS/bin/talosctl"
}

D1="sha256:$(printf '1%.0s' $(seq 1 64))"
D2="sha256:$(printf '2%.0s' $(seq 1 64))"
D3="sha256:$(printf '3%.0s' $(seq 1 64))"

@test "image-refs.txt names every image a node holds by the digest it resolved" {
  tmp=$(mktemp -d)
  _stub_talos "$tmp/talos"
  n=192.168.123.11
  {
    printf '%s ghcr.io/cozy/api:v1 %s 1MB x now\n' "$n" "$D1"
    printf '%s ghcr.io/cozy/api@%s %s 1MB x now\n' "$n" "$D1" "$D1"
    printf '%s docker.io/library/busybox@%s %s 1MB x now\n' "$n" "$D2" "$D2"
    # An image ID alias, not a pull: it names no repository.
    printf '%s %s %s 1MB x now\n' "$n" "$D3" "$D3"
  } >"$MOCK_TALOS/$n-cri"
  printf '%s registry.example:5000/etcd:v3 %s 1MB x now\n' 192.168.123.12 "$D3" >"$MOCK_TALOS/192.168.123.12-system"
  cp hack/collect-images.sh "$tmp/"
  (cd "$tmp" && PATH="$MOCK_TALOS/bin:$PATH" sh ./collect-images.sh)
  printf '%s\n' "docker.io/library/busybox@$D2" "ghcr.io/cozy/api@$D1" "registry.example:5000/etcd@$D3" | diff - "$tmp/image-refs.txt"
  grep -q "^$D1 ghcr.io/cozy/api:v1\$" "$tmp/images.txt"
  rm -rf "$tmp"
}

@test "a node that cannot be read leaves no image-refs.txt behind" {
  tmp=$(mktemp -d)
  _stub_talos "$tmp/talos"
  printf '%s ghcr.io/cozy/api:v1 %s 1MB x now\n' 192.168.123.11 "$D1" >"$MOCK_TALOS/192.168.123.11-cri"
  echo 192.168.123.13 >"$MOCK_TALOS/down"
  cp hack/collect-images.sh "$tmp/"
  # A partial list would audit only the nodes that answered.
  echo stale >"$tmp/image-refs.txt"
  (cd "$tmp" && PATH="$MOCK_TALOS/bin:$PATH" sh ./collect-images.sh) 2>"$tmp/err"
  [ ! -e "$tmp/image-refs.txt" ]
  grep -q 'image-refs.txt not written' "$tmp/err"
  grep -q "ghcr.io/cozy/api:v1" "$tmp/images.txt"
  rm -rf "$tmp"
}

@test "e2e-tag audits what the nodes pulled, fails only a pre-release, and tears down regardless" {
  wf=.github/workflows/e2e-tag.yaml
  names=$(yq -r '.jobs.e2e.steps[].name' "$wf")
  c=$(echo "$names" | grep -nx 'Collect images list' | cut -d: -f1)
  a=$(echo "$names" | grep -nx 'Audit pulled images are multi-arch' | cut -d: -f1)
  t=$(echo "$names" | grep -nx 'Tear down sandbox' | cut -d: -f1)
  [ -n "$c" ] && [ -n "$a" ] && [ -n "$t" ] && [ "$c" -lt "$a" ] && [ "$a" -lt "$t" ]
  step='.jobs.e2e.steps[] | select(.name == "Audit pulled images are multi-arch")'
  [ "$(yq -r "$step | .if" "$wf")" = 'always()' ]
  run=$(yq -r "$step | .run" "$wf")
  echo "$run" | grep -q 'hack/verify-multiarch.sh .*--refs-file'
  # A stable tag's run reports; a pre-release's fails.
  echo "$run" | grep -qF '*-*)'
  [ "$(yq -r '.jobs.e2e.steps[] | select(.name == "Tear down sandbox") | .if' "$wf")" = 'always()' ]
  yq -r '.jobs.e2e.steps[] | select(.name == "Upload image list") | .with.path' "$wf" | grep -q 'image-refs.txt'
  yq -r '.jobs.e2e.steps[] | select(.name == "Upload image list") | .with.path' "$wf" | grep -q 'multiarch-audit.txt'
}

@test "the e2e audit skips a tree that predates it and fails a pre-release tree that has it" {
  # The step runs in the checkout of the tag under test. An rc cut before the
  # audit existed has neither the script nor image-refs.txt, and failing its e2e
  # for that would block re-validating it from main.
  run=$(yq -r '.jobs.e2e.steps[] | select(.name == "Audit pulled images are multi-arch") | .run' .github/workflows/e2e-tag.yaml)
  tmp=$(mktemp -d)
  mkdir -p "$tmp/old" "$tmp/new/hack" "$tmp/new/_out" "$tmp/bin"
  printf '%s\n' "$run" >"$tmp/step.sh"
  # The step installs skopeo and jq when they are missing; neither case here
  # reaches a registry, so stubs keep it off apt-get.
  printf '#!/bin/sh\nexit 0\n' >"$tmp/bin/skopeo"
  printf '#!/bin/sh\nexit 0\n' >"$tmp/bin/jq"
  chmod +x "$tmp/bin/skopeo" "$tmp/bin/jq"
  # cd "/tmp/$SANDBOX_NAME" must land in the fixture, wherever mktemp put it.
  rc=0
  PATH="$tmp/bin:$PATH" SANDBOX_NAME="..$tmp/old" TAG=v9.9.9-rc.1 bash -e "$tmp/step.sh" >"$tmp/out" 2>&1 || rc=$?
  [ "$rc" -eq 0 ]
  grep -q '::warning.*predates the multi-arch audit' "$tmp/out"

  # A tree that has the audit but no image list: the collection failed.
  cp hack/verify-multiarch.sh "$tmp/new/hack/"
  cp hack/multiarch-allowlist "$tmp/new/hack/"
  mkdir -p "$tmp/new/hack/lib" && cp hack/lib/image-refs.sh "$tmp/new/hack/lib/"
  rc=0
  PATH="$tmp/bin:$PATH" SANDBOX_NAME="..$tmp/new" TAG=v9.9.9-rc.1 bash -e "$tmp/step.sh" >"$tmp/out" 2>&1 || rc=$?
  [ "$rc" -ne 0 ]
  grep -q "refs file '_out/image-refs.txt' does not exist" "$tmp/out"
  # A stable tag only warns, and says the audit did not run.
  PATH="$tmp/bin:$PATH" SANDBOX_NAME="..$tmp/new" TAG=v9.9.9 bash -e "$tmp/step.sh" >"$tmp/out" 2>&1
  grep -q '::warning title=multi-arch audit::the multi-arch audit did not run' "$tmp/out"

  # A single-arch image: a pre-release fails, a stable tag warns that the
  # images are not all indexes. Real jq reads the stub's manifest.
  mkdir -p "$tmp/bin-single"
  printf '#!/bin/sh\necho %s\n' "'{\"schemaVersion\":2,\"config\":{}}'" >"$tmp/bin-single/skopeo"
  chmod +x "$tmp/bin-single/skopeo"
  printf 'example.test/x@sha256:%s\n' "$(printf 'a%.0s' $(seq 1 64))" >"$tmp/new/_out/image-refs.txt"
  rc=0
  PATH="$tmp/bin-single:$PATH" SANDBOX_NAME="..$tmp/new" TAG=v9.9.9-rc.1 bash -e "$tmp/step.sh" >"$tmp/out" 2>&1 || rc=$?
  [ "$rc" -ne 0 ]
  grep -q 'example.test/x@sha256:a*: single-platform manifest' "$tmp/out"
  rc=0
  PATH="$tmp/bin-single:$PATH" SANDBOX_NAME="..$tmp/new" TAG=v9.9.9 bash -e "$tmp/step.sh" >"$tmp/out" 2>&1 || rc=$?
  [ "$rc" -eq 0 ]
  grep -q '::warning title=multi-arch audit::some images pulled by e2e are not amd64+arm64 indexes or could not be checked' "$tmp/out"
  rm -rf "$tmp"
}
