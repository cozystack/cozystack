export NAME=cloudflare-tunnel-gateway-controller
export NAMESPACE=cozy-$(NAME)

include ../../../hack/package.mk

# The upstream chart is pinned by version AND OCI manifest digest, and the
# two images it runs are pinned by tag@digest in values.yaml. Registries can
# republish a tag; the digests cannot move. All four are tracked by the
# renovate custom manager keyed on the marker comments, so a release upstream
# arrives as a PR rather than aging in place; the four have to move together,
# and the version check in `update` below is what refuses a half-done bump.
# renovate: datasource=docker depName=ghcr.io/lexfrei/charts/cloudflare-tunnel-gateway-controller
CHART_VERSION ?= 3.4.1
CHART_DIGEST ?= sha256:398a47876d172b86ecf3059586bb226f4af7701cdd0f17d2330f7f5182bcf642

test:
	helm unittest .

update:
	rm -rf charts
	mkdir -p charts
	helm pull oci://ghcr.io/lexfrei/charts/cloudflare-tunnel-gateway-controller@$(CHART_DIGEST) --untar --untardir charts
	# helm pull by digest also leaves an empty charts/<name>@sha256:<digest> directory behind
	rmdir charts/*@sha256:* 2>/dev/null || true
	@actual_version=$$(awk '/^version:/ {print $$2}' charts/cloudflare-tunnel-gateway-controller/Chart.yaml | tr -d '"'); \
	if [ "$$actual_version" != "$(CHART_VERSION)" ]; then \
	  echo "ERROR: pulled chart version=$$actual_version but Makefile CHART_VERSION=$(CHART_VERSION). Bump both in lockstep."; \
	  exit 1; \
	fi
