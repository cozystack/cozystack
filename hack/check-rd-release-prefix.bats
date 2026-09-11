#!/usr/bin/env bats
# Unit test: every resourceNames selector in an ApplicationDefinition names the
# object the chart actually creates.
#
# Release names are "<prefix><app name>" (api/v1alpha1/applicationdefinitions_types.go,
# applied in pkg/registry/apps/application/rest.go), but the template context the
# lineage webhook evaluates selectors in carries only {kind, name, namespace} --
# name being the custom resource's name, without the prefix
# (internal/lineagecontrollerwebhook/webhook.go, computeLabels). So a selector has
# to spell the prefix out, and the definitions do: "postgres-{{ .name }}-credentials",
# "bucket-{{ .name }}-credentials", and so on.
#
# Omitting it produces no error anywhere. The selector simply matches nothing, which
# is indistinguishable from a selector that legitimately has nothing to match: the
# webhook labels the object internal.cozystack.io/tenantresource=false and moves on.
# harbor shipped that way and the Secret it names never reached the TenantSecrets
# API; the symptom was worked around by stamping a label on the Secret rather than
# by fixing the name, so the broken selector stayed in the tree unnoticed.
#
# The assertion is "contains <prefix>{{ .name }}" rather than "starts with the
# prefix", because operator-created objects legitimately carry their own leading
# words: mongodb excludes "internal-mongodb-{{ .name }}-users", which is correct and
# must stay green.
#
# Definitions with an empty prefix are skipped: packages/extra/* are generated with
# PREFIX="" by hack/update-crd.sh, so their releases carry no prefix to assert.

REPO_ROOT="$(cd "$(dirname "${BATS_TEST_FILENAME:-$0}")/.." && pwd)"

@test "every resourceNames selector carries its release prefix" {
  run python3 - "$REPO_ROOT" <<'PY'
import glob, os, sys, yaml

root = sys.argv[1]
violations = []

for path in sorted(glob.glob(os.path.join(root, "packages/system/*-rd/cozyrds/*.yaml"))):
    with open(path) as fh:
        doc = yaml.safe_load(fh)
    if not doc:
        continue
    spec = doc.get("spec") or {}
    prefix = ((spec.get("release") or {}).get("prefix") or "")
    if not prefix:
        continue
    needle = prefix + "{{ .name }}"
    for section in ("secrets", "services", "ingresses"):
        selectors = spec.get(section) or {}
        for half in ("include", "exclude"):
            for selector in (selectors.get(half) or []):
                for name in ((selector or {}).get("resourceNames") or []):
                    if needle not in name:
                        rel = os.path.relpath(path, root)
                        violations.append(f"{rel}: {section}.{half}: {name!r} does not contain {needle!r}")

if violations:
    print("\n".join(violations))
    sys.exit(1)
PY
  [ "$status" -eq 0 ] || {
    echo "resourceNames selectors that can never match the objects their chart creates:"
    echo "$output"
    false
  }
}
