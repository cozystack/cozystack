export NAME=velero
export NAMESPACE=cozy-$(NAME)

include ../../../scripts/common-envs.mk
include ../../../scripts/package.mk

update:
	rm -rf charts
	# Velero
	helm repo add tanzu https://vmware-tanzu.github.io/helm-charts
	helm repo update tanzu
	helm pull tanzu/velero --untar --untardir charts
	tag=$$(yq .image.tag charts/velero/values.yaml) && \
	sed -i "/ARG VERSION/ s|=.*|=$${tag}|g" images/velero/Dockerfile

image:
	docker buildx build images/velero \
		--tag $(REGISTRY)/velero:$(call settag,$(TAG)) \
		--cache-from type=registry,ref=$(REGISTRY)/velero:latest \
		--cache-to type=inline \
		--metadata-file images/velero.json \
		$(BUILDX_ARGS)
	REPOSITORY="$(REGISTRY)/velero" \
		yq -i '.velero.image.repository = strenv(REPOSITORY)' values.yaml
	TAG=$(TAG)@$$(yq e '."containerimage.digest"' images/velero.json -o json -r) \
		yq -i '.velero.image.tag = strenv(TAG)' values.yaml
	rm -f images/velero.json
