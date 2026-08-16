export NAME=cloudflare-tunnel-gateway-controller
export NAMESPACE=cozy-$(NAME)

include ../../../hack/package.mk

# The upstream chart is pinned by version AND OCI manifest digest, and the
# two images it runs are pinned by tag@digest in values.yaml. Registries can
# republish a tag; the digests cannot move. Bump all four together:
#   crane digest ghcr.io/lexfrei/charts/cloudflare-tunnel-gateway-controller:<tag>
#   crane digest ghcr.io/lexfrei/cloudflare-tunnel-gateway-controller:<tag>
#   crane digest ghcr.io/lexfrei/cloudflare-tunnel-gateway-controller-proxy:<tag>
CHART_VERSION ?= 3.4.0
CHART_DIGEST ?= sha256:56cb2411b7639640f23cbc4ce8ad2f4822deaca6c31f9221651ffcb435511b6a

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
