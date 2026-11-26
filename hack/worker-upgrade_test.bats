#!/usr/bin/env bats

load test_helper

@test "worker upgrade selection fails closed without changed paths" {
  tmp=$(mktemp -d)
  rc=0; output=$(bash hack/worker-upgrade-needed.sh "$tmp/missing" 2>&1) || rc=$?
  [ "$rc" -ne 0 ]
  rm -rf "$tmp"
}

@test "worker class changes require the real upgrade scenario" {
  tmp=$(mktemp -d)
  printf '%s\n' internal/kubernetesworker/config/resolve.go > "$tmp/paths"
  rc=0; output=$(bash hack/worker-upgrade-needed.sh "$tmp/paths" 2>&1) || rc=$?
  [ "$rc" -eq 0 ]
  [ "$output" = required ]
  rm -rf "$tmp"
}

@test "a change to the upgrade lane's own inputs selects it" {
  tmp=$(mktemp -d)
  for path in \
    hack/testdata/worker-upgrade-baseline.json \
    hack/testdata/worker-upgrade/upgrade-order.sh \
    hack/e2e-chainsaw-run.sh \
    hack/e2e-install-cozystack.bats \
    hack/e2e-chainsaw/_lib/talos-image-cache.sh \
    hack/e2e-talos-image-cache.yaml \
    hack/e2e-chainsaw/kubernetes-previous/chainsaw-test.yaml \
    hack/e2e-chainsaw/.chainsaw.yaml; do
    printf '%s\n' "$path" > "$tmp/paths"
    [ "$(bash hack/worker-upgrade-needed.sh "$tmp/paths")" = required ] || { echo "$path does not select the upgrade" >&2; false; }
    [ "$(bash hack/worker-upgrade-needed.sh --kairos "$tmp/paths")" = off ]
  done
  # The Kairos gate is read only in a required run; these keep the ordinary
  # run, where the Kairos pool runs under its own bound regardless.
  for path in hack/e2e-chainsaw/_lib/leak-guard.sh hack/e2e-chainsaw/_lib/leak-allowlist.txt hack/e2e-chainsaw/_lib/remediation-guard.sh hack/cozytest.sh hack/e2e-test-openapi.bats; do
    printf '%s\n' "$path" > "$tmp/paths"
    [ "$(bash hack/worker-upgrade-needed.sh "$tmp/paths")" = off ]
  done
  rm -rf "$tmp"
}

@test "installer chart changes select the upgrade without gating Kairos" {
  tmp=$(mktemp -d)
  for path in packages/core/installer/Chart.yaml packages/core/installer/values.yaml packages/core/installer/templates/cozystack-operator.yaml packages/core/installer/.helmignore; do
    printf '%s\n' "$path" > "$tmp/paths"
    [ "$(bash hack/worker-upgrade-needed.sh "$tmp/paths")" = required ] || { printf '%s was skipped\n' "$path"; false; }
    [ "$(bash hack/worker-upgrade-needed.sh --kairos "$tmp/paths")" = off ]
  done
  for path in packages/core/installer/README.md packages/core/installer-other/values.yaml; do
    printf '%s\n' "$path" > "$tmp/paths"
    [ "$(bash hack/worker-upgrade-needed.sh "$tmp/paths")" = off ]
  done
  rm -rf "$tmp"
}

# The parent chart owns the worker join path (the tenant DNS Service IP, the
# Cluster, the apiserver name a Kairos worker resolves), so any change to it can
# break workers the upgrade must keep, the Kairos pool included.
@test "a change to the parent kubernetes application selects the upgrade" {
  tmp=$(mktemp -d)
  for path in packages/apps/kubernetes/templates/cluster.yaml packages/apps/kubernetes/values.yaml packages/apps/kubernetes/files/versions.yaml packages/core/platform/sources/kubernetes-application.yaml; do
    printf '%s\n' "$path" > "$tmp/paths"
    [ "$(bash hack/worker-upgrade-needed.sh "$tmp/paths")" = required ] || { echo "$path does not select the upgrade" >&2; false; }
    [ "$(bash hack/worker-upgrade-needed.sh --kairos "$tmp/paths")" = required ] || { echo "$path does not gate on the Kairos operation" >&2; false; }
  done
  printf '%s\n' packages/apps/kubernetes-rd/Makefile > "$tmp/paths"
  [ "$(bash hack/worker-upgrade-needed.sh "$tmp/paths")" = off ]
  rm -rf "$tmp"
}

@test "a release promotion keeps the fresh install of its candidate" {
  step=$(yq '.jobs[].steps[] | select(.name == "Prepare immutable worker upgrade baseline")' .github/workflows/pull-requests.yaml)
  printf '%s\n' "$step" | grep --quiet "contains(github.event.pull_request.labels.\*.name, 'release')"
  printf '%s\n' "$step" | yq '.run' | grep --quiet 'mode=off'
  printf '%s\n' "$step" | yq '.run' | grep --quiet 'kairos=off'
}

@test "unrelated changes retain the ordinary fresh install" {
  tmp=$(mktemp -d)
  printf '%s\n' packages/apps/postgres/values.yaml > "$tmp/paths"
  rc=0; output=$(bash hack/worker-upgrade-needed.sh "$tmp/paths" 2>&1) || rc=$?
  [ "$rc" -eq 0 ]
  [ "$output" = off ]
  rm -rf "$tmp"
}

@test "each kind of Kairos change selects the upgrade and gates on the Kairos operation" {
  tmp=$(mktemp -d)
  for path in \
    packages/system/capi-providers-bootstrap/files/bootstrap-components-kairos.yaml \
    packages/system/capi-providers-bootstrap/files/components-kairos.gz \
    packages/system/capi-providers-bootstrap/files/metadata-kairos.yaml \
    packages/system/capi-providers-bootstrap/images/cluster-api-provider-kairos/Dockerfile \
    packages/system/capi-providers-bootstrap/patches/kairos/0001-select-the-provider-pods-by-provider-label.diff \
    packages/system/capi-providers-bootstrap/templates/providers.yaml \
    packages/system/capi-providers-bootstrap/templates/configmaps.yaml \
    packages/system/capi-providers-bootstrap/Makefile \
    packages/system/capi-providers-bootstrap/Chart.yaml \
    packages/system/capi-providers-bootstrap/.helmignore \
    packages/core/platform/sources/capi-provider-bootstrap-kubeadm.yaml \
    packages/system/kubernetes-nodes-rd/cozyrds/kubernetes-nodes.yaml \
    packages/system/kubernetes-rd/cozyrds/kubernetes.yaml \
    packages/core/platform/sources/kubernetes-nodes-application.yaml \
    packages/core/platform/sources/kubernetes-worker-classes.yaml \
    hack/e2e-chainsaw/_lib/run-kubernetes.sh \
    packages/apps/kubernetes-nodes/images/kairos-worker/Dockerfile \
    packages/apps/kubernetes-nodes/images/kairos-worker-v1.35.tag \
    packages/apps/kubernetes-nodes/templates/_kairosconfigtemplate.tpl \
    packages/apps/kubernetes-nodes/templates/kairos-reconcile-job.yaml \
    packages/apps/kubernetes-nodes/templates/nodegroup.yaml \
    packages/apps/kubernetes-nodes/templates/_workerclass.tpl \
    packages/apps/kubernetes-nodes/templates/_helpers.tpl \
    packages/apps/kubernetes-nodes/files/versions.yaml \
    packages/apps/kubernetes-nodes/templates/_talosconfigtemplate.tpl \
    packages/apps/kubernetes-nodes/templates/_resources.tpl \
    packages/apps/kubernetes-nodes/templates/_versions.tpl \
    packages/apps/kubernetes-nodes/templates/talos-reconcile-job.yaml \
    packages/apps/kubernetes-nodes/values.yaml \
    packages/apps/kubernetes-nodes/Chart.yaml \
    packages/apps/kubernetes-nodes/Makefile \
    packages/apps/kubernetes-nodes/values.schema.json \
    packages/apps/kubernetes-nodes/images/kubectl.tag \
    packages/apps/kubernetes-nodes/files/worker-defaults.json \
    packages/apps/kubernetes-nodes/.helmignore \
    internal/kubernetesworker/config/resolve.go \
    internal/kubernetesworker/admission/webhook.go \
    api/workers/v1alpha1/workerclass_types.go \
    cmd/kubernetes-worker-controller/main.go \
    packages/system/kubernetes-worker-classes/templates/workerclasses.yaml \
    packages/system/kubernetes-worker-classes-crds/definitions/cozystack.io_workerclasses.yaml \
    hack/e2e-chainsaw/_lib/run-kubernetes-kairos.sh \
    hack/e2e-chainsaw/_lib/run-kubernetes-upgrade.sh \
    hack/e2e-chainsaw/kubernetes-latest/chainsaw-test.yaml; do
    printf '%s\n' "$path" > "$tmp/paths"
    [ "$(bash hack/worker-upgrade-needed.sh "$tmp/paths")" = required ] || { echo "$path does not select the upgrade" >&2; false; }
    [ "$(bash hack/worker-upgrade-needed.sh --kairos "$tmp/paths")" = required ] || { echo "$path does not gate on the Kairos operation" >&2; false; }
  done
  rm -rf "$tmp"
}

# A platform source added under the worker prefix is unclassified until someone
# narrows it on purpose, so it gates on the Kairos operation by default.
@test "a new kubernetes platform source gates on the Kairos operation" {
  tmp=$(mktemp -d)
  printf '%s\n' packages/core/platform/sources/kubernetes-new-thing.yaml > "$tmp/paths"
  [ "$(bash hack/worker-upgrade-needed.sh "$tmp/paths")" = required ]
  [ "$(bash hack/worker-upgrade-needed.sh --kairos "$tmp/paths")" = required ]
  rm -rf "$tmp"
}

# Pairing a narrowed upgrade input with a path outside both lists must not turn
# the Kairos pool off: the unclassified path may change how the pool behaves.
@test "an unclassified path next to a narrowed upgrade input keeps the Kairos operation" {
  tmp=$(mktemp -d)
  for other in packages/system/kamaji/values.yaml cmd/cozystack-operator/main.go packages/system/capi-providers-core/values.yaml; do
    printf '%s\n' hack/e2e-chainsaw-run.sh "$other" > "$tmp/paths"
    [ "$(bash hack/worker-upgrade-needed.sh "$tmp/paths")" = required ]
    [ "$(bash hack/worker-upgrade-needed.sh --kairos "$tmp/paths")" = required ] || { echo "$other next to the runner leaves Kairos off" >&2; false; }
  done
  printf '%s\n' hack/e2e-chainsaw-run.sh packages/core/installer/values.yaml docs/README.md > "$tmp/paths"
  [ "$(bash hack/worker-upgrade-needed.sh --kairos "$tmp/paths")" = off ]
  rm -rf "$tmp"
}

@test "a worker change outside Kairos leaves the Kairos operation non-blocking" {
  tmp=$(mktemp -d)
  # One path per kind whose change cannot alter how a Kairos pool behaves.
  for path in \
    packages/core/installer/values.yaml \
    cmd/workerclass-restore/main.go \
    packages/core/platform/sources/kubernetes-worker-image.yaml \
    packages/apps/kubernetes-nodes/tests/workerclass_test.yaml \
    packages/apps/kubernetes/tests/admin_kubeconfig_wait_test.yaml \
    hack/e2e-chainsaw-run.sh; do
    printf '%s\n' "$path" > "$tmp/paths"
    [ "$(bash hack/worker-upgrade-needed.sh "$tmp/paths")" = required ] || { echo "$path does not select the upgrade" >&2; false; }
    [ "$(bash hack/worker-upgrade-needed.sh --kairos "$tmp/paths")" = off ] || { echo "$path gates on the Kairos operation" >&2; false; }
  done
  for path in \
    packages/system/capi-providers-bootstrap/files/bootstrap-components-talos.yaml \
    packages/system/capi-providers-bootstrap/files/components-talos.gz \
    packages/system/capi-providers-bootstrap/files/components.gz \
    packages/system/capi-providers-bootstrap/files/metadata-talos.yaml \
    packages/system/capi-providers-bootstrap/patches/0001-rename-default-sa-to-cabpt-manager.diff; do
    printf '%s\n' "$path" > "$tmp/paths"
    [ "$(bash hack/worker-upgrade-needed.sh "$tmp/paths")" = required ] || { echo "$path does not select the upgrade" >&2; false; }
    [ "$(bash hack/worker-upgrade-needed.sh --kairos "$tmp/paths")" = off ] || { echo "$path gates on the Kairos operation" >&2; false; }
  done
  rm -rf "$tmp"
}

@test "a worker file moved elsewhere still selects the upgrade" {
  step=$(yq '.jobs[].steps[] | select(.name == "Prepare immutable worker upgrade baseline") | .run' .github/workflows/pull-requests.yaml)
  printf '%s\n' "$step" | grep --quiet --fixed-strings 'git diff --name-only --no-renames "origin/${BASE_REF}...HEAD" > /tmp/worker-upgrade-changed.txt'
}

@test "the upgrade library addresses pools by the resource the API serves" {
  resource=$(jq --raw-output '.resource + "." + .group' internal/kubernetesworker/config/resource.json)
  uses=$(grep --only-matching --extended-regexp 'kubernetesnodes[a-z]*\.apps\.cozystack\.io' hack/e2e-chainsaw/_lib/run-kubernetes-upgrade.sh | sort -u)
  [ -n "$uses" ]
  [ "$uses" = "$resource" ]
  grep --quiet --fixed-strings "== \"$(jq --raw-output .resource internal/kubernetesworker/config/resource.json)\"" hack/e2e-chainsaw/_lib/run-kubernetes-upgrade.sh
}

