#!/usr/bin/env bats
# -----------------------------------------------------------------------------
# packages/apps/kubernetes: the OIDC bootstrap Job must not carry the users[]
# list inside its container command.
#
# The Job's script reaches the container as one `sh -c` argument, and Linux caps
# one exec argument at 128 KiB (MAX_ARG_STRLEN). With a ClusterRoleBinding per
# user written into that script, about 240 users take it past the cap: the
# container cannot start, the post-install/post-upgrade hook fails, and the
# tenant cluster's HelmRelease stays unready. The bindings reach the Job through
# a mounted ConfigMap instead, so the script is the same size for any list.
#
# The last three tests execute the rendered script against a fake kubectl, since
# no assertion on the script's text says what the prune step does with the list:
# a condition forced either way passes every helm-unittest case.
#
# Requires: helm, yq (mikefarah v4+), jq, like hack/admin-kubeconfig-invariant.bats.
# -----------------------------------------------------------------------------

load test_helper

USERS=300

# render_users <dir> <count> renders the chart in System mode with <count>
# generated users and leaves one JSON document per object in <dir>/rendered.json.
render_users() {
  jq -n --argjson n "$2" '
    {oidc: {users: [range($n) | {email: "user\(.)@example.org", role: (if . % 7 == 0 then "admin" else "view" end)}]}}' \
    > "$1/users.yaml"
  helm template bindings packages/apps/kubernetes \
    --namespace tenant-test \
    --values packages/apps/kubernetes/tests/values/oidc-system.yaml \
    --values "$1/users.yaml" \
    > "$1/rendered.yaml"
  yq --output-format=json eval-all '.' "$1/rendered.yaml" > "$1/rendered.json"
}

# render_job <dir> <users JSON> renders the chart in CustomConfig mode and unpacks
# what the bootstrap Job runs and mounts into <dir>: script.sh is its `sh -c`
# script, bindings/ holds the two files of the mounted ConfigMap. -j keeps an
# empty value an empty file, as the kubelet writes it.
render_job() {
  jq -n --argjson users "$2" '
    {_cluster: {"oidc-enabled": "false"},
     oidc: {mode: "CustomConfig",
            customConfig: {config: "apiVersion: apiserver.config.k8s.io/v1beta1\nkind: AuthenticationConfiguration\njwt: []\n"},
            users: $users}}' > "$1/values.yaml"
  helm template bindings packages/apps/kubernetes \
    --namespace tenant-test \
    --values packages/apps/kubernetes/tests/values/oidc-system.yaml \
    --values "$1/values.yaml" \
    > "$1/rendered.yaml"
  yq --output-format=json eval-all '.' "$1/rendered.yaml" > "$1/rendered.json"
  jq -s -j '.[] | select(.kind == "Job" and .metadata.name == "bindings-oidc-bootstrap")
    | .spec.template.spec.containers[0].command[2]' "$1/rendered.json" > "$1/script.sh"
  mkdir -p "$1/bindings"
  jq -s -j '.[] | select(.kind == "ConfigMap" and .metadata.name == "bindings-oidc-bindings")
    | .data["crbs.yaml"]' "$1/rendered.json" > "$1/bindings/crbs.yaml"
  jq -s -j '.[] | select(.kind == "ConfigMap" and .metadata.name == "bindings-oidc-bindings")
    | .data["desired.txt"]' "$1/rendered.json" > "$1/bindings/desired.txt"
}

# fake_kubectl <dir> <names> puts a kubectl first on PATH that logs every call and
# answers the script's list of existing bindings with <names>.
fake_kubectl() {
  mkdir -p "$1/bin"
  printf '%s' "$2" > "$1/existing"
  cat > "$1/bin/kubectl" <<'KEOF'
#!/bin/sh
echo "$*" >> "$KLOG"
case "$*" in
  *"get clusterrolebindings"*) cat "$EXISTING" ;;
esac
KEOF
  chmod +x "$1/bin/kubectl"
}

# run_job <dir> runs the unpacked script as the container does and leaves its
# status in $rc and its output in <dir>/out. Run by bash, which has the pipefail
# the script sets and which is what the unit lane runs under; the container's own
# shell is not exercised.
run_job() {
  : > "$1/kubectl.log"
  echo kubeconfig > "$1/kubeconfig"
  rc=0
  env PATH="$1/bin:$PATH" KLOG="$1/kubectl.log" EXISTING="$1/existing" HOME="$1" \
    KUBECONFIG_TENANT="$1/kubeconfig" RELEASE=bindings RELEASE_NAMESPACE=tenant-test \
    OIDC_MODE=CustomConfig OIDC_BINDINGS_DIR="$1/bindings" \
    bash "$1/script.sh" > "$1/out" 2>&1 || rc=$?
}

@test "the OIDC bootstrap Job script stays under the exec argument cap and carries no user" {
  dir=$(mktemp -d)
  render_users "$dir" "$USERS"
  jq -s -r '.[] | select(.kind == "Job" and .metadata.name == "bindings-oidc-bootstrap")
    | .spec.template.spec.containers[0].command[2]' "$dir/rendered.json" > "$dir/script"
  size=$(wc -c < "$dir/script")
  [ "$size" -gt 0 ]
  [ "$size" -lt 131072 ]
  if grep -q -E 'user[0-9]+@example\.org' "$dir/script"; then
    echo "the Job script still carries users[] entries" >&2
    false
  fi
  rm -rf "$dir"
}

