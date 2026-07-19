#!/usr/bin/env bats
# Unit test: no generated values.schema.json may declare a non-string default
# on a property typed "string".
#
# cozyvalues-gen quotes a default only when the declared type is the literal
# `string` or `quantity` (formatDefault in internal/openapi/openapi.go). A
# named alias — including one declared `@enum {string}` — falls through to a
# YAML re-parse of the bare value, where the 1.1 boolean words resolve. So a
# chart that materialises `authClients: "no"` under an `@enum` type emits
# `"default": false` into a property whose type is "string" and whose enum is
# ["no","optional","yes"] — a default the field's own schema rejects. The same
# coercion reaches api/ as `+kubebuilder:default:=false` on a string-typed Go
# field, so the CRD defaults an enum-constrained string to a boolean and every
# CR relying on the default is rejected. "optional" survives; "no" and "yes"
# do not. The quotes in values.yaml do not help: the value reaches
# formatDefault as a flat string with no record of having been quoted.
#
# The damage is invisible to the chart test suites: templates read the value
# from values.yaml, not from the schema, so rendering stays correct and only
# the shipped schema and CRD are wrong. This test is the guard, and it is why
# packages/apps/redis/values.yaml leaves `tls: {}` empty and resolves the
# authClients default in templates/_tls.tpl instead.
#
# Runs under hack/cozytest.sh, which is POSIX /bin/sh — no process
# substitution, no arrays, no bats `run`.

REPO_ROOT="$(cd "$(dirname "${BATS_TEST_FILENAME:-$0}")/.." && pwd)"

@test "no values.schema.json declares a non-string default on a string-typed property" {
  violations="$(find "$REPO_ROOT/packages" -name values.schema.json -exec jq -r '
    input_filename as $f
    | [ .. | objects
        | select(.type == "string" and has("default") and (.default | type) != "string")
        | "\($f): default=\(.default | tojson) enum=\(.enum // "none" | tojson)"
      ] | .[]' {} +)"

  if [ -n "$violations" ]; then
    echo "String-typed schema properties carrying a non-string default:" >&2
    echo "$violations" >&2
    echo "This is cozyvalues-gen coercing a YAML 1.1 boolean word (no/yes/on/off)" >&2
    echo "out of values.yaml. Leave the field absent from values.yaml and resolve" >&2
    echo "its default in the template instead." >&2
    return 1
  fi
}