@test "the Kairos operation's own bound outlasts the worst path its step comment adds up" {
  lib="$PWD/hack/e2e-chainsaw/_lib/run-kubernetes-upgrade.sh"
  worst=$(grep --only-matching --extended-regexp '= [0-9]+s, [0-9]+\.[0-9]m, under the [0-9]+m below' hack/e2e-chainsaw/kubernetes-latest/chainsaw-test.yaml | sed --regexp-extended 's/^= ([0-9]+)s,.*/\1/')
  [ -n "$worst" ]
  bound=$(bash -c '. "$1"; cozy_worker_upgrade_kairos_bound' _ "$lib")
  ceiling=$(bash -c '. "$1"; cozy_worker_upgrade_kairos_ceiling' _ "$lib")
  [ "$bound" -gt "$worst" ]
  [ "$(( bound + 10 ))" -lt "$ceiling" ]
}

# cozytest runs the e2e files under /bin/sh, which is dash in the sandbox, so
# the install step is executed here under dash rather than read as text.
@test "a fresh install passes the installer no values file, so its defaults stay defaults" {
  tmp=$(mktemp -d)
  { echo 'install() {'; sed -n '/^@test "Install Cozystack" {/,/--timeout 2m$/p' hack/e2e-install-cozystack.bats | sed '1d'; echo '}'; } > "$tmp/install.sh"
  mkdir -p "$tmp/_out/worker-upgrade-baseline/packages/core/installer"
  : > "$tmp/_out/worker-upgrade-baseline/packages/core/installer/Chart.yaml"
  for mode in off required; do
    (cd "$tmp" && COZY_WORKER_UPGRADE_MODE=$mode dash -c '. ./install.sh; helm() { printf "%s\n" "$@" > "helm-$COZY_WORKER_UPGRADE_MODE"; }; bash() { :; }; install')
  done
  if grep --quiet -- '--values' "$tmp/helm-off"; then echo "FAIL: a fresh install passed the installer a values file" >&2; false; fi
  grep --quiet --line-regexp packages/core/installer "$tmp/helm-off"
  [ "$(grep --after-context=1 --line-regexp -- '--values' "$tmp/helm-required" | tail -n 1)" = _out/worker-upgrade-baseline/install-values.json ]
  grep --quiet --line-regexp _out/worker-upgrade-baseline/packages/core/installer "$tmp/helm-required"
  rm -rf "$tmp"
}

@test "the lane hands the Kairos gate to the chainsaw run" {
  grep --quiet 'kairos=$(bash hack/worker-upgrade-needed.sh --kairos /tmp/worker-upgrade-changed.txt)' .github/workflows/pull-requests.yaml
  grep --quiet 'echo "COZY_WORKER_UPGRADE_KAIROS=$kairos" >> "$GITHUB_ENV"' .github/workflows/pull-requests.yaml
  sed -n '/^test-chainsaw:/,/^$/p' packages/core/testing/Makefile | grep --quiet -- '--env COZY_WORKER_UPGRADE_KAIROS="${COZY_WORKER_UPGRADE_KAIROS}"'
  [ "$(yq 'select(document_index == 0) | .spec.steps[0].try[] | select(.script.content | contains("run_kairos_pool_test")) | .script.content' hack/e2e-chainsaw/kubernetes-latest/chainsaw-test.yaml | grep -c cozy_worker_upgrade_kairos_operation)" -eq 1 ]
}

@test "worker documentation alone does not launch a lifecycle upgrade" {
  tmp=$(mktemp -d)
  printf '%s\n' packages/system/kubernetes-worker-classes/README.md > "$tmp/paths"
  rc=0; output=$(bash hack/worker-upgrade-needed.sh "$tmp/paths" 2>&1) || rc=$?
  [ "$rc" -eq 0 ]
  [ "$output" = off ]
  rm -rf "$tmp"
}

@test "required upgrade refuses an absent pinned baseline bundle" {
  tmp=$(mktemp -d)
  rc=0; output=$(bash hack/worker-upgrade-inputs.sh values "$tmp/missing" 2>&1) || rc=$?
  [ "$rc" -ne 0 ]
  rm -rf "$tmp"
}

@test "baseline values carry both immutable digests and exact pre-feature source" {
  rc=0; output=$(bash hack/worker-upgrade-inputs.sh values hack/testdata/worker-upgrade-baseline.json 2>&1) || rc=$?
  [ "$rc" -eq 0 ]
  [ "$(printf '%s' "$output" | jq --raw-output '.cozystackOperator.image')" = "ghcr.io/cozystack/cozystack/cozystack-operator:0.0.0-nightly.20261125.45531239791.1@sha256:28322133b9373f859af90413abe9617466dcb234b597730d3cc5aa02d32778ef" ]
  [ "$(printf '%s' "$output" | jq --raw-output '.cozystackOperator.platformSourceRef')" = "digest=sha256:f27825af445e550036b08ff820005d92214c753fe57989dbdc444b52164fbe61" ]
}

@test "the pinned baseline carries only the inputs the lane consumes" {
  [ "$(jq --compact-output 'keys' hack/testdata/worker-upgrade-baseline.json)" = '["operatorImage","packagesRef","packagesURL","source"]' ]
}

@test "a baseline built from a commit the candidate does not contain fails before install" {
  tmp=$(mktemp -d)
  root=$PWD
  repo="$tmp/repo"
  mkdir -p "$repo/packages/core/installer" "$repo/packages/apps/kubernetes" "$repo/packages/apps/kubernetes-nodes/files"
  printf 'name: installer\n' > "$repo/packages/core/installer/Chart.yaml"
  printf '{}\n' > "$repo/packages/apps/kubernetes/values.yaml"
  printf '{}\n' > "$repo/packages/apps/kubernetes-nodes/values.yaml"
  printf '{}\n' > "$repo/packages/apps/kubernetes-nodes/files/versions.yaml"
  mkdir -p "$repo/packages/apps/kubernetes/templates"
  : > "$repo/packages/apps/kubernetes/templates/etcd.yaml"
  git -C "$repo" init -q
  git -C "$repo" config commit.gpgsign false
  git -C "$repo" add .
  git -C "$repo" -c user.name=Test -c user.email=test@example.org commit -qm base
  base=$(git -C "$repo" rev-parse HEAD)
  git -C "$repo" -c user.name=Test -c user.email=test@example.org commit -q --allow-empty -m newer
  newer=$(git -C "$repo" rev-parse HEAD)
  git -C "$repo" checkout -q "$base"
  jq --arg source "$newer" '.source = $source' hack/testdata/worker-upgrade-baseline.json > "$tmp/bundle"
  rc=0; output=$(cd "$repo" && bash "$root/hack/worker-upgrade-inputs.sh" prepare "$tmp/bundle" _out/baseline 2>&1) || rc=$?
  [ "$rc" -ne 0 ]
  printf '%s\n' "$output" | grep -qF "$newer"
  [ ! -e "$repo/_out/baseline" ]
  jq --arg source "$base" '.source = $source' hack/testdata/worker-upgrade-baseline.json > "$tmp/bundle"
  (cd "$repo" && bash "$root/hack/worker-upgrade-inputs.sh" prepare "$tmp/bundle" _out/baseline)
  rm -rf "$tmp"
}

@test "a baseline that predates per-tenant etcd fails before install" {
  tmp=$(mktemp -d)
  root=$PWD
  repo="$tmp/repo"
  mkdir -p "$repo/packages/core/installer" "$repo/packages/apps/kubernetes/templates" "$repo/packages/apps/kubernetes-nodes/files"
  printf 'name: installer\n' > "$repo/packages/core/installer/Chart.yaml"
  printf '{}\n' > "$repo/packages/apps/kubernetes/values.yaml"
  printf '{}\n' > "$repo/packages/apps/kubernetes-nodes/values.yaml"
  printf '{}\n' > "$repo/packages/apps/kubernetes-nodes/files/versions.yaml"
  git -C "$repo" init -q
  git -C "$repo" config commit.gpgsign false
  git -C "$repo" add .
  git -C "$repo" -c user.name=Test -c user.email=test@example.org commit -qm shared-etcd
  jq --arg source "$(git -C "$repo" rev-parse HEAD)" '.source = $source' hack/testdata/worker-upgrade-baseline.json > "$tmp/bundle"
  rc=0; output=$(cd "$repo" && bash "$root/hack/worker-upgrade-inputs.sh" prepare "$tmp/bundle" _out/baseline 2>&1) || rc=$?
  [ "$rc" -ne 0 ]
  printf '%s\n' "$output" | grep -qF 'predates per-tenant etcd'
  [ ! -e "$repo/_out/baseline" ]
  : > "$repo/packages/apps/kubernetes/templates/etcd.yaml"
  git -C "$repo" add .
  git -C "$repo" -c user.name=Test -c user.email=test@example.org commit -qm own-etcd
  jq --arg source "$(git -C "$repo" rev-parse HEAD)" '.source = $source' hack/testdata/worker-upgrade-baseline.json > "$tmp/bundle"
  (cd "$repo" && bash "$root/hack/worker-upgrade-inputs.sh" prepare "$tmp/bundle" _out/baseline)
  rm -rf "$tmp"
}

@test "a floating baseline image fails before install" {
  tmp=$(mktemp -d)
  jq '.operatorImage = "registry.example.com/operator:latest"' hack/testdata/worker-upgrade-baseline.json > "$tmp/bundle"
  rc=0; output=$(bash hack/worker-upgrade-inputs.sh values "$tmp/bundle" 2>&1) || rc=$?
  [ "$rc" -ne 0 ]
  rm -rf "$tmp"
}

@test "the prepared baseline seeds the mirror with the Talos images of both baseline charts" {
  tmp=$(mktemp -d)
  root=$PWD
  repo="$tmp/repo"
  mkdir -p "$repo/packages/core/installer" "$repo/packages/apps/kubernetes" "$repo/packages/apps/kubernetes-nodes/files"
  printf 'name: installer\n' > "$repo/packages/core/installer/Chart.yaml"
  printf 'talos:\n  schematicID: parent\n  version: v1.13.5\n' > "$repo/packages/apps/kubernetes/values.yaml"
  printf 'talos:\n  schematicID: nodes\n  version: v1.13.6\n' > "$repo/packages/apps/kubernetes-nodes/values.yaml"
  printf '{}\n' > "$repo/packages/apps/kubernetes-nodes/files/versions.yaml"
  mkdir -p "$repo/packages/apps/kubernetes/templates"
  : > "$repo/packages/apps/kubernetes/templates/etcd.yaml"
  git -C "$repo" init -q
  git -C "$repo" config commit.gpgsign false
  git -C "$repo" add .
  git -C "$repo" -c user.name=Test -c user.email=test@example.org commit -qm fixture
  jq --arg source "$(git -C "$repo" rev-parse HEAD)" '.source = $source' hack/testdata/worker-upgrade-baseline.json > "$tmp/bundle"
  (cd "$repo" && bash "$root/hack/worker-upgrade-inputs.sh" prepare "$tmp/bundle" _out/worker-upgrade-baseline)
  printf '{"talos":{"schematicID":"candidate","version":"v1.14.0"}}\n' > "$repo/packages/apps/kubernetes-nodes/files/worker-defaults.json"
  rc=0; output=$(cd "$repo" && COZY_WORKER_UPGRADE_MODE=required sh -c '. "$1"; talos_image_cache_seeds' _ "$root/hack/e2e-chainsaw/_lib/talos-image-cache.sh") || rc=$?
  [ "$rc" -eq 0 ]
  [ "$output" = "candidate/v1.14.0 parent/v1.13.5 nodes/v1.13.6" ]
  rm -rf "$tmp"
}

@test "lifecycle suite precedes candidate OpenAPI and every remaining selected suite" {
  rc=0; output=$(bash hack/testdata/worker-upgrade/runner-order.sh 2>&1) || rc=$?
  [ "$rc" -eq 0 ]
  [ "$output" = "$(printf 'kubernetes-latest\nopenapi\npostgres\nbucket')" ]
}

@test "upgrade deadline starts at the actual UTC job start and reserves report time" {
  rc=0; output=$(bash hack/worker-upgrade-inputs.sh deadline 1970-01-01T00:00:00Z 2>&1) || rc=$?
  [ "$rc" -eq 0 ]
  [ "$output" = 16200 ]
  cap=$(yq '.jobs[] | select(.name == "E2E (in-tree)") | .["timeout-minutes"]' .github/workflows/pull-requests.yaml)
  [ "$output" = "$(( (cap - 35) * 60 ))" ]
  rc=0; output=$(bash hack/worker-upgrade-inputs.sh deadline unknown 2>&1) || rc=$?
  [ "$rc" -ne 0 ]
}

@test "required lifecycle refuses a missing deadline before creating resources" {
  rc=0; output=$(bash -c '. hack/e2e-chainsaw/_lib/run-kubernetes-upgrade.sh; cozy_worker_upgrade_budget 600' 2>&1) || rc=$?
  [ "$rc" -ne 0 ]
}

@test "required lifecycle refuses insufficient job headroom" {
  rc=0; output=$(env COZY_WORKER_UPGRADE_DEADLINE=1 bash -c '. hack/e2e-chainsaw/_lib/run-kubernetes-upgrade.sh; cozy_worker_upgrade_budget 600' 2>&1) || rc=$?
  [ "$rc" -ne 0 ]
}

@test "required lifecycle accepts an explicit bounded remaining budget" {
  deadline=$(( $(date +%s) + 720 ))
  rc=0; output=$(env COZY_WORKER_UPGRADE_DEADLINE="$deadline" bash -c '. hack/e2e-chainsaw/_lib/run-kubernetes-upgrade.sh; cozy_worker_upgrade_budget 600' 2>&1) || rc=$?
  [ "$rc" -eq 0 ]
}

