export NAME=kafka-operator
export NAMESPACE=cozy-$(NAME)

include ../../../hack/package.mk

test:
	helm unittest .

update:
	rm -rf charts
	helm pull oci://quay.io/strimzi-helm/strimzi-kafka-operator --untar --untardir charts --version 1.2.0
