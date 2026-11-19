export NAME=cozyplane-cilium-crds
export NAMESPACE=cozy-system

include ../../../hack/package.mk

# Pinned to the Cilium release the CRDs were vendored from (byte-identical to
# the upstream files). Keep it in step with the cilium package's upstream
# version when bumping: these CRDs exist so stock charts' CiliumNetworkPolicy
# manifests apply on the cozyplane variant, and a schema older than what those
# charts emit would reject their objects.
CILIUM_VERSION=1.19.5

CRDS_URL=https://raw.githubusercontent.com/cilium/cilium/v$(CILIUM_VERSION)/pkg/k8s/apis/cilium.io/client/crds/v2

update:
	tmp=$$(mktemp -d .update.XXXXXX) && \
	{ curl -fsSL $(CRDS_URL)/ciliumnetworkpolicies.yaml -o "$$tmp/ciliumnetworkpolicies.yaml" && \
	  curl -fsSL $(CRDS_URL)/ciliumclusterwidenetworkpolicies.yaml -o "$$tmp/ciliumclusterwidenetworkpolicies.yaml" || \
	  { rm -rf "$$tmp"; exit 1; }; } && \
	mv "$$tmp"/*.yaml templates/ && rmdir "$$tmp"
