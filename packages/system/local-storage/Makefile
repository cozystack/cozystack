export NAME=local-storage
export NAMESPACE=cozy-$(NAME)

include ../../../hack/common-envs.mk
include ../../../hack/package.mk

update:
	rm -rf charts
	helm repo add openebs-localpv https://openebs.github.io/dynamic-localpv-provisioner
	helm repo update openebs-localpv
	helm pull openebs-localpv/localpv-provisioner --untar --untardir charts --version 4.5.1
