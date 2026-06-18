export NAME=grafana-operator
export NAMESPACE=cozy-grafana-operator

include ../../../hack/common-envs.mk
include ../../../hack/package.mk

update:
	rm -rf charts
	mkdir -p charts
	curl -sSL https://github.com/grafana-operator/grafana-operator/archive/refs/heads/master.tar.gz | \
	tar xzvf - --strip 3 -C charts grafana-operator-master/deploy/helm/grafana-operator
	patch --no-backup-if-mismatch -p1 < patches/startupProbe.diff

image:
	docker buildx build --file images/grafana-dashboards/Dockerfile ../../.. \
		$(call image-tags,grafana-dashboards,v$(COZYSTACK_VERSION)) \
		$(call cache-args,grafana-dashboards) \
		--metadata-file images/grafana-dashboards.json \
		$(BUILDX_ARGS)
	echo "$(REGISTRY)/grafana-dashboards:$(IMAGE_TAG)@$$(yq --exit-status '.["containerimage.digest"]' images/grafana-dashboards.json --output-format json -r)" \
		> images/grafana-dashboards.tag
	rm -f images/grafana-dashboards.json
