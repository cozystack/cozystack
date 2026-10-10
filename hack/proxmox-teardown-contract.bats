#!/usr/bin/env bats

# The Proxmox lane runs on a self-hosted runner that has credentials of its own,
# and its Tear down deletes a tenant whose VMs live on a shared hypervisor. Two
# ways it can go wrong silently: deleting through a kubeconfig that falls back
# to the runner's own cluster, and exiting 0 while the tenant it should have
# removed is still live. These tests run the Stage and Tear down scripts out of
# the workflow against a stub kubectl, so they pin what the steps do rather than
# how they are spelled.

load test_helper

REPO_ROOT="$(cd "$(dirname "${BATS_TEST_FILENAME:-$0}")/.." && pwd)"
WORKFLOW="$REPO_ROOT/.github/workflows/e2e-proxmox.yaml"

setup() {
  strict_setup
  export GITHUB_WORKSPACE="$BATS_TEST_TMPDIR/ws" RUNNER_TEMP="$BATS_TEST_TMPDIR/tmp"
  mkdir -p "$GITHUB_WORKSPACE" "$RUNNER_TEMP" "$BATS_TEST_TMPDIR/bin"
  export COZY_PROXMOX_NS=tenant-e2eproxmox COZY_PVE_SSH_KEY="$BATS_TEST_TMPDIR/ssh-key"
  export KUBECTL_LOG="$BATS_TEST_TMPDIR/kubectl.log"
  : > "$KUBECTL_LOG"
  # `config view` prints $STUB_SERVER, or fails with $STUB_ERR on stderr when
  # it is empty, the two shapes kubectl answers the guard with. A call matching
  # $STUB_FAIL fails; every other call is only recorded.
  cat > "$BATS_TEST_TMPDIR/bin/kubectl" <<'EOF'
#!/usr/bin/env bash
printf '%s\n' "$*" >> "$KUBECTL_LOG"
if [ -n "${STUB_FAIL:-}" ] && [[ "$*" == *"$STUB_FAIL"* ]]; then exit 1; fi
case "$*" in
  *"config view"*)
    if [ -n "${STUB_SERVER:-}" ]; then printf '%s' "$STUB_SERVER"; else echo "$STUB_ERR" >&2; exit 1; fi ;;
esac
EOF
  chmod +x "$BATS_TEST_TMPDIR/bin/kubectl"
  export PATH="$BATS_TEST_TMPDIR/bin:$PATH"
  export STUB_SERVER="" STUB_ERR="error loading config file: did not find expected node content"
}

step_field() {
  yq -r ".jobs.e2e.steps[] | select(.name == \"$1\") | $2" "$WORKFLOW"
}

run_step() {
  local script
  script="$(step_field "$1" .run)"
  [ -n "$script" ]
  run bash -c "$script"
}

@test "Tear down takes Stage's outcome from the Stage step itself" {
  [ "$(step_field 'Stage credentials' .id)" = stage ]
  [ "$(step_field 'Tear down' .env.STAGE_OUTCOME)" = '${{ steps.stage.outcome }}' ]
}

@test "the suite reaches the stand through the staged kubeconfig" {
  [ "$(step_field 'Run the Proxmox suite' .env.KUBECONFIG)" = '${{ github.workspace }}/.kubeconfig' ]
}

@test "Tear down skips with exit 0 when Stage did not succeed" {
  for outcome in failure skipped cancelled; do
    STAGE_OUTCOME="$outcome" run_step "Tear down"
    [ "$status" -eq 0 ]
    [[ "$output" == *"::notice::Tear down skipped"* ]]
  done
  [ ! -s "$KUBECTL_LOG" ]
}

@test "Tear down fails and shows kubectl's error when Stage succeeded but the kubeconfig no longer resolves" {
  STAGE_OUTCOME=success run_step "Tear down"
  [ "$status" -eq 1 ]
  [[ "$output" == *"::error::"* ]]
  [[ "$output" == *"$STUB_ERR"* ]]
  if grep -q ' delete ' "$KUBECTL_LOG"; then echo "FAIL: deleted without a resolvable kubeconfig"; false; fi
}

@test "Tear down deletes through the staged kubeconfig when it resolves" {
  STUB_SERVER=https://192.0.2.10:6443 STAGE_OUTCOME=success run_step "Tear down"
  [ "$status" -eq 0 ]
  [ "$(grep -c ' delete ' "$KUBECTL_LOG")" -eq 3 ]
  if grep -v -- "--kubeconfig $GITHUB_WORKSPACE/.kubeconfig " "$KUBECTL_LOG"; then
    echo "FAIL: a kubectl call above does not name the staged kubeconfig"; false
  fi
}

@test "Tear down runs every delete after one fails, and then fails" {
  for target in " delete kubernetesnodes" " delete kubernetes.apps" " delete tenant"; do
    : > "$KUBECTL_LOG"
    STUB_FAIL="$target" STUB_SERVER=https://192.0.2.10:6443 STAGE_OUTCOME=success run_step "Tear down"
    [ "$status" -eq 1 ]
    [ "$(grep -c ' delete ' "$KUBECTL_LOG")" -eq 3 ]
  done
}

@test "Stage refuses a kubeconfig that does not resolve and shows kubectl's error" {
  export KUBECONFIG_B64="" PVE_SSH_KEY="" PVE_TOKEN_ID="" PVE_TOKEN_SECRET="" PVE_URL="" PVE_REGION=""
  run_step "Stage credentials"
  [ "$status" -eq 1 ]
  [[ "$output" == *"$STUB_ERR"* ]]
  if grep -qv 'config view' "$KUBECTL_LOG"; then echo "FAIL: Stage went past the guard"; false; fi
}

@test "Stage writes the stand's Secret through the staged kubeconfig when it resolves" {
  export KUBECONFIG_B64="" PVE_SSH_KEY="" PVE_TOKEN_ID="" PVE_TOKEN_SECRET="" PVE_URL="" PVE_REGION=""
  STUB_SERVER=https://192.0.2.10:6443 run_step "Stage credentials"
  [ "$status" -eq 0 ]
  [ "$(grep -c ' apply ' "$KUBECTL_LOG")" -eq 2 ]
  if grep -v -- "--kubeconfig $GITHUB_WORKSPACE/.kubeconfig " "$KUBECTL_LOG"; then
    echo "FAIL: a kubectl call above does not name the staged kubeconfig"; false
  fi
}