@test "custom legacy fixture is created classless with explicit supported Talos settings" {
  rc=0; output=$(bash -c '
    . hack/e2e-chainsaw/_lib/run-kubernetes-upgrade.sh
    COZY_UPGRADE_CONTEXT=synthetic
    cozy_e2e_storage_class() { echo local; }
    talos_spec_block() { printf "  talos:\n    imageFactoryURL: https://factory.example.com\n"; }
    yq() { case "$*" in *schematicID*) echo abc123 ;; *keys*) echo v1.34 ;; *) echo v1.13.6 ;; esac; }
    kubectl() { cat; }
    cozy_worker_upgrade_create_custom example
  ' 2>&1) || rc=$?
  [ "$rc" -eq 0 ]
  actual=$(printf '%s' "$output" | yq --output-format=json)
  [ "$(printf '%s\n' "$actual" | jq --raw-output '.spec.version')" = v1.34 ]
  [ "$(printf '%s\n' "$actual" | jq '.spec | has("workerClassName")')" = false ]
  [ "$(printf '%s\n' "$actual" | jq --raw-output '.spec.talos.version')" = v1.13.6 ]
  [ "$(printf '%s\n' "$actual" | jq --raw-output '.spec.talos.imageFactoryURL')" = https://factory.example.com ]
  [ "$(printf '%s\n' "$actual" | jq '.spec.kernelModules | length')" -eq 0 ]
  [ "$(printf '%s\n' "$actual" | jq '.spec.minReplicas')" -eq 1 ]
}

@test "custom legacy fixture refuses a baseline without a Talos version or schematic" {
  tmp=$(mktemp -d)
  lib="$PWD/hack/e2e-chainsaw/_lib/run-kubernetes-upgrade.sh"
  values="$tmp/_out/worker-upgrade-baseline/packages/apps/kubernetes-nodes/values.yaml"
  mkdir -p "${values%/*}"
  for talos in 'talos: {schematicID: abc123}' 'talos: {version: v1.13.6}'; do
    printf '%s\n' "$talos" > "$values"
    rc=0; output=$(cd "$tmp" && bash -c '
      . "$1"
      COZY_UPGRADE_CONTEXT=synthetic
      cozy_e2e_storage_class() { echo local; }
      cozy_worker_upgrade_minor() { echo v1.34; }
      talos_spec_block() { :; }
      kubectl() { cat > applied.yaml; }
      cozy_worker_upgrade_create_custom example
    ' _ "$lib" 2>&1) || rc=$?
    [ "$rc" -ne 0 ]
    [ ! -e "$tmp/applied.yaml" ]
    case "$output" in *"baseline kubernetes-nodes values have no talos."*) ;; *) false ;; esac
  done
  rm -rf "$tmp"
}

@test "required lane runs the newest Kubernetes minor both baseline and candidate support" {
  tmp=$(mktemp -d)
  lib="$PWD/hack/e2e-chainsaw/_lib/run-kubernetes-upgrade.sh"
  mkdir -p "$tmp/_out/worker-upgrade-baseline/packages/apps/kubernetes-nodes/files" "$tmp/packages/apps/kubernetes-nodes/files"
  printf '"v1.33": "v1.33.1"\n"v1.35": "v1.35.1"\n"v1.34": "v1.34.1"\n' > "$tmp/_out/worker-upgrade-baseline/packages/apps/kubernetes-nodes/files/versions.yaml"
  printf '"v1.36": "v1.36.0"\n"v1.34": "v1.34.2"\n"v1.35": "v1.35.2"\n' > "$tmp/packages/apps/kubernetes-nodes/files/versions.yaml"
  rc=0; output=$(cd "$tmp" && bash -c '. "$1"; cozy_worker_upgrade_minor' _ "$lib" 2>&1) || rc=$?
  [ "$rc" -eq 0 ]
  [ "$output" = v1.35 ]
  printf '"v1.37": "v1.37.0"\n' > "$tmp/packages/apps/kubernetes-nodes/files/versions.yaml"
  rc=0; output=$(cd "$tmp" && bash -c '. "$1"; cozy_worker_upgrade_minor' _ "$lib" 2>&1) || rc=$?
  [ "$rc" -ne 0 ]
  rm -rf "$tmp"
}

@test "no-rollout snapshots preserve identity without exporting bootstrap material" {
  rc=0; output=$(bash hack/testdata/worker-upgrade/snapshot.sh 2>&1) || rc=$?
  [ "$rc" -eq 0 ]
}

@test "application GET default removal is observed separately from unchanged stored values and workers" {
  rc=0; output=$(bash hack/testdata/worker-upgrade/snapshot.sh get-defaults 2>&1) || rc=$?
  [ "$rc" -eq 0 ]
}

@test "no-rollout snapshots reject replacement, template version and provider changes" {
  for mutation in uid version provider; do
    rc=0; output=$(bash hack/testdata/worker-upgrade/snapshot.sh "$mutation" 2>&1) || rc=$?
    [ "$rc" -eq 1 ]
    case "$output" in *"snapshots complete"*) ;; *) printf '%s\n' "$output"; false ;; esac
  done
}

@test "a non-running worker fails even when the snapshot is called in an OR list" {
  rc=0; output=$(bash hack/testdata/worker-upgrade/snapshot.sh not-ready 2>&1) || rc=$?
  [ "$rc" -ne 0 ]
  case "$output" in *"snapshots complete"*) false ;; esac
}

@test "upgrade preserves installer overrides and captures only after candidate convergence" {
  rc=0; output=$(bash hack/testdata/worker-upgrade/upgrade-order.sh 2>&1) || rc=$?
  [ "$rc" -eq 0 ]
  [ "$output" = "$(printf 'upgrade\nreadiness\ncandidate-source\nreadiness\ncontrollers\nplatform-roots\nrelease-actions\ntenant\nsnapshot\ninterval\nsnapshot')" ]
}

@test "upgrade provider identity follows its PackageSource namespace" {
  rc=0; output=$(bash -c '. hack/e2e-chainsaw/_lib/run-kubernetes-upgrade.sh; cozy_worker_upgrade_controllers' 2>&1) || rc=$?
  [ "$rc" -eq 0 ]
  actual=$(yq --unwrapScalar '.spec.variants[].components[] | select(.name == "capi-providers-bootstrap") | .install.namespace' packages/core/platform/sources/capi-provider-bootstrap-kubeadm.yaml | sort --unique)
  case "$output" in *"$actual capi-kairos-bootstrap-controller-manager"*) ;; *) false ;; esac
}

@test "upgrade root validation rejects misplaced or duplicate provider identities" {
  for roots in \
    '[{"name":"kubernetes-worker-classes","namespace":"cozy-system","uid":"a"},{"name":"capi-kairos-bootstrap-controller-manager","namespace":"cozy-system","uid":"b"}]' \
    '[{"name":"kubernetes-worker-classes","namespace":"cozy-system","uid":"a"},{"name":"capi-kairos-bootstrap-controller-manager","namespace":"cozy-cluster-api","uid":"a"}]'; do
    rc=0; output=$(env ROOTS="$roots" bash -c '. hack/e2e-chainsaw/_lib/run-kubernetes-upgrade.sh; cozy_worker_upgrade_roots_valid <(echo "[]") <<< "$ROOTS"' 2>&1) || rc=$?
    [ "$rc" -ne 0 ]
  done
}

@test "candidate readiness uses the provider namespace" {
  tmp=$(mktemp -d)
  rc=0; output=$(bash hack/testdata/worker-upgrade/candidate-ready.sh "$tmp" 2>&1) || rc=$?
  [ "$rc" -eq 0 ]
  rm -rf "$tmp"
}

# The CAPI operator creates a provider Deployment after the release that
# applied its provider object is Ready, so the check waits for it.
@test "a provider Deployment that appears after convergence is waited for" {
  tmp=$(mktemp -d)
  rc=0; output=$(bash hack/testdata/worker-upgrade/candidate-ready.sh "$tmp" late 2>&1) || rc=$?
  [ "$rc" -eq 0 ] || { printf '%s\n' "$output"; false; }
  grep --quiet '^cozy-cluster-api deployment/capi-kairos-bootstrap-controller-manager$' "$tmp/events"
  [ "$(cat "$tmp/late-reads")" = 1 ]
  rm -rf "$tmp"
}

# The upgrade baseline is pinned, so every platform workload main gained since
# then is created by the upgrade, not only the ones this lane installs.
@test "upgrade platform roots are every platform workload the upgrade created" {
  rc=0; output=$(bash hack/testdata/worker-upgrade/platform-roots.sh 2>&1) || rc=$?
  [ "$rc" -eq 0 ]
  [ "$output" = "new-root provider-root worker-root" ]
}

@test "upgrade platform roots refuse a candidate without an intended controller" {
  rc=0; output=$(bash hack/testdata/worker-upgrade/platform-roots.sh missing-provider 2>&1) || rc=$?
  [ "$rc" -ne 0 ]
  case "$output" in *"capi-kairos-bootstrap-controller-manager"*) ;; *) false ;; esac
}

@test "candidate readiness refuses an absent or wrong-namespace provider explicitly" {
  tmp=$(mktemp -d)
  for mode in absent wrong-namespace; do
    mkdir "$tmp/$mode"
    rc=0; output=$(bash hack/testdata/worker-upgrade/candidate-ready.sh "$tmp/$mode" "$mode" 2>&1) || rc=$?
    [ "$rc" -ne 0 ]
    case "$output" in *"candidate deployment cozy-cluster-api/capi-kairos-bootstrap-controller-manager is missing"*) ;; *) false ;; esac
  done
  rm -rf "$tmp"
}

@test "candidate readiness waits for app generators to consume the candidate bundle" {
  tmp=$(mktemp -d)
  for mode in current old-source; do
    mkdir "$tmp/$mode"
    rc=0; output=$(bash hack/testdata/worker-upgrade/candidate-ready.sh "$tmp/$mode" ready "$mode" 2>&1) || rc=$?
    if [ "$mode" = current ]; then
      [ "$rc" -eq 0 ] || { printf '%s\n' "$output"; false; }
      grep --quiet '^app-artifacts$' "$tmp/$mode/events"
    else
      [ "$rc" -eq 1 ]
      case "$output" in *"candidate app artifacts did not converge"*) ;; *) false ;; esac
    fi
  done
  rm -rf "$tmp"
}

@test "candidate readiness refuses a provider that exists but is not ready" {
  tmp=$(mktemp -d)
  rc=0; output=$(bash hack/testdata/worker-upgrade/candidate-ready.sh "$tmp" not-ready 2>&1) || rc=$?
  [ "$rc" -ne 0 ]
  case "$output" in *"candidate deployment cozy-cluster-api/capi-kairos-bootstrap-controller-manager is not ready"*) ;; *) false ;; esac
  rm -rf "$tmp"
}

@test "unchanged app digests count only after their generators consumed the candidate bundle" {
  rc=0; output=$(bash hack/testdata/worker-upgrade/app-artifacts.sh 2>&1) || rc=$?
  [ "$rc" -eq 0 ] || { printf '%s\n' "$output"; false; }
  for mutation in old-source stale-generation stale-output stale-generator-uid wrong-source-name wrong-source-kind wrong-source-version wrong-source-namespace owner-only missing-generator; do
    rc=0; output=$(bash hack/testdata/worker-upgrade/app-artifacts.sh "$mutation" 2>&1) || rc=$?
    [ "$rc" -eq 1 ] || { printf '%s: %s\n' "$mutation" "$output"; false; }
  done
}

@test "an unchanged deployed release is valid once its candidate artifact has converged" {
  state='{"metadata":{"generation":7},"status":{"observedGeneration":7,"conditions":[{"type":"Ready","status":"True"}],"history":[{"status":"deployed","version":2}]}}'
  rc=0; output=$(printf '%s\n' "$state" | bash -c '. hack/e2e-chainsaw/_lib/run-kubernetes-upgrade.sh; cozy_worker_upgrade_release_current 2' 2>&1) || rc=$?
  [ "$rc" -eq 0 ]
}

@test "release convergence rejects a revision older than the baseline" {
  for version in 1 3; do
    state=$(jq --null-input --argjson version "$version" '{metadata:{generation:7},status:{observedGeneration:7,conditions:[{type:"Ready",status:"True"}],history:[{status:"deployed",version:$version}]}}')
    rc=0; output=$(printf '%s\n' "$state" | bash -c '. hack/e2e-chainsaw/_lib/run-kubernetes-upgrade.sh; cozy_worker_upgrade_release_current 2' 2>&1) || rc=$?
    if [ "$version" -eq 1 ]; then
      [ "$rc" -ne 0 ]
    else
      [ "$rc" -eq 0 ]
    fi
  done
}

@test "a platform release counts as converged only once it reconciled its source's current artifact" {
  rc=0; output=$(bash -c '. hack/e2e-chainsaw/_lib/run-kubernetes-upgrade.sh; cozy_worker_upgrade_unconverged' < hack/testdata/worker-upgrade/platform-releases.json 2>&1) || rc=$?
  [ "$rc" -eq 0 ]
  [ "$(printf '%s\n' "$output" | tr '\n' ' ')" = "cozy-x/lagging cozy-x/notready cozy-x/orphan cozy-x/stale " ]
}

@test "a platform producer with matching baseline artifact and release is still pending" {
  rc=0; output=$(bash hack/testdata/worker-upgrade/platform-producers.sh old-producer 2>&1) || rc=$?
  [ "$rc" -eq 1 ] || { printf '%s\n' "$output"; false; }
  case "$output" in *"platform artifact producers have not consumed the candidate bundle"*) ;; *) printf '%s\n' "$output"; false ;; esac
}

