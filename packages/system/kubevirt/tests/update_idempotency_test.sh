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

# Same call, but keeps stdout+stderr so a case can assert on the diagnostic.
run_update_logged() {
	make -C "$pkg" update CR_FILE="$1" >"$2" 2>&1
}

tmpdir="$(mktemp -d)"
trap 'rm -rf "$tmpdir"' EXIT

PHD='{{- with .Values.permittedHostDevices }}'
MDC='{{- with .Values.mediatedDevicesConfiguration }}'
MIG='{{- with .Values.migrations }}'

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
	"$MDC" \
	"$MIG"; do
	assert_count 1 "$guard" "$missing_mdev"
done

# A hand-merge may drop migrations while preserving both device blocks.
# Restoring it must reproduce the committed template, including the YAML key
# and indentation, rather than merely reintroduce the directive.
missing_migrations="$tmpdir/missing-migrations.yaml"
sed '/{{- with .Values.migrations }}/,/{{- end }}/d' "$src" >"$missing_migrations"
assert_absent "$MIG" "$missing_migrations"
assert_present "$PHD" "$missing_migrations"
assert_present "$MDC" "$missing_migrations"
if ! run_update "$missing_migrations"; then
	fail "make update exited non-zero on a template missing only migrations"
fi
assert_present "$MIG" "$missing_migrations"
if ! diff -u "$src" "$missing_migrations" >/dev/null; then
	fail "make update rebuilt migrations differently from the committed template"
fi
run_update "$missing_migrations"
assert_count 1 "$MIG" "$missing_migrations"
if ! diff -u "$src" "$missing_migrations" >/dev/null; then
	fail "repeated make update changed the restored migrations template"
fi

# sed succeeds even when its insertion anchor is absent; the target's sanity
# check must reject a hand-merged CR that cannot restore migration settings.
anchorless_migrations="$tmpdir/anchorless-migrations.yaml"
sed -e '/{{- with .Values.migrations }}/,/{{- end }}/d' \
	-e '/^    evictionStrategy:/d' "$src" >"$anchorless_migrations"
anchorless_migrations_log="$tmpdir/anchorless-migrations.log"
if run_update_logged "$anchorless_migrations" "$anchorless_migrations_log"; then
	fail "make update exited zero without the migrations block or its evictionStrategy anchor"
fi
if ! grep -qF "directive '$MIG' not inserted" "$anchorless_migrations_log"; then
	fail "make update did not name the missing migrations directive: $(cat "$anchorless_migrations_log")"
fi

# A retained opening directive must not hide a missing body or terminator.
# The opening-less case also leaves an orphan key: inserting a second complete
# block must not make that damaged template pass validation.
for missing in key payload end body opening; do
	partial_migrations="$tmpdir/migrations-missing-$missing.yaml"
	awk -v missing="$missing" -v opening="$MIG" '
		index($0, opening) { inside=1; if (missing != "opening") print; next }
		inside && /{{- end }}/ { inside=0; if (missing != "end") print; next }
		inside && /^    migrations:$/ && (missing == "key" || missing == "body") { next }
		inside && /{{- toYaml \. \| nindent 6 }}/ && (missing == "payload" || missing == "body") { next }
		{ print }
	' "$src" >"$partial_migrations"
	partial_migrations_log="$tmpdir/migrations-missing-$missing.log"
	if run_update_logged "$partial_migrations" "$partial_migrations_log"; then
		fail "make update exited zero on a migrations block missing $missing"
	fi
	if ! grep -qF 'migrations block is partial or duplicated' "$partial_migrations_log"; then
		fail "make update did not diagnose migrations missing $missing: $(cat "$partial_migrations_log")"
	fi
done

# Presence checks can also accept reordered lines, a duplicate complete block,
# or a complete body with EOF in place of its closing directive.
for malformed in reordered duplicate eof; do
	malformed_migrations="$tmpdir/migrations-$malformed.yaml"
	awk -v malformed="$malformed" -v opening="$MIG" '
		index($0, opening) { inside=1; block=$0 ORS; print; next }
		inside {
			block=block $0 ORS
			if (/{{- end }}/) {
				if (malformed == "eof") exit
				inside=0; print
				if (malformed == "duplicate") printf "%s", block
				next
			}
			if (malformed == "reordered" && /^    migrations:$/) { print "      {{- toYaml . | nindent 6 }}"; next }
			if (malformed == "reordered" && /{{- toYaml \. \| nindent 6 }}/) { print "    migrations:"; next }
		}
		{ print }
	' "$src" >"$malformed_migrations"
	malformed_migrations_log="$tmpdir/migrations-$malformed.log"
	if run_update_logged "$malformed_migrations" "$malformed_migrations_log"; then
		fail "make update exited zero on a $malformed migrations block"
	fi
	if ! grep -qF 'migrations block is partial or duplicated' "$malformed_migrations_log"; then
		fail "make update did not diagnose $malformed migrations: $(cat "$malformed_migrations_log")"
	fi
done

echo "PASS update_idempotency_test"
