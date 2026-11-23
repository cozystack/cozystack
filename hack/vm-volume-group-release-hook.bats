#!/usr/bin/env bats
# Behavioural tests for the pre-delete hook that releases a VM's Velero volume
# group, packages/apps/vm-instance/templates/volume-group-release-hook.yaml.
#
# A vm-disk-<name> claim belongs to the vm-disk release and outlives the VM.
# Every VM leaves out a claim grouped under another VM's name, so a label left
# behind by a deleted VM keeps the disk out of any replacement VM's group for
# good. The script is extracted from the rendered chart and RUN against a fake
# kubectl holding claim labels on disk, because what matters is the labels it
# leaves and its exit code; the helm-unittest suite in
# packages/apps/vm-instance/tests/ matches the source text instead.
#
# Run from the repo root through `make bats-unit-tests`. Each @test builds and
# removes its fixture inline, and there is no EXIT trap: see
# docs/agents/e2e-testing.md.

load test_helper

VM_CHART=packages/apps/vm-instance

# render_release_hook <release> <out>
# The chart's vm.yaml looks up the default instance type and profile, which a
# render with no cluster cannot find, so the VM is given explicit resources.
render_release_hook() {
  release=$1 out=$2
  helm template "$release" "$VM_CHART" --namespace tenant-test \
    --set instanceType= --set instanceProfile= \
    --set resources.cpu=1 --set resources.sockets=1 --set resources.memory=1Gi \
    --set-json '_cluster={"scheduling":{}}' \
    --show-only templates/volume-group-release-hook.yaml 2>/dev/null \
    | yq eval 'select(.kind == "Job") | .spec.template.spec.containers[0].args[0]' - > "$out"
  [ -s "$out" ] || return 1
}

# write_fake_kubectl <dir>
# Each file in $CLAIMS is a claim; its content is the value of its
# velero.io/volume-group label, an empty file a claim without one. The fake
# serves the three calls the hook makes:
#   * a label-selector list of the claims carrying a value, or a failure when
#     $FAIL_LIST is set;
#   * a JSON test+remove patch, which fails with the API server's Invalid
#     answer when the value differs, as a JSON patch test does, and NotFound
#     when the claim is gone. $BEFORE_PATCH runs first, to model another writer
#     between the list and the patch; $FAIL_PATCH names a claim whose patch
#     fails for a reason unrelated to its label;
#   * a read of one claim's label.
write_fake_kubectl() {
  cat > "$1/kubectl" <<'KEOF'
#!/bin/sh
echo "$*" >> "$KLOG"
verb=$1 target=$2
case "$verb:$target" in
  get:persistentvolumeclaims)
    if [ -n "${FAIL_LIST:-}" ]; then
      echo "error: the server could not list persistentvolumeclaims" >&2
      exit 1
    fi
    want=${6#*=}
    for f in "$CLAIMS"/*; do
      [ -e "$f" ] || continue
      if [ "$(cat "$f")" = "$want" ]; then echo "persistentvolumeclaim/${f##*/}"; fi
    done
    exit 0
    ;;
esac
name=${target#persistentvolumeclaim/}
if [ "$verb" = patch ] && [ -n "${BEFORE_PATCH:-}" ]; then
  sh -c "$BEFORE_PATCH"
fi
if [ ! -e "$CLAIMS/$name" ]; then
  echo "Error from server (NotFound): persistentvolumeclaims \"$name\" not found" >&2
  exit 1
fi
case "$verb" in
  get)
    cat "$CLAIMS/$name"
    ;;
  patch)
    if [ "$name" = "${FAIL_PATCH:-}" ]; then
      echo "Error from server (InternalError): etcdserver: request timed out" >&2
      exit 1
    fi
    expected=$(printf '%s' "$7" | sed -n 's/.*"value":"\([^"]*\)".*/\1/p')
    if [ "$(cat "$CLAIMS/$name")" != "$expected" ]; then
      echo "The request is invalid: the server rejected our request due to an error in our request" >&2
      exit 1
    fi
    : > "$CLAIMS/$name"
    ;;
  *)
    echo "fake kubectl: unexpected call $*" >&2
    exit 64
    ;;
esac
KEOF
  chmod 0755 "$1/kubectl"
}

@test "a render that produces no script fails instead of passing as a no-op" {
  tmp=$(mktemp -d)
  if VM_CHART="$tmp/no-such-chart" render_release_hook vm-instance-old "$tmp/hook.sh"; then
    echo "FAIL: render_release_hook reported success with no chart to render"
    false
  fi
  rm -rf "$tmp"
}

