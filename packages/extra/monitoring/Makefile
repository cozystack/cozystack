GRAFANA_TAG = $(shell awk '$$1 == "version:" {print $$2}' Chart.yaml)

NAME=monitoring

include ../../../hack/common-envs.mk
include ../../../hack/package.mk

.PHONY: test

generate:
	cozyvalues-gen -v values.yaml -s values.schema.json -r README.md
	../../../hack/update-crd.sh

test:
	helm unittest .

image:
	docker buildx build images/grafana \
		$(call image-tags,grafana,$(GRAFANA_TAG)) \
		$(call cache-args,grafana) \
		--metadata-file images/grafana.json \
		$(BUILDX_ARGS)
	echo "$(REGISTRY)/grafana:$(IMAGE_TAG)@$$(yq e '."containerimage.digest"' images/grafana.json -o json -r)" \
		> images/grafana.tag
	rm -f images/grafana.json
