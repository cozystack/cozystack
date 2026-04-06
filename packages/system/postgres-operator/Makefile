export NAME=postgres-operator
export NAMESPACE=cozy-$(NAME)

include ../../../hack/package.mk

update:
	rm -rf charts
	helm repo add cnpg https://cloudnative-pg.github.io/charts
	helm repo update cnpg
	helm pull cnpg/cloudnative-pg --untar --untardir charts --version 0.26.1
	rm -rf charts/cloudnative-pg/charts

update-image-catalogs:
	@echo "Downloading PostgreSQL image catalogs from CNPG artifacts..."
	@curl -sSL https://raw.githubusercontent.com/cloudnative-pg/artifacts/main/image-catalogs/catalog-standard-bookworm.yaml \
		| sed 's/postgresql-standard-bookworm/postgresql-bookworm/' \
		| awk '/major: 18/,/image:/ {next} {print}' \
		> templates/image-catalog-bookworm.yaml
	@curl -sSL https://raw.githubusercontent.com/cloudnative-pg/artifacts/main/image-catalogs/catalog-standard-trixie.yaml \
		| sed 's/postgresql-standard-trixie/postgresql-trixie/' \
		| awk '/major: 18/,/image:/ {next} {print}' \
		> templates/image-catalog-trixie.yaml
	@echo "Updated image catalogs (PostgreSQL 13-17, excluded v18)"