@test "every user keeps one ClusterRoleBinding with its old name and role in the bindings ConfigMap" {
  dir=$(mktemp -d)
  render_users "$dir" "$USERS"
  jq -s -r '.[] | select(.kind == "ConfigMap" and .metadata.name == "bindings-oidc-bindings")
    | .data["crbs.yaml"]' "$dir/rendered.json" > "$dir/crbs.yaml"
  jq -s -r '.[] | select(.kind == "ConfigMap" and .metadata.name == "bindings-oidc-bindings")
    | .data["desired.txt"]' "$dir/rendered.json" > "$dir/desired.txt"
  [ -s "$dir/crbs.yaml" ]
  yq --output-format=json eval-all '.' "$dir/crbs.yaml" | jq -s '.' > "$dir/crbs.json"

  [ "$(jq 'length' "$dir/crbs.json")" -eq "$USERS" ]
  [ "$(jq -r '[.[].subjects[0].name] | unique | length' "$dir/crbs.json")" -eq "$USERS" ]
  [ "$(jq -r '[.[] | select(.roleRef.name == "cluster-admin")] | length' "$dir/crbs.json")" -eq $(( (USERS + 6) / 7 )) ]

  # The name is what the prune step and every binding already in a tenant cluster
  # go by, so it must not move: cozystack-oidc- plus the first 16 hex digits of
  # sha256("<release>-<email>").
  for i in 0 1 6 7; do
    email="user$i@example.org"
    name="cozystack-oidc-$(printf '%s' "bindings-$email" | sha256sum | cut -c1-16)"
    [ "$(jq -r --arg e "$email" '.[] | select(.subjects[0].name == $e) | .metadata.name' "$dir/crbs.json")" = "$name" ]
    grep -q -x -F "$name" "$dir/desired.txt"
  done

  # The list the prune step compares against is exactly the applied names.
  [ "$(jq -r '.[].metadata.name' "$dir/crbs.json" | sort)" = "$(grep . "$dir/desired.txt" | sort)" ]
  rm -rf "$dir"
}

@test "the Job applies the mounted bindings and deletes only the bindings no user has" {
  dir=$(mktemp -d)
  render_job "$dir" '[{"email":"alice@example.org","role":"admin"},{"email":"bob@example.org","role":"view"}]'
  alice="cozystack-oidc-$(printf '%s' 'bindings-alice@example.org' | sha256sum | cut -c1-16)"
  bob="cozystack-oidc-$(printf '%s' 'bindings-bob@example.org' | sha256sum | cut -c1-16)"
  fake_kubectl "$dir" "$alice cozystack-oidc-000000000000dead $bob cozystack-oidc-000000000000beef"
  run_job "$dir"
  if [ "$rc" -ne 0 ]; then
    cat "$dir/out"
    false
  fi
  grep -q -F -- "apply --filename $dir/bindings/crbs.yaml" "$dir/kubectl.log"
  grep -q -F -- "delete clusterrolebinding cozystack-oidc-000000000000dead --ignore-not-found" "$dir/kubectl.log"
  grep -q -F -- "delete clusterrolebinding cozystack-oidc-000000000000beef --ignore-not-found" "$dir/kubectl.log"
  if grep -F -e "delete clusterrolebinding $alice" -e "delete clusterrolebinding $bob" "$dir/kubectl.log"; then
    echo "the Job deleted a binding a user still has" >&2
    false
  fi
  [ "$(grep -c 'delete clusterrolebinding' "$dir/kubectl.log")" -eq 2 ]
  rm -rf "$dir"
}

@test "a release with no users applies nothing and deletes every binding it left behind" {
  dir=$(mktemp -d)
  render_job "$dir" '[]'
  fake_kubectl "$dir" "cozystack-oidc-000000000000dead cozystack-oidc-000000000000beef"
  run_job "$dir"
  if [ "$rc" -ne 0 ]; then
    cat "$dir/out"
    false
  fi
  if grep -q -F -- ' apply ' "$dir/kubectl.log"; then
    echo "the Job ran kubectl apply with no bindings to apply" >&2
    false
  fi
  [ "$(grep -c 'delete clusterrolebinding' "$dir/kubectl.log")" -eq 2 ]
  rm -rf "$dir"
}

@test "the Job stops before deleting anything when the desired list is not mounted" {
  dir=$(mktemp -d)
  render_job "$dir" '[{"email":"alice@example.org","role":"admin"}]'
  fake_kubectl "$dir" "cozystack-oidc-000000000000dead cozystack-oidc-000000000000beef"
  rm "$dir/bindings/desired.txt"
  run_job "$dir"
  [ "$rc" -ne 0 ]
  if grep -q -E 'delete clusterrolebinding| apply ' "$dir/kubectl.log"; then
    cat "$dir/kubectl.log"
    false
  fi
  rm -rf "$dir"
}
