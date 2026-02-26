export NAME=opensearch-operator
export NAMESPACE=cozy-$(NAME)

include ../../../hack/package.mk

update:
	rm -rf charts
	helm repo add opensearch-operator https://opensearch-project.github.io/opensearch-k8s-operator/
	helm repo update opensearch-operator
	helm pull opensearch-operator/opensearch-operator --version 2.8.0 --untar --untardir charts
