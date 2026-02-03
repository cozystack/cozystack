export NAME=clickhouse-operator
export NAMESPACE=cozy-clickhouse-operator

include ../../../hack/package.mk

update:
	rm -rf charts
	helm repo add clickhouse-operator https://docs.altinity.com/clickhouse-operator/
	helm repo update clickhouse-operator
	helm pull clickhouse-operator/altinity-clickhouse-operator --untar --untardir charts
