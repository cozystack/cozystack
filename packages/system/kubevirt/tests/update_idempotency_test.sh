#!/usr/bin/env bash
# Regression test for the `make update` idempotency contract.
#
# Each cozystack parameterization patch must be reinserted INDEPENDENTLY of the
# others. A hand-merge from a newer upstream KubeVirt CR may drop one directive
# while keeping the rest; `make update` must self-heal whichever directive is
# missing rather than fail. The trap this pins: a single guard that gates the
# insertion of two directives at once silently skips the second one when the
# first is still present, and the sanity-check tail then aborts the target.
set -euo pipefail

# The update target relies on GNU sed (`sed -i` with `a\`/`i\` insert syntax and
# the range-delete used below). Skip on BSD sed so the suite stays green on a
# stock macOS without gnu-sed.
if ! sed --version 2>/dev/null | grep -q GNU; then
	echo "SKIP update_idempotency_test: GNU sed required" >&2
	exit 0
fi

here="$(cd "$(dirname "$0")" && pwd)"
pkg="$(dirname "$here")"
src="$pkg/templates/kubevirt-cr.yaml"

fail() {
	echo "FAIL update_idempotency_test: $*" >&2
	exit 1
}

assert_present() {
	if ! grep -qF "$1" "$2"; then
		fail "expected directive missing from $2: $1"
	fi
}

assert_absent() {
	if grep -qF "$1" "$2"; then
		fail "unexpected directive present in $2: $1"
	fi
}

assert_count() {
	local want="$1" pat="$2" file="$3" got
	got="$(grep -cF "$pat" "$file" || true)"
	if [ "$got" -ne "$want" ]; then
		fail "expected $want occurrence(s) of '$pat' in $file, got $got"
	fi
}

run_update() {
	make -C "$pkg" update CR_FILE="$1" >/dev/null 2>&1
}

tmpdir="$(mktemp -d)"
trap 'rm -rf "$tmpdir"' EXIT

PHD='{{- with .Values.permittedHostDevices }}'
MDC='{{- with .Values.mediatedDevicesConfiguration }}'

# --- case 1: a hand-merge dropped ONLY the mediatedDevicesConfiguration block.
# `make update` must reinsert it without duplicating permittedHostDevices.
missing_mdev="$tmpdir/missing-mdev.yaml"
sed '/{{- with .Values.mediatedDevicesConfiguration }}/,/{{- end }}/d' "$src" >"$missing_mdev"
assert_present "$PHD" "$missing_mdev"
assert_absent "$MDC" "$missing_mdev"
if ! run_update "$missing_mdev"; then
	fail "make update exited non-zero on a template missing only mediatedDevicesConfiguration"
fi
assert_present "$MDC" "$missing_mdev"
assert_count 1 "$PHD" "$missing_mdev"

# --- case 2: a hand-merge dropped ONLY the permittedHostDevices block (the
# symmetric trap). `make update` must reinsert it without touching the other.
missing_phd="$tmpdir/missing-phd.yaml"
sed '/{{- with .Values.permittedHostDevices }}/,/{{- end }}/d' "$src" >"$missing_phd"
assert_absent "$PHD" "$missing_phd"
assert_present "$MDC" "$missing_phd"
if ! run_update "$missing_phd"; then
	fail "make update exited non-zero on a template missing only permittedHostDevices"
fi
assert_present "$PHD" "$missing_phd"
assert_count 1 "$MDC" "$missing_phd"

# --- case 3: an already-parameterized template is a no-op (idempotent).
full="$tmpdir/full.yaml"
cp "$src" "$full"
if ! run_update "$full"; then
	fail "make update failed on an already-parameterized template"
fi
if ! diff -u "$src" "$full" >/dev/null; then
	fail "make update mutated an already-parameterized template"
fi

# --- case 4: repeated runs never duplicate a guard directive.
run_update "$missing_mdev"
for guard in \
	'{{- if .Values.cpuAllocationRatio }}' \
	'{{- range .Values.extraFeatureGates }}' \
	"$PHD" \
	"$MDC"; do
	assert_count 1 "$guard" "$missing_mdev"
done

echo "PASS update_idempotency_test"
