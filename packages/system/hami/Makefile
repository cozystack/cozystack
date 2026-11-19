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
#   3. Strips the hami-dra values block (its subchart is removed and no
#      template references it). The top-level `dra:` and `podSecurityPolicy:`
#      blocks are KEPT (both default to `enabled: false`): since 2.9.0 the
#      scheduler/device-plugin templates gate on `.Values.dra.enabled` and
#      `.Values.podSecurityPolicy.enabled` directly, so keeping them disabled
#      cleanly turns off the DRA and PSP paths without nil-pointer renders or
#      having to strip the conditionals from ~27 templates.
#
# Template-level patches are NOT reproduced automatically:
#
#   * device-plugin/monitorservice.yaml and scheduler/service.yaml use
#     `indent` with leading whitespace on the custom Service labels block;
#     it must be rewritten to `{{- ... | nindent 4 }}` so the labels render
#     as valid map entries when devicePlugin.service.labels /
#     scheduler.service.labels are set (commit 3685254; both Services share
#     the same upstream defect).
#
# After `make update`, run `git diff -- charts/hami/templates/` and
# replay these template patches against the new upstream version, then
# verify with `helm unittest`. If upstream restructured the affected
# files, the patches may need to be redesigned rather than reapplied.

test:
	helm unittest .

update:
	rm -rf charts
	helm repo add hami-charts https://project-hami.github.io/HAMi/
	helm repo update hami-charts
	helm pull hami-charts/hami --untar --untardir charts --version 2.9.0
	rm -rf charts/hami/charts/hami-dra
	yq --inplace '.dependencies = []' charts/hami/Chart.yaml
	rm -f charts/hami/Chart.lock
	yq --inplace 'del(.["hami-dra"])' charts/hami/values.yaml
