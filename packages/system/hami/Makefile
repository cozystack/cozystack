export NAME=hami
export NAMESPACE=kube-system

include ../../../scripts/common-envs.mk
include ../../../scripts/package.mk

update:
	rm -rf charts
	helm repo add hami https://project-hami.github.io/HAMi/
	helm repo update hami
	helm pull hami/hami --version 2.7.0 --untar --untardir charts
