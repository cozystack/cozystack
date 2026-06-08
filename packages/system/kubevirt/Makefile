export NAME=kubevirt
export NAMESPACE=cozy-$(NAME)

include ../../../hack/package.mk

test:
	helm unittest .
	./tests/update_idempotency_test.sh

# File the parameterization patches are applied to. Overridable so the
# idempotency test can drive `update` against a throwaway copy instead of the
# in-tree CR.
CR_FILE ?= templates/kubevirt-cr.yaml

# Re-apply the cozystack-specific parameterization on top of the in-tree
# templates/kubevirt-cr.yaml. This target does NOT download anything from
# upstream — the live KubeVirt CR distributed by upstream is a near-empty
# stub (featureGates: [], customizeComponents: {}, imagePullPolicy, empty
# workloadUpdateStrategy) and cannot stand in for the in-tree file, which
# carries cozystack-specific defaults (cozy-kubevirt namespace, full
# featureGates list including LiveMigration / AutoResourceLimitsGate /
# CPUManager / GPU / VMExport, evictionStrategy: LiveMigrate,
# workloadUpdateMethods, vmRolloutStrategy, monitorNamespace, etc.) that
# have been hand-curated over multiple cozystack releases.
#
# To refresh from a newer upstream KubeVirt:
#   1. Operator inspects the new upstream release notes for spec changes
#      that affect the cozystack-managed fields listed above.
#   2. Operator hand-merges the new fields into templates/kubevirt-cr.yaml.
#   3. Operator runs `make update` to re-apply the five cozystack
#      parameterization sed patches in case they were lost during the
#      hand-merge.
#
# GNU sed is required — on macOS run under a Linux container or via gsed.
update:
	@test -f $(CR_FILE) || { echo "ERROR: $(CR_FILE) not found — drop a hand-merged upstream CR there first" >&2; exit 1; }
	@# Idempotent — each sed only inserts if the matching cozystack patch
	@# isn't already present in the file.
	grep -q '^  namespace: cozy-kubevirt$$' $(CR_FILE) || sed -i 's/namespace: kubevirt/namespace: cozy-kubevirt/g' $(CR_FILE)
	grep -q '{{- if .Values.cpuAllocationRatio }}' $(CR_FILE) || sed -i '/^    developerConfiguration:$$/a\      {{- if .Values.cpuAllocationRatio }}\n      cpuAllocationRatio: {{ .Values.cpuAllocationRatio }}\n      {{- end }}' $(CR_FILE)
	grep -q '{{- range .Values.extraFeatureGates }}' $(CR_FILE) || sed -i '/^      - VMExport$$/a\      {{- range .Values.extraFeatureGates }}\n      - {{ . }}\n      {{- end }}' $(CR_FILE)
	@# permittedHostDevices and mediatedDevicesConfiguration are guarded
	@# independently: a hand-merge may drop one while keeping the other, and
	@# each must self-heal on its own. A single guard over both would skip the
	@# second insert whenever the first is still present, then trip the
	@# sanity-check tail below.
	grep -q '{{- with .Values.permittedHostDevices }}' $(CR_FILE) || sed -i '/^    evictionStrategy:/i\    {{- with .Values.permittedHostDevices }}\n    permittedHostDevices:\n      {{- toYaml . | nindent 6 }}\n    {{- end }}' $(CR_FILE)
	grep -q '{{- with .Values.mediatedDevicesConfiguration }}' $(CR_FILE) || sed -i '/^    evictionStrategy:/i\    {{- with .Values.mediatedDevicesConfiguration }}\n    mediatedDevicesConfiguration:\n      {{- toYaml . | nindent 6 }}\n    {{- end }}' $(CR_FILE)
	@# Sanity-check: every cozystack patch MUST end up in the file. If the
	@# hand-merge step moved or renamed an anchor (developerConfiguration:,
	@# - VMExport, evictionStrategy:) the sed silently no-ops and the chart
	@# ships without the parameterization. Each message names the directive
	@# that failed to land and the anchor its insert keys off, so the operator
	@# knows which line to restore before re-running.
	@grep -q '{{- if .Values.cpuAllocationRatio }}' $(CR_FILE) || { echo "ERROR: directive '{{- if .Values.cpuAllocationRatio }}' not inserted — restore the 'developerConfiguration:' anchor in $(CR_FILE) and re-run" >&2; exit 1; }
	@grep -q '{{- range .Values.extraFeatureGates }}' $(CR_FILE) || { echo "ERROR: directive '{{- range .Values.extraFeatureGates }}' not inserted — restore the '- VMExport' anchor in $(CR_FILE) and re-run" >&2; exit 1; }
	@grep -q '{{- with .Values.permittedHostDevices }}' $(CR_FILE) || { echo "ERROR: directive '{{- with .Values.permittedHostDevices }}' not inserted — restore the 'evictionStrategy:' anchor in $(CR_FILE) and re-run" >&2; exit 1; }
	@grep -q '{{- with .Values.mediatedDevicesConfiguration }}' $(CR_FILE) || { echo "ERROR: directive '{{- with .Values.mediatedDevicesConfiguration }}' not inserted — restore the 'evictionStrategy:' anchor in $(CR_FILE) and re-run" >&2; exit 1; }
