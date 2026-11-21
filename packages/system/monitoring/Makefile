NAME=monitoring
NAMESPACE=cozy-$(NAME)

include ../../../hack/common-envs.mk
include ../../../hack/package.mk

test:
	helm unittest .

# Packages artifacts omit symlinks, so the tenant VMAlert rules are a copy of
# the operator's rule file rather than a link to it.
generate:
	cp ../postgres-operator/alerts/cnpg-default-alerts.yaml rules/cnpg-default-alerts.yaml

.PHONY: generate image image-otelcol-tracing

# The collector is versioned by its upstream release, pinned in the
# Containerfile; bump COZYSTACK_REV for a respin on the same release (a new
# component in builder-config.yaml, a Go toolchain bump).
OTELCOL_VERSION = $(shell awk -F= '/^ARG VERSION=/{print $$2}' images/otelcol-tracing/Containerfile)
COZYSTACK_REV = 0
COMPONENT_VERSION = v$(OTELCOL_VERSION)-cozystack.$(COZYSTACK_REV)

image: image-otelcol-tracing

image-otelcol-tracing:
	docker buildx build images/otelcol-tracing \
		--file images/otelcol-tracing/Containerfile \
		$(call image-tags,otelcol-tracing,$(COMPONENT_VERSION)) \
		$(call cache-args,otelcol-tracing) \
		--metadata-file images/otelcol-tracing.json \
		$(BUILDX_ARGS)
	IMAGE="$(REGISTRY)/otelcol-tracing:$(COMPONENT_VERSION)@$$(yq e '."containerimage.digest"' images/otelcol-tracing.json -o json -r)" \
		yq -i '.tracingCollector.image = strenv(IMAGE)' values.yaml
	rm -f images/otelcol-tracing.json
