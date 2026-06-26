export NAME=capi-providers-bootstrap
export NAMESPACE=cozy-cluster-api

include ../../../hack/package.mk

.PHONY: update

# Upstream Talos bootstrap provider (CABPT) release pinned by tag. Keep
# in lockstep with templates/providers.yaml (BootstrapProvider spec.version)
# and templates/configmaps.yaml (ConfigMap name) — clusterctl matches the
# bundle by tag.
CABPT_UPSTREAM_VERSION = v0.6.12

# Refreshes files/bootstrap-components-talos.yaml + files/components-talos.gz
# from upstream CABPT release artifacts at CABPT_UPSTREAM_VERSION with
# patches/ applied (the cozystack-side rename of the default ServiceAccount
# to cabpt-manager so capi-operator does not rewrite the SA namespace —
# see PR #2610 for the full rationale). Mirrors the symmetric update
# target in packages/system/capi-providers-cpprovider/Makefile.
#
# Run when bumping CABPT_UPSTREAM_VERSION or editing patches/. The
# metadata-talos.yaml file is checked in as-is from upstream and is not
# refreshed here (it changes far less often and a manual curl+diff is
# enough when CABPT cuts a new release series).
update:
	@command -v patch >/dev/null || { echo "patch(1) required"; exit 1; }
	rm -rf .update-tmp
	mkdir .update-tmp
	curl -sSL "https://github.com/siderolabs/cluster-api-bootstrap-provider-talos/releases/download/$(CABPT_UPSTREAM_VERSION)/bootstrap-components.yaml" \
		-o .update-tmp/bootstrap-components.yaml
	for p in patches/*.diff; do \
		[ -e "$$p" ] || continue; \
		(cd .update-tmp && patch bootstrap-components.yaml < ../$$p) || exit 1; \
	done
	mv .update-tmp/bootstrap-components.yaml files/bootstrap-components-talos.yaml
	gzip -nc files/bootstrap-components-talos.yaml > files/components-talos.gz
	rm -rf .update-tmp