@test "current platform producers permit unchanged deployed artifacts but reject stale inventory" {
  for mode in current platform-source; do
    rc=0; output=$(bash hack/testdata/worker-upgrade/platform-producers.sh "$mode" 2>&1) || rc=$?
    [ "$rc" -eq 0 ] || { printf '%s\n' "$output"; false; }
  done
  for mode in stale-inventory wrong-source-name wrong-source-namespace stale-generator-uid; do
    rc=0; output=$(bash hack/testdata/worker-upgrade/platform-producers.sh "$mode" 2>&1) || rc=$?
    [ "$rc" -eq 1 ] || { printf '%s\n' "$output"; false; }
    case "$output" in *"platform artifact producers have not consumed the candidate bundle"*) ;; *) printf '%s\n' "$output"; false ;; esac
  done
}

@test "platform convergence uses native semver and non-semver revision tokens" {
  for entry in 'latest full' 'nightly full' '1.2.3-01 full' '18446744073709551616 full' '1 digest' 'v1.2 digest' '01.02.03 digest' 'v1.2.3_build.1 digest'; do
    tag=${entry% *}
    token_kind=${entry#* }
    revision="$tag@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
    token=$revision
    [ "$token_kind" != digest ] || token=${revision#*@}
    for mode in current stale alternate; do
      observed=$token
      [ "$mode" != stale ] || observed=sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb
      if [ "$mode" = alternate ]; then
        if [ "$token_kind" = full ]; then observed=${revision#*@}; else observed=$revision; fi
      fi
      state=$(jq --null-input --arg revision "$revision" --arg observed "$observed" '{items:[
        {kind:"ExternalArtifact",metadata:{namespace:"cozy-x",name:"chart"},status:{artifact:{revision:$revision}}},
        {kind:"HelmRelease",metadata:{namespace:"cozy-x",name:"release",generation:2},spec:{chartRef:{kind:"ExternalArtifact",name:"chart"}},status:{observedGeneration:2,conditions:[{type:"Ready",status:"True"}],lastAttemptedRevisionDigest:$observed}}]}')
      rc=0; output=$(printf '%s\n' "$state" | bash -c '. hack/e2e-chainsaw/_lib/run-kubernetes-upgrade.sh; cozy_worker_upgrade_unconverged' 2>&1) || rc=$?
      [ "$rc" -eq 0 ]
      if [ "$mode" = current ]; then
        [ -z "$output" ] || { printf '%s: %s\n' "$tag" "$output"; false; }
      else
        [ "$output" = cozy-x/release ] || { printf '%s %s: %s\n' "$tag" "$mode" "$output"; false; }
      fi
    done
  done
}

@test "a stalled platform release fails convergence at once instead of waiting out the deadline" {
  list='{"items":[
    {"kind":"HelmRelease","metadata":{"namespace":"cozy-x","name":"broken","generation":4},"spec":{},"status":{"conditions":[{"type":"Ready","status":"False"},{"type":"Stalled","status":"True","reason":"InvalidChartReference","message":"chart not found"}]}},
    {"kind":"HelmRelease","metadata":{"namespace":"cozy-x","name":"paused"},"spec":{"suspend":true},"status":{"conditions":[{"type":"Stalled","status":"True"}]}},
    {"kind":"HelmRelease","metadata":{"namespace":"cozy-x","name":"retrying"},"spec":{},"status":{"conditions":[{"type":"Ready","status":"False"},{"type":"Stalled","status":"False"}]}}]}'
  rc=0; output=$(LIST="$list" bash -c '. hack/e2e-chainsaw/_lib/run-kubernetes-upgrade.sh; kubectl() { printf "%s\n" "$LIST"; }; sleep() { echo slept; exit 3; }; cozy_worker_upgrade_platform_converged ctx' 2>&1) || rc=$?
  [ "$rc" -eq 1 ]
  [ "$output" = "HelmRelease cozy-x/broken stalled while applying generation 4: InvalidChartReference: chart not found" ]
}

# A failed action with a later one above it stays in the history, so the
# history guard would fail the run anyway; one still at the head may be a
# first install that a retry rewrites to superseded, so the wait goes on.
@test "only a settled failed action on a platform release that retries in place fails convergence at once" {
  tmp=$(mktemp -d)
  mkdir -p "$tmp/_out"
  printf '%s\n' '{}' > "$tmp/_out/worker-upgrade-platform-history-before.json"
  lib="$PWD/hack/e2e-chainsaw/_lib/run-kubernetes-upgrade.sh"
  pending='{"kind":"HelmRelease","metadata":{"namespace":"cozy-x","name":"pending"},"spec":{},"status":{"conditions":[{"type":"Ready","status":"False"}]}}'
  settled='{"items":[{"kind":"HelmRelease","metadata":{"namespace":"cozy-x","name":"upgraded"},"spec":{"install":{"strategy":{"name":"RetryOnFailure"}}},"status":{"conditions":[{"type":"Ready","status":"False"}],"history":[{"version":3,"status":"deployed","digest":"sha256:c"},{"version":2,"status":"failed","digest":"sha256:b"}]}},'"$pending"']}'
  head='{"items":[{"kind":"HelmRelease","metadata":{"namespace":"cozy-x","name":"installing"},"spec":{"install":{"strategy":{"name":"RetryOnFailure"}}},"status":{"conditions":[{"type":"Ready","status":"False"}],"history":[{"version":1,"status":"failed","digest":"sha256:a"}]}},'"$pending"']}'
  rc=0; output=$(cd "$tmp" && LIB="$lib" LIST="$settled" bash -c '. "$LIB"; kubectl() { printf "%s\n" "$LIST"; }; sleep() { echo slept; exit 3; }; cozy_worker_upgrade_platform_converged ctx' 2>&1) || rc=$?
  [ "$rc" -eq 1 ] || { printf 'settled: %s\n' "$output"; false; }
  [ "$output" = "FAIL HelmRelease cozy-x/upgraded (install strategy RetryOnFailure) recorded v2 failed after the upgrade began" ]
  rc=0; output=$(cd "$tmp" && LIB="$lib" LIST="$head" bash -c '. "$LIB"; kubectl() { printf "%s\n" "$LIST"; }; sleep() { echo slept; exit 3; }; cozy_worker_upgrade_platform_converged ctx' 2>&1) || rc=$?
  rm -rf "$tmp"
  [ "$rc" -eq 3 ] || { printf 'head: %s\n' "$output"; false; }
  [ "$output" = slept ]
}

@test "a stalled tenant release fails the Helm action wait at once instead of waiting out the deadline" {
  tmp=$(mktemp -d)
  lib="$PWD/hack/e2e-chainsaw/_lib/run-kubernetes-upgrade.sh"
  mkdir -p "$tmp/_out/cozyreport/snapshots/worker-upgrade"
  printf '%s\n' '{"name":"kubernetes-p","version":1}' > "$tmp/_out/worker-upgrade-release-revisions.jsonl"
  rc=0; output=$(cd "$tmp" && LIB="$lib" COZY_UPGRADE_CONTEXT=ctx bash -c '. "$LIB"; kubectl() { printf "%s\n" "{\"kind\":\"HelmRelease\",\"metadata\":{\"namespace\":\"tenant-test\",\"name\":\"kubernetes-p\",\"generation\":2},\"spec\":{},\"status\":{\"conditions\":[{\"type\":\"Stalled\",\"status\":\"True\",\"reason\":\"UpgradeFailed\",\"message\":\"retries exhausted\"}]}}"; }; cozy_guard_helmrelease() { :; }; sleep() { echo slept; exit 3; }; cozy_worker_upgrade_releases_ready p' 2>&1) || rc=$?
  rm -rf "$tmp"
  [ "$rc" -eq 1 ]
  [ "$output" = "HelmRelease tenant-test/kubernetes-p stalled while applying generation 2: UpgradeFailed: retries exhausted" ]
}

@test "candidate convergence failure prevents after evidence and new creates" {
  rc=0; output=$(bash hack/testdata/worker-upgrade/upgrade-order.sh fail 2>&1) || rc=$?
  [ "$rc" -ne 0 ]
  [ "$output" = "$(printf 'upgrade\nreadiness\ncandidate-source')" ]
}

@test "upgrade platform roots do not hide unrelated tenant leaks" {
  rc=0; output=$(bash hack/testdata/worker-upgrade/platform-leaks.sh 2>&1) || rc=$?
  [ "$rc" -eq 0 ]
  [ "$output" = tenant-test/tenant ]
}

@test "new default-class Node identity requires provider, tenant annotation and Ready" {
  node='{"metadata":{"annotations":{"cluster.x-k8s.io/cluster-namespace":"tenant-test"}},"spec":{"providerID":"kubevirt://worker"},"status":{"conditions":[{"type":"Ready","status":"True"}]}}'
  rc=0; output=$(printf '%s\n' "$node" | bash -c '. hack/e2e-chainsaw/_lib/run-kubernetes-upgrade.sh; cozy_worker_upgrade_node_identity' 2>&1) || rc=$?
  [ "$rc" -eq 0 ]
  for mutation in '.spec.providerID=""' '.metadata.annotations["cluster.x-k8s.io/cluster-namespace"]="other"' '.status.conditions[0].status="False"'; do
    invalid=$(printf '%s\n' "$node" | jq "$mutation")
    rc=0; output=$(printf '%s\n' "$invalid" | bash -c '. hack/e2e-chainsaw/_lib/run-kubernetes-upgrade.sh; cozy_worker_upgrade_node_identity' 2>&1) || rc=$?
    [ "$rc" -ne 0 ]
  done
}

@test "default Talos proof drains its new guest before Kairos can use the slot" {
  rc=0; output=$(bash hack/testdata/worker-upgrade/default-order.sh 2>&1) || rc=$?
  [ "$rc" -eq 0 ]
  [ "$output" = "$(printf 'connect\ncreate-one\nnode-identity\nscheduled-pod\nscale-zero-and-drain')" ]
}

# A release that failed an action and recovered under RetryOnFailure is current
# and Ready again; only the history guard sees that it failed.
@test "platform convergence judges actions the upgrade caused, and the default pool's scale-down runs the history guard" {
  tmp=$(mktemp -d)
  for history in failed:v4 uninstalled:v4 reset:v1 new-release:v4; do
    rc=0; output=$(HISTORY=${history%%:*} bash hack/testdata/worker-upgrade/platform-producers.sh current 2>&1) || rc=$?
    [ "$rc" -ne 0 ] || { printf '%s: %s\n' "$history" "$output"; false; }
    case "$output" in *"FAIL HelmRelease cozy-cluster-api/capi-providers-infraprovider (install strategy RetryOnFailure) recorded ${history#*:} "*" after the upgrade began"*) ;; *) printf '%s: %s\n' "$history" "$output"; false ;; esac
  done
  rc=0; output=$(HISTORY=missing-baseline bash hack/testdata/worker-upgrade/platform-producers.sh current 2>&1) || rc=$?
  [ "$rc" -ne 0 ] || { printf 'missing-baseline: %s\n' "$output"; false; }
  rc=0; output=$(HISTORY=note bash hack/testdata/worker-upgrade/platform-producers.sh current 2>&1) || rc=$?
  [ "$rc" -eq 0 ] || { printf '%s\n' "$output"; false; }
  case "$output" in *"NOTE HelmRelease cozy-cluster-api/capi-providers-infraprovider (install strategy <unset>) recorded v4 failed"*) ;; *) printf '%s\n' "$output"; false ;; esac
  for history in baseline-failed suspended-failed; do
    rc=0; output=$(HISTORY=$history bash hack/testdata/worker-upgrade/platform-producers.sh current 2>&1) || rc=$?
    [ "$rc" -eq 0 ] || { printf '%s: %s\n' "$history" "$output"; false; }
  done
  rc=0; output=$(GUARD_LOG="$tmp/drain" bash hack/testdata/worker-upgrade/default-drain.sh 2>&1) || rc=$?
  [ "$rc" -eq 0 ] || { printf '%s\n' "$output"; false; }
  # The pool is scaled down only once its release passed the history guard.
  [ "$(cat "$tmp/drain")" = "$(printf 'tenant-test/kubernetes-nodes-example-default-create required\nscale')" ]
  rc=0; output=$(GUARD_FAIL=1 GUARD_LOG="$tmp/failed" bash hack/testdata/worker-upgrade/default-drain.sh 2>&1) || rc=$?
  [ "$rc" -ne 0 ]
  if grep -qx scale "$tmp/failed"; then false; fi
  rm -rf "$tmp"
}

@test "the platform history baseline records the digest of each release's actions" {
  tmp=$(mktemp -d)
  lib="$PWD/hack/e2e-chainsaw/_lib/run-kubernetes-upgrade.sh"
  rc=0; output=$(cd "$tmp" && mkdir _out && bash -c '
    . "$1"
    kubectl() { echo "{\"items\":[{\"metadata\":{\"namespace\":\"cozy-a\",\"name\":\"one\"},\"status\":{\"history\":[{\"version\":4,\"digest\":\"sha256:b\"},{\"version\":3,\"digest\":\"sha256:a\"}]}},{\"metadata\":{\"namespace\":\"cozy-b\",\"name\":\"new\"},\"status\":{}}]}"; }
    cozy_worker_upgrade_platform_history_before synthetic && cat _out/worker-upgrade-platform-history-before.json
  ' _ "$lib" 2>&1) || rc=$?
  [ "$rc" -eq 0 ] || { printf '%s\n' "$output"; false; }
  [ "$(printf '%s' "$output" | jq --compact-output --sort-keys .)" = '{"cozy-a/one":["sha256:b","sha256:a"],"cozy-b/new":[]}' ]
  rm -rf "$tmp"
}

@test "the default-class pool is judged by the history guard once its release is ready" {
  for guard in pass fail; do
    rc=0; output=$(GUARD=$guard bash -c '
      . hack/e2e-chainsaw/_lib/run-kubernetes-upgrade.sh
      COZY_UPGRADE_CONTEXT=synthetic
      cozy_e2e_storage_class() { echo local; }
      cozy_worker_upgrade_minor() { echo v1.34; }
      kubectl() { case " $* " in *" apply "*) cat >/dev/null ;; *" get kubernetesnodeses"*) echo "{\"spec\":{\"workerClassName\":\"talos\"}}" ;; esac; }
      cozy_wait_kairos_pool_release() { echo "ready $1"; }
      cozy_guard_helmrelease() { echo "guard $1/$2 ${3:-}"; [ "$GUARD" = pass ]; }
      cozy_worker_upgrade_default_create example
    ' 2>&1) || rc=$?
    case "$output" in *"ready kubernetes-nodes-example-default-create"*"guard tenant-test/kubernetes-nodes-example-default-create required"*) ;; *) printf '%s\n' "$output"; false ;; esac
    if [ "$guard" = pass ]; then [ "$rc" -eq 0 ]; else [ "$rc" -ne 0 ]; fi
  done
}

