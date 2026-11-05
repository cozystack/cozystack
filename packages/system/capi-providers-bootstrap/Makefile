export NAME=capi-providers-bootstrap
export NAMESPACE=cozy-cluster-api

CAPI_VERSION=v1.13.7
CAPI_URL=https://github.com/kubernetes-sigs/cluster-api/releases/download/$(CAPI_VERSION)
# SHA-256 of the kubeadm bootstrap provider's two release assets at
# CAPI_VERSION. A release tag can be moved; these cannot. Keep CAPI_VERSION
# equal to the one in capi-providers-core/Makefile.
CAPI_SHA256_COMPONENTS=7ee336e138208949f9b772895ec35adb9f5694012d2a4d82c04f0106f0eb1f94
CAPI_SHA256_METADATA=20ffd682bfc50ab5193aa14ba55222ba390f9204ccf6a0dd2d52c4a3c4fb004f

include ../../../hack/package.mk

.PHONY: update components

# Upstream Talos bootstrap provider (CABPT) release pinned by tag. Keep
# in lockstep with templates/providers.yaml (BootstrapProvider spec.version)
# and templates/configmaps.yaml (ConfigMap name) — clusterctl matches the
# bundle by tag.
CABPT_UPSTREAM_VERSION = v0.6.13
CABPT_URL=https://github.com/siderolabs/cluster-api-bootstrap-provider-talos/releases/download/$(CABPT_UPSTREAM_VERSION)
# SHA-256 of the two CABPT release assets at CABPT_UPSTREAM_VERSION, as
# downloaded, before patches/ is applied.
CABPT_SHA256_COMPONENTS=1e721ad294bdd5c8994aa957017cb94ad6e0cfd4ba52a52b45ed456d93efd4f8
CABPT_SHA256_METADATA=f2069963ae63593a795a7d134bb35999e372fd36240219a539f123c9387507ed

# Refreshes files/bootstrap-components-talos.yaml + files/components-talos.gz
# from upstream CABPT release artifacts at CABPT_UPSTREAM_VERSION with
# patches/ applied (the cozystack-side rename of the default ServiceAccount
# to cabpt-manager so capi-operator does not rewrite the SA namespace —
# see PR #2610 for the full rationale). Mirrors the symmetric update
# target in packages/system/capi-providers-cpprovider/Makefile.
#
# Run when bumping CABPT_UPSTREAM_VERSION and the two sums, or when editing
# patches/. It also refreshes files/metadata-talos.yaml.
update:
	@command -v patch >/dev/null || { echo "patch(1) required"; exit 1; }
	rm -rf .update-tmp
	mkdir .update-tmp
	curl -fL -o .update-tmp/bootstrap-components.yaml $(CABPT_URL)/bootstrap-components.yaml
	curl -fL -o .update-tmp/metadata.yaml $(CABPT_URL)/metadata.yaml
	printf '%s  .update-tmp/bootstrap-components.yaml\n%s  .update-tmp/metadata.yaml\n' $(CABPT_SHA256_COMPONENTS) $(CABPT_SHA256_METADATA) | sha256sum -c -
	for p in patches/*.diff; do \
		[ -e "$$p" ] || continue; \
		(cd .update-tmp && patch bootstrap-components.yaml < ../$$p) || exit 1; \
	done
	mv .update-tmp/bootstrap-components.yaml files/bootstrap-components-talos.yaml
	mv .update-tmp/metadata.yaml files/metadata-talos.yaml
	gzip -9 -n -c files/bootstrap-components-talos.yaml > files/components-talos.gz
	rm -rf .update-tmp

# Refresh the vendored kubeadm bootstrap provider. Bump CAPI_VERSION and the two
# sums, run this, and update the kubeadm version in
# templates/{configmaps,providers}.yaml to match.
components:
	rm -rf .components-tmp
	mkdir .components-tmp
	curl -fL -o .components-tmp/bootstrap-components.yaml $(CAPI_URL)/bootstrap-components.yaml
	curl -fL -o .components-tmp/metadata.yaml $(CAPI_URL)/metadata.yaml
	printf '%s  .components-tmp/bootstrap-components.yaml\n%s  .components-tmp/metadata.yaml\n' $(CAPI_SHA256_COMPONENTS) $(CAPI_SHA256_METADATA) | sha256sum -c -
	mv .components-tmp/bootstrap-components.yaml files/bootstrap-components.yaml
	mv .components-tmp/metadata.yaml files/metadata.yaml
	gzip -9 -n -c files/bootstrap-components.yaml > files/components.gz
	rm -rf .components-tmp
