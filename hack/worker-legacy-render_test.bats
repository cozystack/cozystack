#!/usr/bin/env bats

load test_helper

@test "classless factory builtin and Proxmox pools preserve independent legacy renders" {
  legacy_tmp="$(mktemp -d)"
  legacy_root="$(pwd)"
  legacy_profile=legacy
  if jq --exit-status '.properties | has("labels")' packages/apps/kubernetes-nodes/values.schema.json >/dev/null; then
    legacy_profile=scheduling
  fi
  rc=0
  python3 -I hack/testdata/worker-legacy-render/render.py "${legacy_root}" "${legacy_root}/hack/testdata/worker-legacy-render" "${legacy_tmp}" > "${legacy_tmp}/actual.json" || rc=$?
  if [ "${rc}" -eq 0 ]; then
    diff --unified "hack/testdata/worker-legacy-render/${legacy_profile}-digests.json" "${legacy_tmp}/actual.json" || rc=$?
  fi
  python3 -I - "${legacy_tmp}" <<'PY'
import shutil, sys
shutil.rmtree(sys.argv[1])
PY
  [ "${rc}" -eq 0 ]
}
