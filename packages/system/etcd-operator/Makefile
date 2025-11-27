export NAME=etcd-operator
export NAMESPACE=cozy-${NAME}

include ../../../hack/package.mk

update:
	rm -rf charts
	helm pull oci://ghcr.io/aenix-io/charts/etcd-operator --untar --untardir charts