@test "the default guest gets its whole drain budget after the scale-down settles" {
  rc=0; output=$(bash hack/testdata/worker-upgrade/default-drain.sh late 2>&1) || rc=$?
  [ "$rc" -eq 0 ]
}

@test "default guest slot is not released while a launcher is still running" {
  rc=0; output=$(bash hack/testdata/worker-upgrade/default-drain.sh 2>&1) || rc=$?
  [ "$rc" -eq 0 ]
  rc=0; output=$(bash hack/testdata/worker-upgrade/default-drain.sh busy 2>&1) || rc=$?
  [ "$rc" -ne 0 ]
  case "$output" in *"did not drain before Kairos: machines=0 vmis=0 vms=0 launchers=1 claims=0"*) ;; *) printf '%s\n' "$output"; false ;; esac
}

@test "required pre-suite read failures prevent every candidate check" {
  for failure in clock-fail baseline-fail; do
    rc=0; output=$(bash hack/testdata/worker-upgrade/runner-order.sh "$failure" 2>&1) || rc=$?
    [ "$rc" -ne 0 ]
    [ -z "$output" ]
  done
}

@test "required pre-suite reads cannot outlive the remaining suite budget" {
  for mode in clock-hang baseline-hang; do
    started=$(date +%s)
    rc=0; output=$(FIXTURE_SLEEP=18 RUNNER_HEADROOM=302 bash hack/testdata/worker-upgrade/runner-order.sh "$mode" 2>&1) || rc=$?
    elapsed=$(( $(date +%s) - started ))
    printf '%s exit=%s elapsed=%ss output=%s\n' "$mode" "$rc" "$elapsed" "$output"
    [ "$elapsed" -lt 12 ]
    [ "$rc" -eq 1 ]
    case "$output" in *kubernetes-latest*|*openapi*) false ;; esac
    case "$output" in read-started*) ;; *) false ;; esac
    case "$output" in *"synthetic: required upgrade job budget"*) ;; *) false ;; esac
  done
}

@test "a successful pre-suite read past the deadline admits no suite" {
  for mode in clock-late baseline-late; do
    rc=0; output=$(RUNNER_HEADROOM=302 bash hack/testdata/worker-upgrade/runner-order.sh "$mode" 2>&1) || rc=$?
    [ "$rc" -eq 1 ]
    case "$output" in *kubernetes-latest*|*openapi*) printf '%s: %s\n' "$mode" "$output"; false ;; esac
    case "$output" in read-past-deadline*) ;; *) printf '%s: the pre-suite read was not admitted: %s\n' "$mode" "$output"; false ;; esac
    case "$output" in *"synthetic: required upgrade job budget"*) ;; *) false ;; esac
  done
}

@test "required post-suite leak reads are bounded and reported unchecked" {
  started=$(date +%s)
  rc=0; output=$(FIXTURE_SLEEP=18 RUNNER_HEADROOM=60 RUNNER_LEAK_BUDGET=2 RUNNER_POST_REMAINING=2 bash hack/testdata/worker-upgrade/runner-order.sh post-hang 2>&1) || rc=$?
  elapsed=$(( $(date +%s) - started ))
  printf 'exit=%s elapsed=%ss output=%s\n' "$rc" "$elapsed" "$output"
  [ "$elapsed" -lt 12 ]
  [ "$rc" -eq 1 ]
  case "$output" in *"unchecked:"*"leak check budget exhausted"*) ;; *) false ;; esac
  case "$output" in *openapi*|*postgres*|*bucket*|*"leaked:"*) false ;; esac
}

@test "an exhausted post-suite budget does not start a disabled timeout" {
  rc=0; output=$(RUNNER_LEAK_BUDGET=2 RUNNER_HEADROOM=60 bash hack/testdata/worker-upgrade/runner-order.sh post-budget-gone 2>&1) || rc=$?
  [ "$rc" -eq 1 ]
  case "$output" in *"unchecked:"*"leak check budget exhausted"*) ;; *) printf '%s\n' "$output"; false ;; esac
  case "$output" in *post-check-was-invoked*|*openapi*|*postgres*|*bucket*|*"leaked:"*) false ;; esac
}

@test "bounded post-suite checks preserve clock baseline and platform root state" {
  for budget in 0 300; do
    rc=0; output=$(RUNNER_LEAK_BUDGET=$budget bash hack/testdata/worker-upgrade/runner-order.sh post-state 2>&1) || rc=$?
    [ "$rc" -eq 0 ] || { printf 'budget=%s: %s\n' "$budget" "$output"; false; }
    [ "$output" = "$(printf 'kubernetes-latest\nopenapi\npostgres\nbucket')" ]
  done
}

@test "required post-suite checks preserve inconclusive and proven-leak outcomes" {
  for mode in post-read-error post-proven-leak; do
    rc=0; output=$(bash hack/testdata/worker-upgrade/runner-order.sh "$mode" 2>&1) || rc=$?
    [ "$rc" -eq 1 ]
    if [ "$mode" = post-read-error ]; then
      case "$output" in *"unchecked:"*) ;; *) false ;; esac
      case "$output" in *"leaked:"*) false ;; esac
    else
      case "$output" in *"leaked:"*) ;; *) false ;; esac
      case "$output" in *"unchecked:"*) false ;; esac
    fi
    case "$output" in *openapi*|*postgres*|*bucket*) false ;; esac
  done
}

@test "required phase deadline stops nested suite timeout groups" {
  for mode in suite-nested suite-cozy-nested; do
    started=$(date +%s)
    rc=0; output=$(FIXTURE_SLEEP=30 RUNNER_HEADROOM=310 bash hack/testdata/worker-upgrade/runner-order.sh "$mode" 2>&1) || rc=$?
    elapsed=$(( $(date +%s) - started ))
    printf '%s exit=%s elapsed=%s output=%s\n' "$mode" "$rc" "$elapsed" "$output"
    [ "$elapsed" -lt 22 ]
    [ "$rc" -eq 1 ]
    case "$output" in *nested-complete*|*openapi*|*postgres*|*bucket*) false ;; esac
    case "$output" in kubernetes-latest*"ran out while"*) ;; *) false ;; esac
  done
}

@test "required phase deadline stops nested OpenAPI timeout groups" {
  started=$(date +%s)
  rc=0; output=$(FIXTURE_SLEEP=30 RUNNER_HEADROOM=310 bash hack/testdata/worker-upgrade/runner-order.sh openapi-nested 2>&1) || rc=$?
  elapsed=$(( $(date +%s) - started ))
  printf 'exit=%s elapsed=%s output=%s\n' "$rc" "$elapsed" "$output"
  [ "$elapsed" -lt 22 ]
  case "$output" in *openapi*"ran out during the candidate OpenAPI checks"*) ;; *) false ;; esac
  [ "$rc" -eq 1 ]
  case "$output" in *nested-complete*|*postgres*|*bucket*) false ;; esac
  case "$output" in *"synthetic: required upgrade"*"budget"*) ;; *) false ;; esac
}

@test "successful phase stops background timeout work before admitting later phases" {
  for mode in suite-background suite-raw-background; do
    started=$(date +%s)
    rc=0; output=$(FIXTURE_SLEEP=18 RUNNER_HEADROOM=340 bash hack/testdata/worker-upgrade/runner-order.sh "$mode" 2>&1) || rc=$?
    elapsed=$(( $(date +%s) - started ))
    printf '%s exit=%s elapsed=%s output=%s\n' "$mode" "$rc" "$elapsed" "$output"
    [ "$elapsed" -lt 12 ]
    [ "$rc" -eq 0 ]
    case "$output" in *nested-complete*) false ;; esac
    [ "$output" = "$(printf 'kubernetes-latest\nopenapi\npostgres\nbucket')" ]
  done
}

@test "nested phase work within budget still admits later suites" {
  for mode in suite-nested suite-cozy-nested openapi-nested; do
    rc=0; output=$(RUNNER_HEADROOM=340 bash hack/testdata/worker-upgrade/runner-order.sh "$mode" 2>&1) || rc=$?
    [ "$rc" -eq 0 ] || { printf '%s: %s\n' "$mode" "$output"; false; }
    case "$output" in *nested-complete*) ;; *) false ;; esac
    case "$output" in *"kubernetes-latest"*"openapi"*"postgres"*"bucket"*) ;; *) false ;; esac
  done
}

@test "candidate OpenAPI checks that outlast the job deadline end as a failure, not a cancelled job" {
  started=$(date +%s)
  rc=0; output=$(RUNNER_HEADROOM=305 bash hack/testdata/worker-upgrade/runner-order.sh openapi-hang 2>&1) || rc=$?
  [ "$rc" -ne 0 ]
  [ "$(( $(date +%s) - started ))" -lt 40 ]
  [ "$output" = "$(printf 'kubernetes-latest\nopenapi\nsynthetic: required upgrade job budget ran out during the candidate OpenAPI checks\nsynthetic: suite not run: required upgrade job budget ran out during the candidate OpenAPI checks\nsynthetic: suite not run: required upgrade job budget ran out during the candidate OpenAPI checks')" ]
}

@test "candidate OpenAPI checks that ignore the deadline signal are killed and reported as out of budget" {
  rc=0; output=$(RUNNER_HEADROOM=303 bash hack/testdata/worker-upgrade/runner-order.sh openapi-stubborn 2>&1) || rc=$?
  [ "$rc" -ne 0 ]
  [ "$output" = "$(printf 'kubernetes-latest\nopenapi\nsynthetic: required upgrade job budget ran out during the candidate OpenAPI checks\nsynthetic: suite not run: required upgrade job budget ran out during the candidate OpenAPI checks\nsynthetic: suite not run: required upgrade job budget ran out during the candidate OpenAPI checks')" ]
}

@test "a passing upgrade suite that recorded no platform roots fails as a platform baseline" {
  rc=0; output=$(bash hack/testdata/worker-upgrade/runner-order.sh pass-noroots 2>&1) || rc=$?
  [ "$rc" -ne 0 ]
  [ "$output" = "$(printf 'kubernetes-latest\nsynthetic: the upgrade did not record its platform roots\nsynthetic: suite not run: the required worker upgrade failed\nsynthetic: suite not run: the required worker upgrade failed')" ]
}

# Once the pinned baseline already runs the worker controllers, the upgrade
# creates neither, and an empty list of new roots is a valid record.
@test "a passing upgrade suite whose baseline already ran the controllers records valid empty roots" {
  rc=0; output=$(bash hack/testdata/worker-upgrade/runner-order.sh pass-emptyroots 2>&1) || rc=$?
  [ "$rc" -eq 0 ]
  [ "$output" = "$(printf 'kubernetes-latest\nopenapi\npostgres\nbucket')" ]
}

@test "upgrade platform roots do not require controllers the baseline already ran" {
  rc=0; output=$(bash hack/testdata/worker-upgrade/platform-roots.sh already-installed 2>&1) || rc=$?
  [ "$rc" -eq 0 ]
  [ "$output" = "new-root" ]
}

@test "upgrade root validation fails closed without the snapshot taken before the upgrade" {
  rc=0; output=$(bash -c '. hack/e2e-chainsaw/_lib/run-kubernetes-upgrade.sh; cozy_worker_upgrade_roots_valid /nonexistent/before.json <<< "[]"' 2>&1) || rc=$?
  [ "$rc" -ne 0 ]
}

# Without the roots the leak guard cannot tell the upgrade's own workloads from
# leaks; it is not run, and the suite is marked unchecked, which fails the run
# just as a leak would.
@test "a failed upgrade suite without roots is marked unchecked instead of leak-checked" {
  rc=0; output=$(bash hack/testdata/worker-upgrade/runner-order.sh fail-noroots 2>&1) || rc=$?
  [ "$rc" -ne 0 ]
  [ "$output" = "$(printf 'kubernetes-latest\nsynthetic: suite not run: the required worker upgrade failed\nsynthetic: suite not run: the required worker upgrade failed\nunchecked: the required upgrade failed before it recorded its platform roots, so the leak guard cannot tell its workloads from leaks')" ]
}

# A suite that failed before the snapshot the upgrade takes first created no
# platform workloads, so it is leak-checked like any other suite.
@test "a suite that fails before the upgrade starts is leak-checked, not marked unchecked" {
  rc=0; output=$(bash hack/testdata/worker-upgrade/runner-order.sh fail-nobefore 2>&1) || rc=$?
  [ "$rc" -ne 0 ]
  [ "$output" = "$(printf 'kubernetes-latest\nsynthetic: suite not run: the required worker upgrade failed\nsynthetic: suite not run: the required worker upgrade failed\nleak guard ran on kubernetes-latest')" ]
}

# The suites a failed upgrade keeps from running carry that reason, not the
# merge's "no report", which would say chainsaw ran them and wrote nothing.
@test "suites a failed upgrade stops are reported as not run, with the reason" {
  rc=0; output=$(bash hack/testdata/worker-upgrade/runner-order.sh fail-notrun 2>&1) || rc=$?
  [ "$rc" -ne 0 ]
  [ "$output" = "$(printf 'kubernetes-latest\npostgres: suite not run: the required worker upgrade failed\nbucket: suite not run: the required worker upgrade failed\nno report: kubernetes-latest')" ]
}

