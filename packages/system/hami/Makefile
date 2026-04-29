export NAME=hami
export NAMESPACE=cozy-$(NAME)

include ../../../hack/common-envs.mk
include ../../../hack/package.mk

# When bumping the HAMi version, run `make update` and then review
# the resulting diff in `charts/hami/`. The recipe below reproduces the
# top-level vendoring overrides automatically:
#
#   1. Removes the broken hami-dra subchart. Upstream's NVIDIA DRA driver
#      path requires kubelet DRA support that cozystack does not enable
#      and has no upstream fix tracked. See commit 3c5521e.
#   2. Empties Chart.yaml dependencies and drops Chart.lock so Helm does
#      not try to re-pull hami-dra at build time. See commit 2734dc0.
#   3. Strips dra/hami-dra/podSecurityPolicy blocks from the upstream
#      values.yaml since the corresponding code paths are gone. PSP is
#      removed from Kubernetes 1.25+ and is unused by cozystack.
#
# Template-level patches are NOT reproduced automatically:
#
#   * Scheduler templates have `{{- if .Values.dra.enabled }}` blocks
#     that need to be removed because the dra value is gone (commit
#     2734dc0 stripped them).
#   * device-plugin/monitorservice.yaml uses `indent` with leading
#     whitespace; it must be rewritten to `nindent` for the labels block
#     to render correctly when devicePlugin.service.labels is set
#     (commit 3685254).
#
# After `make update`, run `git diff -- charts/hami/templates/` and
# replay these template patches against the new upstream version, then
# verify with `helm unittest`. If upstream restructured the affected
# files, the patches may need to be redesigned rather than reapplied.

update:
	rm -rf charts
	helm repo add hami-charts https://project-hami.github.io/HAMi/
	helm repo update hami-charts
	helm pull hami-charts/hami --untar --untardir charts
	rm -rf charts/hami/charts/hami-dra
	yq --inplace '.dependencies = []' charts/hami/Chart.yaml
	rm -f charts/hami/Chart.lock
	yq --inplace 'del(.dra) | del(.["hami-dra"]) | del(.podSecurityPolicy)' charts/hami/values.yaml
