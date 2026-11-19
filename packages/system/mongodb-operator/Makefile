export NAME=mongodb-operator
export NAMESPACE=cozy-$(NAME)

include ../../../hack/package.mk

update:
	rm -rf charts
	helm repo add percona https://percona.github.io/percona-helm-charts
	helm repo update percona
	helm pull percona/psmdb-operator --untar --untardir charts
	rm -rf charts/psmdb-operator/charts