@test "a failed upgrade suite prevents candidate checks on the baseline" {
  rc=0; output=$(bash hack/testdata/worker-upgrade/runner-order.sh fail 2>&1) || rc=$?
  [ "$rc" -ne 0 ]
  [ "$output" = kubernetes-latest ]
}

@test "required reserves sum the required operations, the teardown, the cleanup and every other Test of the file, and leave the Kairos operation out" {
  tmp=$(mktemp -d)
  lib="$PWD/hack/e2e-chainsaw/_lib/run-kubernetes-upgrade.sh"
  printf '%s\n' 'apiVersion: chainsaw.kyverno.io/v1alpha1' 'kind: Test' 'spec:' '  steps:' '  - try:' \
    '    - script: {timeout: 10m, content: "cozy_worker_upgrade_preflight"}' \
    '    - script: {timeout: 20m, content: "apply"}' \
    '    - script: {timeout: 5m, content: "cozy_worker_upgrade_default_test x"}' \
    '    - script: {timeout: 7m, content: "run_kairos_pool_test x"}' \
    '    finally:' '    - script: {timeout: 3m, content: "cozy_cleanup x"}' \
    '---' 'apiVersion: chainsaw.kyverno.io/v1alpha1' 'kind: Test' 'spec:' '  steps:' '  - try:' \
    '    - script: {timeout: 99m, content: "other"}' > "$tmp/suite.yaml"
  printf '%s\n' 'apiVersion: chainsaw.kyverno.io/v1alpha2' 'kind: Configuration' 'spec:' '  timeouts:' '    cleanup: 2m' > "$tmp/config.yaml"
  rc=0; output=$(COZY_WORKER_UPGRADE_SUITE="$tmp/suite.yaml" COZY_CHAINSAW_CONFIG="$tmp/config.yaml" bash -c '. "$1"; cozy_worker_upgrade_reserve_from cozy_worker_upgrade_preflight; cozy_worker_upgrade_reserve_from cozy_worker_upgrade_default_test; cozy_worker_upgrade_kairos_ceiling; cozy_worker_upgrade_kairos_reserve' _ "$lib" 2>&1) || rc=$?
  [ "$rc" -eq 0 ]
  [ "$output" = "$(printf '8460\n6660\n420\n6780')" ]
  rc=0; output=$(COZY_WORKER_UPGRADE_SUITE="$tmp/suite.yaml" COZY_CHAINSAW_CONFIG="$tmp/config.yaml" bash -c '. "$1"; cozy_worker_upgrade_reserve_from cozy_worker_upgrade_missing' _ "$lib" 2>&1) || rc=$?
  [ "$rc" -ne 0 ]
  sed 's/content: "apply"/content: "run_kairos_pool_test y"/' "$tmp/suite.yaml" > "$tmp/two.yaml"
  rc=0; output=$(COZY_WORKER_UPGRADE_SUITE="$tmp/two.yaml" COZY_CHAINSAW_CONFIG="$tmp/config.yaml" bash -c '. "$1"; cozy_worker_upgrade_kairos_reserve' _ "$lib" 2>&1) || rc=$?
  [ "$rc" -ne 0 ]
  sed 's/{timeout: 99m, content: "other"}/{content: "other"}/' "$tmp/suite.yaml" > "$tmp/untimed.yaml"
  rc=0; output=$(COZY_WORKER_UPGRADE_SUITE="$tmp/untimed.yaml" COZY_CHAINSAW_CONFIG="$tmp/config.yaml" bash -c '. "$1"; cozy_worker_upgrade_reserve_from cozy_worker_upgrade_preflight' _ "$lib" 2>&1) || rc=$?
  [ "$rc" -ne 0 ]
  sed 's/timeout: 20m/timeout: 1h20m/' "$tmp/suite.yaml" > "$tmp/odd.yaml"
  rc=0; output=$(COZY_WORKER_UPGRADE_SUITE="$tmp/odd.yaml" COZY_CHAINSAW_CONFIG="$tmp/config.yaml" bash -c '. "$1"; cozy_worker_upgrade_reserve_from cozy_worker_upgrade_preflight' _ "$lib" 2>&1) || rc=$?
  [ "$rc" -ne 0 ]
  rm -rf "$tmp"
}

@test "the required reserve plus the Kairos ceiling covers every operation of the upgrade test" {
  lib="$PWD/hack/e2e-chainsaw/_lib/run-kubernetes-upgrade.sh"
  rc=0; output=$(bash -c '. "$1"; echo $(( $(cozy_worker_upgrade_reserve_from cozy_worker_upgrade_preflight) + $(cozy_worker_upgrade_kairos_ceiling) ))' _ "$lib" 2>&1) || rc=$?
  [ "$rc" -eq 0 ]
  # A script is priced by its timeout, an assert or apply by its own or the
  # configured default; the suite uses whole minutes throughout.
  apply=$(yq '.spec.timeouts.apply' hack/e2e-chainsaw/.chainsaw.yaml)
  assert=$(yq '.spec.timeouts.assert' hack/e2e-chainsaw/.chainsaw.yaml)
  total=$(yq --output-format=json 'select(.kind == "Test")' hack/e2e-chainsaw/kubernetes-latest/chainsaw-test.yaml \
    | jq --slurp --arg apply "$apply" --arg assert "$assert" '[.[].spec.steps[] | (.try[]?, .finally[]?) | if .script then .script.timeout elif .assert then (.assert.timeout // $assert) else (.apply.timeout // $apply) end] | map(sub("m$"; "") | tonumber * 60) | add')
  tests=$(yq --output-format=json 'select(.kind == "Test")' hack/e2e-chainsaw/kubernetes-latest/chainsaw-test.yaml | jq --slurp 'length')
  [ "$tests" -ge 2 ]
  cleanup=$(yq '.spec.timeouts.cleanup' hack/e2e-chainsaw/.chainsaw.yaml | sed 's/m$//')
  [ "$output" = "$(( total + tests * cleanup * 60 ))" ]
  [ "$(yq 'select(document_index == 0) | .spec.steps[0].try[0].script.content' hack/e2e-chainsaw/kubernetes-latest/chainsaw-test.yaml | grep -c cozy_worker_upgrade_preflight)" -eq 1 ]
}

# Outside a Kairos change the operation does not start at all: the cluster it
# joins goes with kubernetes-latest, so it cannot wait for the candidate checks
# and the remaining suites, and starting it first would spend their budget.
@test "a required upgrade of a change outside Kairos does not start the Kairos operation, whatever its headroom" {
  tmp=$(mktemp -d)
  lib="$PWD/hack/e2e-chainsaw/_lib/run-kubernetes-upgrade.sh"
  record="$tmp/_out/cozyreport/snapshots/worker-upgrade/kairos-operation.txt"
  for deadline in $(( $(date +%s) + 60 )) $(( $(date +%s) + 3600 )) ''; do
    rm -f "$record"
    rc=0; output=$(cd "$tmp" && COZY_WORKER_UPGRADE_KAIROS=off COZY_WORKER_UPGRADE_DEADLINE=$deadline bash -c '. "$1"; cozy_worker_upgrade_kairos_reserve() { echo 600; }; cozy_worker_upgrade_kairos_operation bash -c "echo ran"' _ "$lib" 2>&1) || rc=$?
    [ "$rc" -eq 0 ]
    [ "$output" = "Kairos pool test not run in the required upgrade run: not a Kairos change" ]
    [ "$(cat "$record")" = "skipped: not a Kairos change" ]
  done
  rc=0; output=$(cd "$tmp" && COZY_WORKER_UPGRADE_DEADLINE=$(( $(date +%s) + 3600 )) bash -c '. "$1"; cozy_worker_upgrade_kairos_reserve() { echo 600; }; cozy_worker_upgrade_kairos_operation bash -c "exit 3"' _ "$lib" 2>&1) || rc=$?
  [ "$rc" -eq 0 ]
  [ "$(cat "$record")" = "skipped: not a Kairos change" ]
  rm -rf "$tmp"
}

@test "a Kairos change gates its lane on the Kairos operation" {
  tmp=$(mktemp -d)
  lib="$PWD/hack/e2e-chainsaw/_lib/run-kubernetes-upgrade.sh"
  record="$tmp/_out/cozyreport/snapshots/worker-upgrade/kairos-operation.txt"
  rc=0; output=$(cd "$tmp" && COZY_WORKER_UPGRADE_KAIROS=required COZY_WORKER_UPGRADE_DEADLINE=$(( $(date +%s) + 3600 )) bash -c '. "$1"; cozy_worker_upgrade_kairos_reserve() { echo 600; }; cozy_worker_upgrade_kairos_ceiling() { echo 120; }; cozy_worker_upgrade_kairos_operation bash -c "exit 3"' _ "$lib" 2>&1) || rc=$?
  [ "$rc" -eq 3 ]
  case "$output" in *"::error title=kairos-upgrade::"*"exit 3"*) ;; *) false ;; esac
  grep --quiet '^failed 3' "$record"
  rc=0; output=$(cd "$tmp" && COZY_WORKER_UPGRADE_KAIROS=required COZY_WORKER_UPGRADE_DEADLINE=$(( $(date +%s) + 3600 )) bash -c '. "$1"; cozy_worker_upgrade_kairos_reserve() { echo 600; }; cozy_worker_upgrade_kairos_ceiling() { echo 120; }; cozy_worker_upgrade_kairos_operation bash -c "echo ran"' _ "$lib" 2>&1) || rc=$?
  [ "$rc" -eq 0 ]
  case "$output" in *ran*) ;; *) false ;; esac
  grep --quiet '^passed' "$record"
  rm -rf "$tmp"
}

@test "a Kairos change without headroom for the Kairos operation fails instead of skipping it" {
  tmp=$(mktemp -d)
  lib="$PWD/hack/e2e-chainsaw/_lib/run-kubernetes-upgrade.sh"
  rc=0; output=$(cd "$tmp" && COZY_WORKER_UPGRADE_KAIROS=required COZY_WORKER_UPGRADE_DEADLINE=$(( $(date +%s) + 60 )) bash -c '. "$1"; cozy_worker_upgrade_kairos_reserve() { echo 600; }; cozy_worker_upgrade_kairos_operation bash -c "echo ran"' _ "$lib" 2>&1) || rc=$?
  [ "$rc" -ne 0 ]
  case "$output" in *"::error title=kairos-upgrade::"*"insufficient headroom"*) ;; *) false ;; esac
  case "$output" in *ran*) false ;; esac
  grep --quiet '^failed: insufficient headroom' "$tmp/_out/cozyreport/snapshots/worker-upgrade/kairos-operation.txt"
  rm -rf "$tmp"
}

@test "the Kairos operation reports a missing deadline as such, not as missing headroom" {
  tmp=$(mktemp -d)
  lib="$PWD/hack/e2e-chainsaw/_lib/run-kubernetes-upgrade.sh"
  rc=0; output=$(cd "$tmp" && COZY_WORKER_UPGRADE_KAIROS=required COZY_WORKER_UPGRADE_DEADLINE= bash -c '. "$1"; cozy_worker_upgrade_kairos_reserve() { echo 600; }; cozy_worker_upgrade_kairos_operation bash -c "echo ran"' _ "$lib" 2>&1) || rc=$?
  [ "$rc" -ne 0 ]
  case "$output" in *"deadline is missing or invalid"*) ;; *) false ;; esac
  case "$output" in *headroom*|*ran*) false ;; esac
  rm -rf "$tmp"
}

@test "native inner timeout flags retain individual child group termination" {
  lib="$PWD/hack/e2e-chainsaw/_lib/run-kubernetes-upgrade.sh"
  for mode in term kill; do
    option=--signal=TERM
    child='sleep 6; echo inner-complete'
    if [ "$mode" = kill ]; then option=--kill-after=1s; child='trap "" TERM; sleep 6; echo inner-complete'; fi
    started=$(date +%s)
    rc=0; output=$(bash -c '. "$1"; shift; cozy_worker_upgrade_timeout 5s "$@"' _ "$lib" timeout "$option" 1s sh -c "$child" 2>&1) || rc=$?
    elapsed=$(( $(date +%s) - started ))
    printf '%s exit=%s elapsed=%s output=%s\n' "$mode" "$rc" "$elapsed" "$output"
    [ "$elapsed" -lt 4 ]
    # The inner timeout fired before the bound: a failure, not the budget.
    # When it had to kill, its 137 is kept.
    want=1
    [ "$mode" = term ] || want=137
    [ "$rc" -eq "$want" ]
    case "$output" in *inner-complete*) false ;; esac
  done
}

# A child killed before the bound, by the OOM killer for one, is a failure of
# its own; reporting it as the bound running out would blame the budget.
@test "a child killed before its bound keeps its own status" {
  lib="$PWD/hack/e2e-chainsaw/_lib/run-kubernetes-upgrade.sh"
  rc=0; output=$(bash -c '. "$1"; cozy_worker_upgrade_timeout 5s sh -c "kill -KILL \$\$"' _ "$lib" 2>&1) || rc=$?
  [ "$rc" -eq 137 ]
  rc=0; output=$(bash -c '. "$1"; mkdir --parents _out/cozyreport/snapshots/worker-upgrade; cozy_worker_upgrade_run_bounded 5 sh -c "kill -KILL \$\$"' _ "$lib" 2>&1) || rc=$?
  [ "$rc" -eq 137 ]
  case "$output" in *"ran out"*) false ;; esac
  rc=0; output=$(bash -c '. "$1"; cozy_worker_upgrade_timeout 1s sleep 5' _ "$lib" 2>&1) || rc=$?
  [ "$rc" -eq 124 ]
  # A command's own timeout before the bound is not the budget either.
  rc=0; output=$(bash -c '. "$1"; cozy_worker_upgrade_timeout 30s bash -c "timeout 1 sleep 5"' _ "$lib" 2>&1) || rc=$?
  [ "$rc" -eq 1 ]
  case "$output" in *"timed out on its own"*) ;; *) false ;; esac
}

