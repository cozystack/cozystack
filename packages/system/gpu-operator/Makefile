export NAME=gpu-operator
export NAMESPACE=cozy-$(NAME)

include ../../../hack/common-envs.mk
include ../../../hack/package.mk

# hack/helm-unit-tests.sh discovers work by probing for this target, so a
# package without one is skipped in silence however many suites sit under
# tests/. Phony because hack/package.mk's .PHONY line lists only the targets
# that file defines, so a path named `test` here would make the target up to
# date and skip the recipe.
.PHONY: test
test:
	helm unittest .

update:
	rm -rf charts
	helm repo add nvidia https://helm.ngc.nvidia.com/nvidia
	helm repo update nvidia
	helm pull nvidia/gpu-operator --untar --untardir charts --version v26.3.1
