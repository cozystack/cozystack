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
		| yq '.metadata.name = "postgresql-bookworm" | del(.spec.images[] | select(.major == 13 or .major == 18))' \
		> templates/image-catalog-bookworm.yaml
	@curl -sSL https://raw.githubusercontent.com/cloudnative-pg/artifacts/main/image-catalogs/catalog-standard-trixie.yaml \
		| yq '.metadata.name = "postgresql-trixie" | del(.spec.images[] | select(.major == 13 or .major == 18))' \
		> templates/image-catalog-trixie.yaml
	@echo "Updated image catalogs (PostgreSQL 14-17, excluded v13 EOL and v18)"
