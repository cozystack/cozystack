export NAME=hami
export NAMESPACE=cozy-$(NAME)

include ../../../hack/common-envs.mk
include ../../../hack/package.mk

update:
	rm -rf charts
	helm repo add hami-charts https://project-hami.github.io/HAMi/
	helm repo update hami-charts
	helm pull hami-charts/hami --untar --untardir charts
