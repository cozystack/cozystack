export NAME=kubevirt
export NAMESPACE=cozy-$(NAME)

include ../../../hack/package.mk

test:
	helm unittest .

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
#   3. Operator runs `make update` to re-apply the four cozystack
#      parameterization sed patches in case they were lost during the
#      hand-merge.
#
# GNU sed is required — on macOS run under a Linux container or via gsed.
update:
	@test -f templates/kubevirt-cr.yaml || { echo "ERROR: templates/kubevirt-cr.yaml not found — drop a hand-merged upstream CR there first" >&2; exit 1; }
	@# Idempotent — each sed only inserts if the matching cozystack patch
	@# isn't already present in the file.
	grep -q '^  namespace: cozy-kubevirt$$' templates/kubevirt-cr.yaml || sed -i 's/namespace: kubevirt/namespace: cozy-kubevirt/g' templates/kubevirt-cr.yaml
	grep -q '{{- if .Values.cpuAllocationRatio }}' templates/kubevirt-cr.yaml || sed -i '/^    developerConfiguration:$$/a\      {{- if .Values.cpuAllocationRatio }}\n      cpuAllocationRatio: {{ .Values.cpuAllocationRatio }}\n      {{- end }}' templates/kubevirt-cr.yaml
	grep -q '{{- range .Values.extraFeatureGates }}' templates/kubevirt-cr.yaml || sed -i '/^      - VMExport$$/a\      {{- range .Values.extraFeatureGates }}\n      - {{ . }}\n      {{- end }}' templates/kubevirt-cr.yaml
	grep -q '{{- with .Values.permittedHostDevices }}' templates/kubevirt-cr.yaml || sed -i '/^    evictionStrategy:/i\    {{- with .Values.permittedHostDevices }}\n    permittedHostDevices:\n      {{- toYaml . | nindent 6 }}\n    {{- end }}\n    {{- with .Values.mediatedDevicesConfiguration }}\n    mediatedDevicesConfiguration:\n      {{- toYaml . | nindent 6 }}\n    {{- end }}' templates/kubevirt-cr.yaml
	@# Sanity-check: every cozystack patch MUST end up in the file. If the
	@# hand-merge step lost an anchor (developerConfiguration:, - VMExport,
	@# evictionStrategy:) the sed silently no-ops and the chart ships without
	@# the parameterization. Fail loudly so the operator hand-re-applies it.
	@grep -q '{{- if .Values.cpuAllocationRatio }}' templates/kubevirt-cr.yaml || { echo "ERROR: cpuAllocationRatio anchor 'developerConfiguration:' not found" >&2; exit 1; }
	@grep -q '{{- range .Values.extraFeatureGates }}' templates/kubevirt-cr.yaml || { echo "ERROR: extraFeatureGates anchor '- VMExport' not found" >&2; exit 1; }
	@grep -q '{{- with .Values.permittedHostDevices }}' templates/kubevirt-cr.yaml || { echo "ERROR: permittedHostDevices anchor 'evictionStrategy:' not found" >&2; exit 1; }
	@grep -q '{{- with .Values.mediatedDevicesConfiguration }}' templates/kubevirt-cr.yaml || { echo "ERROR: mediatedDevicesConfiguration anchor 'evictionStrategy:' not found" >&2; exit 1; }
