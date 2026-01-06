export NAME=grafana-operator
export NAMESPACE=cozy-grafana-operator

include ../../../scripts/common-envs.mk
include ../../../scripts/package.mk

update:
	rm -rf charts
	mkdir -p charts
	curl -sSL https://github.com/grafana-operator/grafana-operator/archive/refs/heads/master.tar.gz | \
	tar xzvf - --strip 3 -C charts grafana-operator-master/deploy/helm/grafana-operator

image:
	docker buildx build --file images/grafana-dashboards/Dockerfile ../../.. \
		--tag $(REGISTRY)/grafana-dashboards:$(call settag,$(TAG)) \
		--cache-from type=registry,ref=$(REGISTRY)/grafana-dashboards:latest \
		--cache-to type=inline \
		--metadata-file images/grafana-dashboards.json \
		$(BUILDX_ARGS)
	echo "$(REGISTRY)/grafana-dashboards:$(call settag,$(TAG))@$$(yq --exit-status '.["containerimage.digest"]' images/grafana-dashboards.json --output-format json --raw-output)" \
		> images/grafana-dashboards.tag
	rm -f images/grafana-dashboards.json
