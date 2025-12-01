export NAME=gpu-operator
export NAMESPACE=cozy-$(NAME)

include ../../../hack/common-envs.mk
include ../../../hack/package.mk

update:
	rm -rf charts
	helm repo add nvidia https://helm.ngc.nvidia.com/nvidia
	helm repo update nvidia
	helm pull nvidia/gpu-operator --untar --untardir charts
