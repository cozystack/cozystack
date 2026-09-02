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
DFG='{{- range .Values.disabledFeatureGates }}'

# The disabledFeatureGates block, comment lines included, ending on the
# featureGates: line that follows it. Cases below either delete this range or
# extract it.
DFG_BLOCK_RANGE='/^      # Template is Beta/,/^      featureGates:$/'

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
	"$DFG" \
	"$PHD" \
	"$MDC"; do
	assert_count 1 "$guard" "$missing_mdev"
done

# --- case 5: a hand-merge dropped ONLY the disabledFeatureGates block. Its
# insert keys off the featureGates: line the other gate directive leaves
# alone, so it must come back with both static opt-outs intact.
#
# The block runs from its first comment line — the insert carries those too —
# down to the featureGates: line below it. It is delimited by that line rather
# than by its own last one because it ends in two consecutive `{{- end }}`
# lines, and a range terminated on the first of them would leave the second
# behind.
missing_dfg="$tmpdir/missing-dfg.yaml"
sed "$DFG_BLOCK_RANGE{/^      featureGates:\$/!d}" "$src" >"$missing_dfg"
assert_absent "$DFG" "$missing_dfg"
assert_absent '      - Template' "$missing_dfg"
assert_absent '      - ExternalNetResourceInjection' "$missing_dfg"
assert_absent '      # Template is Beta' "$missing_dfg"
assert_present '{{- range .Values.extraFeatureGates }}' "$missing_dfg"
if ! run_update "$missing_dfg"; then
	fail "make update exited non-zero on a template missing only disabledFeatureGates"
fi
assert_count 1 "$DFG" "$missing_dfg"
assert_count 1 '      - Template' "$missing_dfg"
assert_count 1 '      - ExternalNetResourceInjection' "$missing_dfg"
assert_count 1 '{{- range .Values.extraFeatureGates }}' "$missing_dfg"

# --- case 6: the block the sed rebuilds must equal the one committed in the
# template, byte for byte. The insert text in the Makefile and the block in
# templates/kubevirt-cr.yaml are two copies of the same lines, and only one of
# them renders — the copy in the Makefile is exercised nowhere else, so an edit
# to the block that stops at the template leaves `make update` silently able to
# restore a stale version of it.
extract_dfg_block() {
	sed -n "${DFG_BLOCK_RANGE}p" "$1" | sed '$d'
}

src_block="$tmpdir/dfg-block-committed.txt"
rebuilt_block="$tmpdir/dfg-block-rebuilt.txt"
extract_dfg_block "$src" >"$src_block"
extract_dfg_block "$missing_dfg" >"$rebuilt_block"
if ! diff -u "$src_block" "$rebuilt_block" >"$tmpdir/dfg-block.diff"; then
	fail "make update rebuilt a disabledFeatureGates block that differs from the committed template; update the sed insert text in the Makefile: $(cat "$tmpdir/dfg-block.diff")"
fi

# --- case 7: a hand-merge kept the range directive but dropped the static
# `- Template` line under it. The insert is guarded by the directive, so
# `make update` cannot rebuild the block; the sanity-check tail must say so
# instead of exiting zero on a CR that would deploy virt-template.
partial_dfg="$tmpdir/partial-dfg.yaml"
sed '/^      - Template$/d' "$src" >"$partial_dfg"
assert_present "$DFG" "$partial_dfg"
assert_present '      disabledFeatureGates:' "$partial_dfg"
assert_absent '      - Template' "$partial_dfg"
partial_log="$tmpdir/partial-dfg.log"
if run_update_logged "$partial_dfg" "$partial_log"; then
	fail "make update exited zero on a disabledFeatureGates block missing its static Template entry"
fi
if ! grep -qF "marker '      - Template' missing" "$partial_log"; then
	fail "make update did not name the missing Template marker: $(cat "$partial_log")"
fi

# --- case 8: the symmetric loss — the block keeps `- Template` but lost the
# `disabledFeatureGates:` key it hangs under, leaving the entry dangling under
# developerConfiguration instead of disabling anything.
keyless_dfg="$tmpdir/keyless-dfg.yaml"
sed '/^      disabledFeatureGates:$/d' "$src" >"$keyless_dfg"
assert_present "$DFG" "$keyless_dfg"
assert_present '      - Template' "$keyless_dfg"
assert_absent '      disabledFeatureGates:' "$keyless_dfg"
keyless_log="$tmpdir/keyless-dfg.log"
if run_update_logged "$keyless_dfg" "$keyless_log"; then
	fail "make update exited zero on a disabledFeatureGates block missing its key line"
fi
if ! grep -qF "marker '      disabledFeatureGates:' missing" "$keyless_log"; then
	fail "make update did not name the missing disabledFeatureGates key: $(cat "$keyless_log")"
fi

# --- case 9: the second static entry has the same exposure as the first, and
# losing it is quieter — the CR keeps disabling virt-template while KubeVirt
# silently stops injecting NAD resourceName requests into launcher pods.
partial_enri="$tmpdir/partial-enri.yaml"
sed '/^      - ExternalNetResourceInjection$/d' "$src" >"$partial_enri"
assert_present "$DFG" "$partial_enri"
assert_present '      - Template' "$partial_enri"
assert_absent '      - ExternalNetResourceInjection' "$partial_enri"
enri_log="$tmpdir/partial-enri.log"
if run_update_logged "$partial_enri" "$enri_log"; then
	fail "make update exited zero on a disabledFeatureGates block missing its static ExternalNetResourceInjection entry"
fi
if ! grep -qF "marker '      - ExternalNetResourceInjection' missing" "$enri_log"; then
	fail "make update did not name the missing ExternalNetResourceInjection marker: $(cat "$enri_log")"
fi

# --- case 10: the third static entry is the one wrapped in a version
# condition, so losing it takes the condition's payload with it and a cluster
# below Kubernetes 1.35 goes back to launcher pods whose image volumes the
# apiserver strips.
partial_iv="$tmpdir/partial-iv.yaml"
sed '/^      - ImageVolume$/d' "$src" >"$partial_iv"
assert_present "$DFG" "$partial_iv"
assert_present '      - Template' "$partial_iv"
assert_absent '      - ImageVolume' "$partial_iv"
iv_log="$tmpdir/partial-iv.log"
if run_update_logged "$partial_iv" "$iv_log"; then
	fail "make update exited zero on a disabledFeatureGates block missing its static ImageVolume entry"
fi
if ! grep -qF "marker '      - ImageVolume' missing" "$iv_log"; then
	fail "make update did not name the missing ImageVolume marker: $(cat "$iv_log")"
fi

# --- case 11: the block's skip reads $staticDisabledFeatureGates from the guard
# header, and no insert in the target restores that header. A hand-merge that
# drops it leaves a template that cannot render at all, so the tail has to say
# which line is missing rather than exit zero on it.
headerless="$tmpdir/headerless.yaml"
sed '/{{- \$staticDisabledFeatureGates := /d' "$src" >"$headerless"
assert_present "$DFG" "$headerless"
assert_present '{{- if not (has . $staticDisabledFeatureGates) }}' "$headerless"
assert_absent '{{- $staticDisabledFeatureGates := ' "$headerless"
headerless_log="$tmpdir/headerless.log"
if run_update_logged "$headerless" "$headerless_log"; then
	fail "make update exited zero on a template whose guard header lost \$staticDisabledFeatureGates"
fi
if ! grep -qF 'staticDisabledFeatureGates := ' "$headerless_log"; then
	fail "make update did not name the missing guard-header line: $(cat "$headerless_log")"
fi

echo "PASS update_idempotency_test"
