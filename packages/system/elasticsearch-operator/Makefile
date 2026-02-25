export NAME=elasticsearch-operator
export NAMESPACE=cozy-$(NAME)

include ../../../hack/package.mk

update:
	rm -rf charts
	helm repo add elastic https://helm.elastic.co
	helm repo update elastic
	helm pull elastic/eck-operator --untar --untardir charts
