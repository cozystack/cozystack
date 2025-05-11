GRAFANA_TAG = $(shell awk '$$1 == "version:" {print $$2}' Chart.yaml)

NAME=monitoring

include ../../../scripts/common-envs.mk
include ../../../scripts/package.mk

generate:
	readme-generator -v values.yaml -s values.schema.json.tmp -r README.md
	cat values.schema.json.tmp | \
		jq '.properties.metricsStorages.items.type = "object" | .properties.logsStorages.items.type = "object"' \
		> values.schema.json
	rm -f values.schema.json.tmp

image:
	docker buildx build --platform linux/amd64 images/grafana \
		--provenance false \
		--builder=$(BUILDER) \
		--platform=$(PLATFORM) \
		--tag $(REGISTRY)/grafana:$(call settag,$(GRAFANA_TAG)) \
		--cache-from type=registry,ref=$(REGISTRY)/grafana:latest \
		--cache-to type=inline \
		--metadata-file images/grafana.json \
		--push=$(PUSH) \
		--label "org.opencontainers.image.source=https://github.com/cozystack/cozystack" \
		--load=$(LOAD)
	echo "$(REGISTRY)/grafana:$(call settag,$(GRAFANA_TAG))@$$(yq e '."containerimage.digest"' images/grafana.json -o json -r)" \
		> images/grafana.tag
	rm -f images/grafana.json
