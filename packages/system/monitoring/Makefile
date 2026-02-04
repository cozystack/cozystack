GRAFANA_TAG = $(shell awk '$$1 == "version:" {print $$2}' Chart.yaml)

NAME=monitoring
NAMESPACE=cozy-$(NAME)

include ../../../hack/common-envs.mk
include ../../../hack/package.mk

image:
	docker buildx build images/grafana \
		--tag $(REGISTRY)/grafana:$(call settag,$(GRAFANA_TAG)) \
		--cache-from type=registry,ref=$(REGISTRY)/grafana:latest \
		--cache-to type=inline \
		--metadata-file images/grafana.json \
		$(BUILDX_ARGS)
	echo "$(REGISTRY)/grafana:$(call settag,$(GRAFANA_TAG))@$$(yq e '."containerimage.digest"' images/grafana.json -o json -r)" \
		> images/grafana.tag
	rm -f images/grafana.json
