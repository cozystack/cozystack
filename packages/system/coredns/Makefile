export NAME=coredns
export NAMESPACE=kube-system

include ../../../scripts/package.mk

update:
	rm -rf charts
	helm repo add cozy-coredns https://coredns.github.io/helm
	helm repo update cozy-coredns
	helm pull cozy-coredns/coredns --untar --untardir charts
