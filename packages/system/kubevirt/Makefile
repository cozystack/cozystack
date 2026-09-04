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
# carries the cozystack-specific settings: the namespace, the two feature-gate
# lists, evictionStrategy, workloadUpdateMethods, vmRolloutStrategy,
# monitorNamespace and the virt-handler resource patch.
#
# The gate lists are not defaults to carry forward unread. Check them against
# the pinned release's pkg/virt-config/featuregate/{active,inactive}.go at every
# bump: a gate that reached GA is enabled whether or not the CR names it, so its
# line does nothing, and a deprecated one is on a removal clock.
#
# To refresh from a newer upstream KubeVirt:
#   1. Operator inspects the new upstream release notes for spec changes
#      that affect the cozystack-managed fields listed above.
#   2. Operator hand-merges the new fields into templates/kubevirt-cr.yaml.
#   3. Operator runs `make update` to re-apply the cozystack parameterization
#      sed patches in case they were lost during the hand-merge.
#
# GNU sed is required — on macOS run under a Linux container or via gsed.
update:
	@test -f $(CR_FILE) || { echo "ERROR: $(CR_FILE) not found — drop a hand-merged upstream CR there first" >&2; exit 1; }
	@# Idempotent — each sed only inserts if the matching cozystack patch
	@# isn't already present in the file.
	grep -q '^  namespace: cozy-kubevirt$$' $(CR_FILE) || sed -i 's/namespace: kubevirt/namespace: cozy-kubevirt/g' $(CR_FILE)
	grep -q '{{- if .Values.cpuAllocationRatio }}' $(CR_FILE) || sed -i '/^    developerConfiguration:$$/a\      {{- if .Values.cpuAllocationRatio }}\n      cpuAllocationRatio: {{ .Values.cpuAllocationRatio }}\n      {{- end }}' $(CR_FILE)
	grep -q '{{- range .Values.extraFeatureGates }}' $(CR_FILE) || sed -i '/^      - VMExport$$/a\      {{- range .Values.extraFeatureGates }}\n      - {{ . }}\n      {{- end }}' $(CR_FILE)
	@# The insert carries the block's comment lines too, so a hand-merge that
	@# drops them gets them back. Its skip reads $$staticDisabledFeatureGates from
	@# the guard header above the document, which no sed here restores; the tail
	@# below greps for that header line instead.
	grep -q '{{- range .Values.disabledFeatureGates }}' $(CR_FILE) || sed -i '/^      featureGates:$$/i\      # Template is Beta and on by default since 1.9; it makes virt-operator\n      # deploy the virt-template aggregated API server, which nothing here uses.\n      # ExternalNetResourceInjection hands NAD resourceName requests to an\n      # injector the platform does not deploy; off keeps virt-controller doing it.\n      # ImageVolume needs the Kubernetes gate of the same name, which is off by\n      # default below 1.35 and strips the launcher'"'"'s image volumes in silence.\n      disabledFeatureGates:\n      - Template\n      - ExternalNetResourceInjection\n      {{- if semverCompare "<1.35-0" .Capabilities.KubeVersion.Version }}\n      - ImageVolume\n      {{- end }}\n      {{- range .Values.disabledFeatureGates }}\n      {{- if not (has . $$staticDisabledFeatureGates) }}\n      - {{ . }}\n      {{- end }}\n      {{- end }}' $(CR_FILE)
	@# permittedHostDevices and mediatedDevicesConfiguration are guarded
	@# independently: a hand-merge may drop one while keeping the other, and
	@# each must self-heal on its own. A single guard over both would skip the
	@# second insert whenever the first is still present, then trip the
	@# sanity-check tail below.
	grep -q '{{- with .Values.permittedHostDevices }}' $(CR_FILE) || sed -i '/^    evictionStrategy:/i\    {{- with .Values.permittedHostDevices }}\n    permittedHostDevices:\n      {{- toYaml . | nindent 6 }}\n    {{- end }}' $(CR_FILE)
	grep -q '{{- with .Values.mediatedDevicesConfiguration }}' $(CR_FILE) || sed -i '/^    evictionStrategy:/i\    {{- with .Values.mediatedDevicesConfiguration }}\n    mediatedDevicesConfiguration:\n      {{- toYaml . | nindent 6 }}\n    {{- end }}' $(CR_FILE)
	@# Sanity-check: every cozystack patch MUST end up in the file. If the
	@# hand-merge step moved or renamed an anchor (developerConfiguration:,
	@# - VMExport, featureGates:, evictionStrategy:) the sed silently no-ops and the chart
	@# ships without the parameterization. Each message names the directive
	@# that failed to land and the anchor its insert keys off, so the operator
	@# knows which line to restore before re-running.
	@#
	@# disabledFeatureGates is checked marker by marker rather than by its range
	@# directive alone. Every insert above is guarded by one directive it also
	@# inserts, so a hand-merge that keeps the directive and drops a sibling line
	@# leaves the guard satisfied, the insert skipped and the render green. That
	@# shape covers the directive and nothing else. What earns a grep per marker
	@# here are the three static entries: they are the only lines these inserts
	@# add that are CR content in their own right rather than a wrapper around a
	@# value, so they render on every install the cluster version selects them
	@# for, and their absence cannot surface as a value that stopped taking
	@# effect — the CR simply goes back to deploying virt-template, to letting
	@# KubeVirt skip NAD resource injection, and to image volumes an apiserver
	@# below 1.35 discards. The payload lines of the blocks above are exposed the
	@# same way; covering them is a wider change than this. `make update` cannot
	@# repair a partial block — the directive guard is what stops the insert — so
	@# name the missing marker and let the operator restore it.
	@grep -q '{{- if .Values.cpuAllocationRatio }}' $(CR_FILE) || { echo "ERROR: directive '{{- if .Values.cpuAllocationRatio }}' not inserted — restore the 'developerConfiguration:' anchor in $(CR_FILE) and re-run" >&2; exit 1; }
	@grep -q '{{- range .Values.extraFeatureGates }}' $(CR_FILE) || { echo "ERROR: directive '{{- range .Values.extraFeatureGates }}' not inserted — restore the '- VMExport' anchor in $(CR_FILE) and re-run" >&2; exit 1; }
	@grep -q '{{- range .Values.disabledFeatureGates }}' $(CR_FILE) || { echo "ERROR: directive '{{- range .Values.disabledFeatureGates }}' not inserted — restore the 'featureGates:' anchor in $(CR_FILE) and re-run" >&2; exit 1; }
	@grep -q '^      disabledFeatureGates:$$' $(CR_FILE) || { echo "ERROR: marker '      disabledFeatureGates:' missing while '{{- range .Values.disabledFeatureGates }}' is present — the insert was skipped over a partial block. Delete the whole disabledFeatureGates block from $(CR_FILE) and re-run to rebuild it" >&2; exit 1; }
	@grep -q '^      - Template$$' $(CR_FILE) || { echo "ERROR: marker '      - Template' missing while '{{- range .Values.disabledFeatureGates }}' is present — the insert was skipped over a partial block and the CR would deploy virt-template. Delete the whole disabledFeatureGates block from $(CR_FILE) and re-run to rebuild it" >&2; exit 1; }
	@grep -q '^      - ExternalNetResourceInjection$$' $(CR_FILE) || { echo "ERROR: marker '      - ExternalNetResourceInjection' missing while '{{- range .Values.disabledFeatureGates }}' is present — the insert was skipped over a partial block and virt-controller would stop injecting NAD resourceName requests. Delete the whole disabledFeatureGates block from $(CR_FILE) and re-run to rebuild it" >&2; exit 1; }
	@grep -q '^      - ImageVolume$$' $(CR_FILE) || { echo "ERROR: marker '      - ImageVolume' missing while '{{- range .Values.disabledFeatureGates }}' is present — the insert was skipped over a partial block and a management cluster below Kubernetes 1.35 would lose the launcher's image volumes. Delete the whole disabledFeatureGates block from $(CR_FILE) and re-run to rebuild it" >&2; exit 1; }
	@# The block's skip reads $$staticDisabledFeatureGates from the guard header,
	@# which no insert here restores. Losing the header fails the render rather
	@# than dropping the skip, but catching a hand-merge is this tail's job.
	@grep -q '{{- $$staticDisabledFeatureGates := ' $(CR_FILE) || { echo "ERROR: marker '{{- \$$staticDisabledFeatureGates := ' missing from the guard header — the disabledFeatureGates block below references it and the render would fail. Restore the header block at the top of $(CR_FILE)" >&2; exit 1; }
	@grep -q '{{- with .Values.permittedHostDevices }}' $(CR_FILE) || { echo "ERROR: directive '{{- with .Values.permittedHostDevices }}' not inserted — restore the 'evictionStrategy:' anchor in $(CR_FILE) and re-run" >&2; exit 1; }
	@grep -q '{{- with .Values.mediatedDevicesConfiguration }}' $(CR_FILE) || { echo "ERROR: directive '{{- with .Values.mediatedDevicesConfiguration }}' not inserted — restore the 'evictionStrategy:' anchor in $(CR_FILE) and re-run" >&2; exit 1; }
