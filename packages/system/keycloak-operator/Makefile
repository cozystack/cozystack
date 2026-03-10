export NAME=keycloak-operator
export NAMESPACE=cozy-keycloak

include ../../../hack/common-envs.mk
include ../../../hack/package.mk

image:
	docker buildx build images/keycloak-operator \
		--tag $(REGISTRY)/keycloak-operator:$(call settag,$(TAG)) \
		--cache-from type=registry,ref=$(REGISTRY)/keycloak-operator:latest \
		--cache-to   type=inline \
		--metadata-file images/keycloak-operator.json \
		$(BUILDX_ARGS)
	TAG="$(call settag,$(TAG))@$$(yq e '."containerimage.digest"' images/keycloak-operator.json -r)" \
		yq -i '.keycloak-operator.image.tag = strenv(TAG)' values.yaml
	rm -f images/keycloak-operator.json

update:
	rm -rf charts
	helm repo add epamedp https://epam.github.io/edp-helm-charts/stable
	helm repo update epamedp
	helm pull epamedp/keycloak-operator --untar --untardir charts
