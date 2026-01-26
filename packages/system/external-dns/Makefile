export NAME=external-dns
export NAMESPACE=cozy-$(NAME)

include ../../../hack/package.mk

update:
	rm -rf charts
	helm repo add external-dns https://kubernetes-sigs.github.io/external-dns/
	helm repo update external-dns
	helm pull external-dns/external-dns --untar --untardir charts