@test "phase bounds refuse zero negative and malformed durations without starting work" {
  for duration in 0s -1s invalid; do
    rc=0; output=$(bash -c '. hack/e2e-chainsaw/_lib/run-kubernetes-upgrade.sh; cozy_worker_upgrade_timeout "$1" bash -c "echo payload-started"' _ "$duration" 2>&1) || rc=$?
    [ "$rc" -eq 2 ]
    case "$output" in *payload-started*) false ;; esac
  done
}

@test "a stopped inherited phase scope refuses payload admission" {
  tmp=$(mktemp --directory)
  mkdir --parents "$tmp/groups" "$tmp/bin"
  printf '%s\n' '#!/bin/sh' 'exit 0' > "$tmp/bin/timeout"
  chmod +x "$tmp/bin/timeout"
  printf '%s\n' stopped > "$tmp/state"
  rc=0; output=$(_COZY_TIMEOUT_BIN="$(command -v timeout)" _COZY_TIMEOUT_SCOPE="$tmp" bash -c '. hack/e2e-chainsaw/_lib/run-kubernetes-upgrade.sh; cozy_worker_upgrade_timeout 1s bash -c "echo payload-started"' 2>&1) || rc=$?
  [ "$rc" -eq 125 ]
  [ -f "$tmp/state" ]
  case "$output" in *payload-started*) false ;; esac
  rm --recursive --force "$tmp"
}

@test "an inherited phase scope refuses missing or recursive native timeout bindings" {
  tmp=$(mktemp --directory)
  mkdir --parents "$tmp/groups" "$tmp/bin"
  printf '%s\n' '#!/bin/sh' 'exit 0' > "$tmp/bin/timeout"
  chmod +x "$tmp/bin/timeout"
  printf '%s\n' running > "$tmp/state"
  for native in '' "$tmp/bin/timeout"; do
    rc=0; output=$(_COZY_TIMEOUT_BIN="$native" _COZY_TIMEOUT_SCOPE="$tmp" bash -c '. hack/e2e-chainsaw/_lib/run-kubernetes-upgrade.sh; cozy_worker_upgrade_timeout 1s bash -c "echo payload-started"' 2>&1) || rc=$?
    [ "$rc" -eq 125 ]
    case "$output" in *payload-started*) false ;; esac
  done
  rm --recursive --force "$tmp"
}

@test "phase helper preserves piped stdin and explicit EOF" {
  lib="$PWD/hack/e2e-chainsaw/_lib/run-kubernetes-upgrade.sh"
  expected=$(printf 'first payload line\nsecond payload line')
  rc=0; output=$(printf '%s\n' "$expected" | bash -c '. "$1"; cozy_worker_upgrade_timeout 5s cat' _ "$lib" 2>&1) || rc=$?
  [ "$rc" -eq 0 ]
  [ "$output" = "$expected" ] || { printf 'received: %s\n' "$output"; false; }
  rc=0; output=$(bash -c '. "$1"; cozy_worker_upgrade_timeout 5s cat' _ "$lib" </dev/null 2>&1) || rc=$?
  [ "$rc" -eq 0 ]
  [ -z "$output" ]
}

@test "native PATH timeout executes the default-class heredoc payload" {
  lib="$PWD/hack/e2e-chainsaw/_lib/run-kubernetes-upgrade.sh"
  script=$(cat <<'SCRIPT'
timeout --kill-after=1s 2s bash <<'PAYLOAD'
printf '%s\n' heredoc-payload-executed
PAYLOAD
SCRIPT
)
  rc=0; output=$(bash -c '. "$1"; cozy_worker_upgrade_timeout 5s sh -c "$2"' _ "$lib" "$script" 2>&1) || rc=$?
  [ "$rc" -eq 0 ]
  [ "$output" = heredoc-payload-executed ] || { printf 'received: %s\n' "$output"; false; }
}

@test "operation bounds preserve ordinary exit codes and leave the caller timeout unchanged" {
  lib="$PWD/hack/e2e-chainsaw/_lib/run-kubernetes-upgrade.sh"
  rc=0; output=$(bash -c '
    . "$1"
    before=$(command -V timeout)
    cozy_worker_upgrade_kairos_bound() { echo 5; }
    for code in 0 7; do
      actual=0
      cozy_worker_upgrade_kairos_bounded bash -c "exit $code" || actual=$?
      [ "$actual" -eq "$code" ] || exit 1
      actual=0
      cozy_worker_upgrade_run_bounded 5 bash -c "exit $code" || actual=$?
      [ "$actual" -eq "$code" ] || exit 1
    done
    [ "$(command -V timeout)" = "$before" ]
  ' _ "$lib" 2>&1) || rc=$?
  [ "$rc" -eq 0 ]
  [ -z "$output" ]
}

@test "the Kairos bound closes stdout held by a nested timeout and a TERM-ignoring child" {
  lib="$PWD/hack/e2e-chainsaw/_lib/run-kubernetes-upgrade.sh"
  started=$(date +%s)
  rc=0; output=$(bash -c '
    . "$1"
    cozy_worker_upgrade_kairos_bound() { echo 1; }
    cozy_worker_upgrade_kairos_bounded bash -c '\''timeout 18 bash -c "$1"'\'' _ '\''trap "" TERM; echo ready; sleep 18; echo escaped'\''
  ' _ "$lib" 2>&1) || rc=$?
  elapsed=$(( $(date +%s) - started ))
  printf 'exit=%s elapsed=%ss output=%s\n' "$rc" "$elapsed" "$output"
  case "$output" in *ready*) ;; *) false ;; esac
  [ "$elapsed" -lt 12 ]
  case "$output" in *escaped*) false ;; esac
  [ "$rc" -eq 124 ]
}

@test "the upgrade bound closes stdout held by a nested timeout and a TERM-ignoring child" {
  lib="$PWD/hack/e2e-chainsaw/_lib/run-kubernetes-upgrade.sh"
  started=$(date +%s)
  rc=0; output=$(bash -c '
    . "$1"
    cozy_worker_upgrade_run_bounded 1 bash -c '\''timeout 18 bash -c "$1"'\'' _ '\''trap "" TERM; echo ready; sleep 18; echo escaped'\''
  ' _ "$lib" 2>&1) || rc=$?
  elapsed=$(( $(date +%s) - started ))
  printf 'exit=%s elapsed=%ss output=%s\n' "$rc" "$elapsed" "$output"
  case "$output" in *ready*) ;; *) false ;; esac
  [ "$elapsed" -lt 12 ]
  case "$output" in *escaped*) false ;; esac
  [ "$rc" -eq 124 ]
}

# In required mode the runner's own bound encloses the suite, so the
# operation's bound is nested and must still stop what started inside it.
@test "a nested bound closes stdout held by a timeout started inside it" {
  lib="$PWD/hack/e2e-chainsaw/_lib/run-kubernetes-upgrade.sh"
  tmp=$(mktemp --directory)
  cat > "$tmp/step.sh" <<'STEP'
. "$1"
s=$SECONDS
# A Chainsaw step: it waits on its stdout pipe.
{ cozy_worker_upgrade_run_bounded 1 bash -c 'timeout 12 bash -c "trap \"\" TERM; echo ready; sleep 12; echo escaped"'; } 2>/dev/null | cat
echo "pipe closed after $((SECONDS - s))s"
STEP
  rc=0; output=$(cd "$tmp" && bash -c '. "$1"; cozy_worker_upgrade_timeout 60s bash "$2" "$1"' _ "$lib" "$tmp/step.sh" 2>&1) || rc=$?
  printf 'exit=%s output=%s\n' "$rc" "$output"
  rm -rf "$tmp"
  case "$output" in *ready*) ;; *) false ;; esac
  case "$output" in *escaped*) false ;; esac
  case "$output" in *"pipe closed after "[0-5]"s"*) ;; *) false ;; esac
}

@test "a Kairos pool test that outlives its bound is killed and named as such" {
  tmp=$(mktemp -d)
  lib="$PWD/hack/e2e-chainsaw/_lib/run-kubernetes-upgrade.sh"
  record="$tmp/_out/cozyreport/snapshots/worker-upgrade/kairos-operation.txt"
  rc=0; output=$(cd "$tmp" && COZY_WORKER_UPGRADE_KAIROS=required COZY_WORKER_UPGRADE_DEADLINE=$(( $(date +%s) + 3600 )) bash -c '. "$1"; cozy_worker_upgrade_kairos_reserve() { echo 600; }; cozy_worker_upgrade_kairos_ceiling() { echo 21; }; cozy_worker_upgrade_kairos_operation bash -c "sleep 30"' _ "$lib" 2>&1) || rc=$?
  [ "$rc" -eq 124 ]
  case "$output" in *"Kairos pool test exceeded its 1s bound"*) ;; *) false ;; esac
  [ "$(cat "$record")" = "failed 124" ]
  rc=0; output=$(cd "$tmp" && bash -c '. "$1"; cozy_worker_upgrade_kairos_ceiling() { echo 21; }; cozy_worker_upgrade_kairos_bounded bash -c "sleep 30"' _ "$lib" 2>&1) || rc=$?
  [ "$rc" -eq 124 ]
  [ "$(printf '%s\n' "$output" | tail --lines=1)" = "Kairos pool test exceeded its 1s bound" ]
  rm -rf "$tmp"
}

# Outside the upgrade lane there is no deadline and no upgrade variable; the
# bound still comes from the step's own ceiling.
@test "the Kairos bound needs no upgrade deadline" {
  lib="$PWD/hack/e2e-chainsaw/_lib/run-kubernetes-upgrade.sh"
  rc=0; output=$(env -u COZY_WORKER_UPGRADE_DEADLINE -u COZY_WORKER_UPGRADE_MODE -u COZY_WORKER_UPGRADE_KAIROS bash -c '. "$1"; cozy_worker_upgrade_kairos_bound; cozy_worker_upgrade_kairos_bounded bash -c "echo ran"' _ "$lib" 2>&1) || rc=$?
  [ "$rc" -eq 0 ]
  [ "$output" = "$(printf '%s\nran' "$(( $(yq 'select(document_index == 0) | .spec.steps[0].try[] | select(.script.content | contains("run_kairos_pool_test")) | .script.timeout' hack/e2e-chainsaw/kubernetes-latest/chainsaw-test.yaml | sed 's/m$//') * 60 - 20 ))")" ]
}

# The step ceiling kills only its shell, so every path into the Kairos pool
# test goes through the bound, in the upgrade lane and outside it.
@test "every invocation of the Kairos pool test runs under its bound" {
  content=$(yq 'select(document_index == 0) | .spec.steps[0].try[] | select(.script.content | contains("run_kairos_pool_test")) | .script.content' hack/e2e-chainsaw/kubernetes-latest/chainsaw-test.yaml)
  [ "$(printf '%s\n' "$content" | grep -c 'run_kairos_pool_test')" -eq 2 ]
  unbounded=$(printf '%s\n' "$content" | grep 'run_kairos_pool_test' | grep -v -E '^[[:space:]]*cozy_worker_upgrade_kairos_(operation|bounded) bash -c ' || true)
  [ -z "$unbounded" ] || { printf 'unbounded Kairos invocation: %s\n' "$unbounded" >&2; false; }
}

# kubernetes-latest stays on the shared minor; a minor only the candidate offers
# would otherwise run nowhere.
@test "a candidate minor newer than the shared one runs in kubernetes-previous" {
  tmp=$(mktemp -d)
  lib="$PWD/hack/e2e-chainsaw/_lib/run-kubernetes-upgrade.sh"
  mkdir --parents "$tmp/_out/worker-upgrade-baseline/packages/apps/kubernetes-nodes/files" "$tmp/packages/apps/kubernetes-nodes/files" "$tmp/packages/apps/kubernetes/files"
  printf '"v1.34": "v1.34.2"\n"v1.35": "v1.35.2"\n' > "$tmp/_out/worker-upgrade-baseline/packages/apps/kubernetes-nodes/files/versions.yaml"
  printf '"v1.34": "v1.34.2"\n"v1.35": "v1.35.2"\n"v1.36": "v1.36.0"\n' | tee "$tmp/packages/apps/kubernetes-nodes/files/versions.yaml" > "$tmp/packages/apps/kubernetes/files/versions.yaml"
  rc=0; output=$(cd "$tmp" && COZY_WORKER_UPGRADE_MODE=required bash -c '. "$1"; cozy_worker_upgrade_minor; expr=$(cozy_worker_upgrade_previous_version_expr); yq --unwrapScalar "$expr" packages/apps/kubernetes/files/versions.yaml' _ "$lib" 2>&1) || rc=$?
  [ "$rc" -eq 0 ]
  [ "$output" = "$(printf 'v1.35\nv1.36')" ]
  printf '"v1.34": "v1.34.2"\n"v1.35": "v1.35.2"\n' | tee "$tmp/packages/apps/kubernetes-nodes/files/versions.yaml" > "$tmp/packages/apps/kubernetes/files/versions.yaml"
  rc=0; output=$(cd "$tmp" && COZY_WORKER_UPGRADE_MODE=required bash -c '. "$1"; expr=$(cozy_worker_upgrade_previous_version_expr); yq --unwrapScalar "$expr" packages/apps/kubernetes/files/versions.yaml' _ "$lib" 2>&1) || rc=$?
  [ "$rc" -eq 0 ]
  [ "$output" = "v1.34" ]
  rc=0; output=$(cd "$tmp" && COZY_WORKER_UPGRADE_MODE=off bash -c '. "$1"; cozy_worker_upgrade_previous_version_expr' _ "$lib" 2>&1) || rc=$?
  [ "$rc" -eq 0 ]
  [ "$output" = 'keys | sort_by(.) | .[-2]' ]
  grep --quiet --fixed-strings 'cozy_worker_upgrade_previous_version_expr' hack/e2e-chainsaw/kubernetes-previous/chainsaw-test.yaml
  rm -rf "$tmp"
}