@test "uninstall releases every claim the VM grouped, and a differently named VM can group them again" {
  # vm-disk-old-data was detached before the uninstall, with no upgrade since,
  # so no render-time list of the VM's disks would still name it.
  tmp=$(mktemp -d)
  write_fake_kubectl "$tmp"
  render_release_hook vm-instance-old "$tmp/hook.sh"
  export KLOG="$tmp/calls" CLAIMS="$tmp/claims"
  mkdir "$CLAIMS"
  printf 'vm-instance-old' > "$CLAIMS/vm-disk-old-system"
  printf 'vm-instance-old' > "$CLAIMS/vm-disk-old-data"
  printf 'vm-instance-old' > "$CLAIMS/persistent-state-for-vm-instance-old-abcde"
  printf 'vm-instance-other' > "$CLAIMS/vm-disk-other"
  : > "$CLAIMS/vm-disk-loose"

  rc=0
  PATH="$tmp:$PATH" sh -e "$tmp/hook.sh" > "$tmp/out" 2> "$tmp/err" || rc=$?
  if [ "$rc" -ne 0 ]; then
    echo "FAIL: hook exited $rc when every patch succeeded"
    cat "$tmp/out" "$tmp/err"
    false
  fi
  for claim in vm-disk-old-system vm-disk-old-data persistent-state-for-vm-instance-old-abcde; do
    if [ -s "$CLAIMS/$claim" ]; then
      echo "FAIL: $claim still carries the group $(cat "$CLAIMS/$claim")"
      false
    fi
  done
  if [ "$(cat "$CLAIMS/vm-disk-other")" != vm-instance-other ]; then
    echo "FAIL: another VM's group was removed"
    cat "$tmp/calls"
    false
  fi
  if grep -q 'vm-disk-other\|vm-disk-loose' "$tmp/calls"; then
    echo "FAIL: the hook touched a claim this VM never grouped"
    cat "$tmp/calls"
    false
  fi
  # The post-install hook of a replacement VM groups a claim whose label is
  # absent or empty and leaves out one grouped under another name; the
  # helm-unittest suite pins that side.
  rm -rf "$tmp"
}

@test "a claim another VM grouped between the list and the patch keeps that group" {
  tmp=$(mktemp -d)
  write_fake_kubectl "$tmp"
  render_release_hook vm-instance-old "$tmp/hook.sh"
  export KLOG="$tmp/calls" CLAIMS="$tmp/claims"
  mkdir "$CLAIMS"
  printf 'vm-instance-old' > "$CLAIMS/vm-disk-a"
  printf 'vm-instance-old' > "$CLAIMS/vm-disk-b"
  export BEFORE_PATCH="printf vm-instance-new > '$CLAIMS/vm-disk-a'"

  rc=0
  PATH="$tmp:$PATH" sh -e "$tmp/hook.sh" > "$tmp/out" 2> "$tmp/err" || rc=$?
  if [ "$rc" -ne 0 ]; then
    echo "FAIL: hook exited $rc for a claim that is no longer this VM's"
    cat "$tmp/out" "$tmp/err"
    false
  fi
  if [ "$(cat "$CLAIMS/vm-disk-a")" != vm-instance-new ]; then
    echo "FAIL: the newer group was removed"
    false
  fi
  rm -rf "$tmp"
}

@test "a claim deleted between the list and the patch does not fail the uninstall" {
  tmp=$(mktemp -d)
  write_fake_kubectl "$tmp"
  render_release_hook vm-instance-old "$tmp/hook.sh"
  export KLOG="$tmp/calls" CLAIMS="$tmp/claims"
  mkdir "$CLAIMS"
  printf 'vm-instance-old' > "$CLAIMS/vm-disk-a"
  printf 'vm-instance-old' > "$CLAIMS/vm-disk-b"
  export BEFORE_PATCH="rm -f '$CLAIMS/vm-disk-a'"

  rc=0
  PATH="$tmp:$PATH" sh -e "$tmp/hook.sh" > "$tmp/out" 2> "$tmp/err" || rc=$?
  if [ "$rc" -ne 0 ]; then
    echo "FAIL: hook exited $rc for a claim that no longer exists"
    cat "$tmp/out" "$tmp/err"
    false
  fi
  if [ -s "$CLAIMS/vm-disk-b" ]; then
    echo "FAIL: the claim after the deleted one kept its group"
    false
  fi
  rm -rf "$tmp"
}

@test "a claim that still carries the group after a failed patch fails the uninstall" {
  # Succeeding here would leave a label no release ever removes.
  tmp=$(mktemp -d)
  write_fake_kubectl "$tmp"
  render_release_hook vm-instance-old "$tmp/hook.sh"
  export KLOG="$tmp/calls" CLAIMS="$tmp/claims"
  mkdir "$CLAIMS"
  printf 'vm-instance-old' > "$CLAIMS/vm-disk-a"
  export FAIL_PATCH=vm-disk-a

  rc=0
  PATH="$tmp:$PATH" sh -e "$tmp/hook.sh" > "$tmp/out" 2> "$tmp/err" || rc=$?
  if [ "$rc" -eq 0 ]; then
    echo "FAIL: hook exited 0 with the group still on vm-disk-a"
    cat "$tmp/out" "$tmp/err"
    false
  fi
  if ! grep -q 'request timed out' "$tmp/err"; then
    echo "FAIL: the patch error was not reported"
    cat "$tmp/err"
    false
  fi
  rm -rf "$tmp"
}

@test "a failed list fails the uninstall instead of releasing nothing" {
  tmp=$(mktemp -d)
  write_fake_kubectl "$tmp"
  render_release_hook vm-instance-old "$tmp/hook.sh"
  export KLOG="$tmp/calls" CLAIMS="$tmp/claims"
  mkdir "$CLAIMS"
  printf 'vm-instance-old' > "$CLAIMS/vm-disk-a"
  export FAIL_LIST=1

  rc=0
  PATH="$tmp:$PATH" sh -e "$tmp/hook.sh" > "$tmp/out" 2> "$tmp/err" || rc=$?
  if [ "$rc" -eq 0 ]; then
    echo "FAIL: hook exited 0 although the claims could not be listed"
    cat "$tmp/out" "$tmp/err"
    false
  fi
  rm -rf "$tmp"
}
