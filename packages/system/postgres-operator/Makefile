export NAME=postgres-operator
export NAMESPACE=cozy-$(NAME)

include ../../../hack/package.mk

test:
	helm unittest .

update:
	rm -rf charts
	helm repo add cnpg https://cloudnative-pg.github.io/charts
	helm repo update cnpg
	helm pull cnpg/cloudnative-pg --untar --untardir charts --version 0.27.1
	rm -rf charts/cloudnative-pg/charts
	patch --no-backup-if-mismatch -p4 < patches/cloudnative-pg-1.28.2.patch
	helm pull cnpg/plugin-barman-cloud --untar --untardir charts --version 0.7.0
	rm -rf charts/plugin-barman-cloud/charts