@test "every Chainsaw operation that sources the bash-only upgrade library runs under bash" {
  bad=$(for f in hack/e2e-chainsaw/*/chainsaw-test.yaml; do
    yq --output-format=json '.. | select(type == "!!map" and has("script")) | .script' "$f" |
      jq --raw-output --arg f "$f" 'select((.content // "") | test("(^|\n)[[:space:]]*\\. hack/e2e-chainsaw/_lib/run-kubernetes-upgrade\\.sh"))
        | select(.shell != "bash" and ((.content | split("\n") | map(select(test("[^[:space:]]"))) | .[0]) | test("bash <<") | not))
        | "\($f): \(.timeout // "no timeout")"'
  done)
  [ -z "$bad" ] || { printf 'operation sourcing run-kubernetes-upgrade.sh without bash: %s\n' "$bad" >&2; false; }
}

@test "a required upgrade declines the passing-path console capture that would crowd out its own work in the operation" {
  rc=0; output=$(bash -c '. hack/e2e-chainsaw/_lib/run-kubernetes.sh; . hack/e2e-chainsaw/_lib/run-kubernetes-upgrade.sh; _COZY_RUN_STARTED_AT=$(date +%s); cozy_green_capture_has_room before || exit 9; cozy_worker_upgrade_decline_green_capture; cozy_green_capture_has_room after' 2>&1) || rc=$?
  [ "$rc" -eq 1 ]
  case "$output" in *"after: not collected"*) ;; *) false ;; esac
  [ "$(yq 'select(document_index == 0) | .spec.steps[0].try[0].script.content' hack/e2e-chainsaw/kubernetes-latest/chainsaw-test.yaml | grep -c cozy_worker_upgrade_decline_green_capture)" -eq 1 ]
}

@test "a bounded upgrade operation that runs out of time names the stage it was in" {
  tmp=$(mktemp -d)
  lib="$PWD/hack/e2e-chainsaw/_lib/run-kubernetes-upgrade.sh"
  rc=0; output=$(cd "$tmp" && bash -c '. "$1"; cozy_worker_upgrade_run_bounded 2 bash -c ". \"$1\"; cozy_worker_upgrade_stage \"candidate sources\"; sleep 30"' _ "$lib" 2>&1) || rc=$?
  [ "$rc" -eq 124 ]
  case "$output" in *"::error title=worker-upgrade::"*"candidate sources"*) ;; *) false ;; esac
  rc=0; output=$(cd "$tmp" && bash -c '. "$1"; cozy_worker_upgrade_run_bounded 5 bash -c "exit 7"' _ "$lib" 2>&1) || rc=$?
  [ "$rc" -eq 7 ]
  case "$output" in *"::error"*) false ;; esac
  rm -rf "$tmp"
}

# The operation's bound is the sum of the stage ceilings, and the Chainsaw
# step around it has to outlast that sum, so a stage that grows without the
# step growing fails here instead of at the step timeout.
@test "the upgrade step outlasts the sum of the stage ceilings" {
  bound=$(bash -c '. hack/e2e-chainsaw/_lib/run-kubernetes-upgrade.sh; cozy_worker_upgrade_apply_bound')
  [ "$bound" -gt 0 ]
  step=$(yq 'select(document_index == 0) | .spec.steps[0].try[1].script.timeout' hack/e2e-chainsaw/kubernetes-latest/chainsaw-test.yaml)
  case "$step" in *m) step=$(( ${step%m} * 60 )) ;; *) false ;; esac
  [ "$step" -ge $(( bound + 60 )) ] || { echo "step ${step}s does not outlast the ${bound}s stage sum" >&2; false; }
  [ "$(yq 'select(document_index == 0) | .spec.steps[0].try[1].script.content' hack/e2e-chainsaw/kubernetes-latest/chainsaw-test.yaml | grep -c 'cozy_worker_upgrade_run_bounded "$(cozy_worker_upgrade_apply_bound)"')" -eq 1 ]
}

# The sum only bounds the stages if they wait no longer than their ceilings:
# every ceiling is in the sum, and no stage sets a deadline or a timeout of
# its own.
@test "the upgrade stages wait only on the ceilings the bound sums" {
  lib=hack/e2e-chainsaw/_lib/run-kubernetes-upgrade.sh
  body() { awk -v f="$1() {" '$0 == f { on = 1 } on { print } on && $0 == "}" { exit }' "$lib"; }
  bound=$(body cozy_worker_upgrade_apply_bound)
  for name in $(grep -oE '^_cozy_worker_upgrade_[a-z_]+_secs=' "$lib" | tr -d =); do
    case "$bound" in *"$name"*) ;; *) echo "$name is not in the bound" >&2; false ;; esac
  done
  for fn in cozy_worker_upgrade_apply cozy_worker_upgrade_candidate_ready cozy_worker_upgrade_platform_converged cozy_worker_upgrade_controllers_ready cozy_worker_upgrade_releases_ready; do
    text=$(body "$fn")
    [ -n "$text" ] || { echo "no body for $fn" >&2; false; }
    if printf '%s\n' "$text" | grep -nE -- '--timeout[= ]"?[0-9]|date \+%s\) \+ [0-9]|sleep [0-9]{2,}'; then
      echo "$fn waits on a literal instead of a stage ceiling" >&2
      false
    fi
  done
}

# Both snapshots after the candidate is applied run inside the operation, so
# each one's ceiling is in the bound.
@test "the bound counts the snapshot ceiling once per snapshot" {
  lib=hack/e2e-chainsaw/_lib/run-kubernetes-upgrade.sh
  one=$(bash -c '. "$1"; cozy_worker_upgrade_apply_bound' _ "$lib")
  two=$(bash -c '. "$1"; _cozy_worker_upgrade_snapshot_secs=$(( _cozy_worker_upgrade_snapshot_secs + 1 )); cozy_worker_upgrade_apply_bound' _ "$lib")
  snapshots=$(awk '$0 == "cozy_worker_upgrade_apply() {" { on = 1 } on { print } on && $0 == "}" { exit }' "$lib" | grep -c 'cozy_worker_upgrade_snapshot "')
  [ "$snapshots" -ge 1 ]
  [ $(( two - one )) -eq "$snapshots" ]
}

# A snapshot never reads past its own ceiling: each read gets what is left,
# at most 20s, and none starts once the ceiling has passed.
@test "a snapshot read waits only for what is left of the snapshot ceiling" {
  tmp=$(mktemp --directory)
  lib="$PWD/hack/e2e-chainsaw/_lib/run-kubernetes-upgrade.sh"
  rc=0; output=$(cd "$tmp" && bash -c '. "$1"
    echo 1000 > clock
    date() { local now; now=$(( $(cat clock) + 50 )); echo "$now" > clock; echo "$now"; }
    kubectl() { printf "%s\n" "$*" >> reads; printf "%s\n" "{\"items\":[{\"metadata\":{\"name\":\"kubernetes-p-md0\"},\"status\":{\"v1beta2\":{\"readyReplicas\":1}}},{\"metadata\":{\"name\":\"kubernetes-p-custom\"},\"status\":{\"v1beta2\":{\"readyReplicas\":1}}}]}"; }
    _cozy_worker_upgrade_snapshot_secs=60
    COZY_UPGRADE_CONTEXT=ctx cozy_worker_upgrade_snapshot p out' _ "$lib" 2>&1) || rc=$?
  [ "$rc" -ne 0 ]
  [ "$(wc -l < "$tmp/reads")" -eq 1 ]
  grep -qF -- '--request-timeout=10s' "$tmp/reads"
  case "$output" in *"ran out of its 60s ceiling"*) ;; *) echo "$output" >&2; false ;; esac
  rm -rf "$tmp"
}

# The custom pool may still be converging when the workloads start, so its
# Machine is read only after both pools report ready.
@test "the upgrade workloads read the Machines after the pools are ready" {
  tmp=$(mktemp --directory)
  lib="$PWD/hack/e2e-chainsaw/_lib/run-kubernetes-upgrade.sh"
  rc=0; output=$(cd "$tmp" && bash -c '. "$1"
    kubectl() {
      case "$*" in
        *" wait machinedeployments"*) echo "$*" >> waited ;;
        *" get machines"*)
          if [ "$(wc -l < waited 2>/dev/null || echo 0)" -ge 2 ]; then
            printf "%s\n" "{\"items\":[{\"metadata\":{\"labels\":{\"cluster.x-k8s.io/deployment-name\":\"kubernetes-p-md0\"}},\"status\":{\"nodeRef\":{\"name\":\"n0\"}}},{\"metadata\":{\"labels\":{\"cluster.x-k8s.io/deployment-name\":\"kubernetes-p-custom\"}},\"status\":{\"nodeRef\":{\"name\":\"n1\"}}}]}"
          else
            printf "%s\n" "{\"items\":[{\"metadata\":{\"labels\":{\"cluster.x-k8s.io/deployment-name\":\"kubernetes-p-md0\"}},\"status\":{\"nodeRef\":{\"name\":\"n0\"}}}]}"
          fi ;;
        *" apply "*) cat > /dev/null; echo "$*" >> applied ;;
        *"get pod worker-upgrade-md0"*) echo n0 ;;
        *"get pod worker-upgrade-custom"*) echo n1 ;;
      esac
    }
    COZY_UPGRADE_CONTEXT=ctx COZY_UPGRADE_TENANT_CONTEXT=t KAIROS_KC=kc cozy_worker_upgrade_workloads p' _ "$lib" 2>&1) || rc=$?
  [ "$rc" -eq 0 ] || { echo "$output" >&2; false; }
  [ "$(wc -l < "$tmp/applied")" -eq 2 ]
  rm -rf "$tmp"
}

# The default-class pool test runs under a literal inner timeout, so a
# ceiling that grows without it would kill a healthy but slow run unnamed.
@test "the default-class pool step outlasts the sum of its ceilings" {
  bound=$(bash -c '. hack/e2e-chainsaw/_lib/run-kubernetes.sh; . hack/e2e-chainsaw/_lib/run-kubernetes-kairos.sh; . hack/e2e-chainsaw/_lib/run-kubernetes-upgrade.sh; cozy_worker_upgrade_default_bound')
  [ "$bound" -gt 0 ]
  op=$(yq 'select(document_index == 0) | .spec.steps[0].try[] | select(.script.content // "" | contains("cozy_worker_upgrade_default_test")) | .script' hack/e2e-chainsaw/kubernetes-latest/chainsaw-test.yaml)
  step=$(printf '%s\n' "$op" | yq '.timeout')
  inner=$(printf '%s\n' "$op" | yq '.content' | grep -oE '^timeout --kill-after=[0-9]+s [0-9]+m ' | grep -oE '[0-9]+m $' | tr -d 'm ')
  case "$step" in *m) step=$(( ${step%m} * 60 )) ;; *) false ;; esac
  [ -n "$inner" ]
  inner=$(( inner * 60 ))
  [ "$inner" -ge $(( bound + 60 )) ] || { echo "inner ${inner}s does not outlast the ${bound}s ceiling sum" >&2; false; }
  [ "$step" -ge $(( inner + 60 )) ] || { echo "step ${step}s does not outlast the ${inner}s inner timeout" >&2; false; }
}

# The default-class pool test waits only on the ceilings that bound sums.
@test "the default-class pool stages wait only on the ceilings its bound sums" {
  lib=hack/e2e-chainsaw/_lib/run-kubernetes-upgrade.sh
  body() { awk -v f="$1() {" '$0 == f { on = 1 } on { print } on && $0 == "}" { exit }' "$lib"; }
  bound=$(body cozy_worker_upgrade_default_bound)
  for name in $(grep -oE '^_cozy_default_pool_[a-z_]+_secs=' "$lib" | tr -d =); do
    case "$bound" in *"$name"*) ;; *) echo "$name is not in the bound" >&2; false ;; esac
  done
  for fn in cozy_worker_upgrade_default_test cozy_worker_upgrade_default_create cozy_worker_upgrade_default_node cozy_worker_upgrade_default_probe cozy_worker_upgrade_default_drain; do
    text=$(body "$fn")
    [ -n "$text" ] || { echo "no body for $fn" >&2; false; }
    if printf '%s\n' "$text" | grep -nE -- '--timeout[= ]"?[0-9]|date \+%s\) \+ [0-9]|sleep [0-9]{2,}| [0-9]{2,} \|\|'; then
      echo "$fn waits on a literal instead of a ceiling" >&2
      false
    fi
  done
}

# The tenant connection's ceiling is priced by hand from these waits.
@test "the tenant connection waits the bound prices" {
  text=$(awk '$0 == "cozy_kairos_tenant_kubeconfig() {" { on = 1 } on { print } on && $0 == "}" { exit }' hack/e2e-chainsaw/_lib/run-kubernetes-kairos.sh)
  [ "$(printf '%s\n' "$text" | grep -oE -- '--timeout=[0-9]+s|timeout [0-9]+' | paste -s -d '|' -)" = "--timeout=60s|timeout 90|timeout 60" ]
}

@test "the upgrade apply records each stage before it starts" {
  rc=0; output=$(bash hack/testdata/worker-upgrade/upgrade-order.sh stages 2>&1) || rc=$?
  [ "$rc" -eq 0 ]
  [ "$(printf '%s\n' "$output" | tail -n 1)" = "stable snapshot" ]
  [ "$(yq 'select(document_index == 0) | .spec.steps[0].try[1].script.content' hack/e2e-chainsaw/kubernetes-latest/chainsaw-test.yaml | grep -c 'cozy_worker_upgrade_run_bounded')" -eq 1 ]
}
