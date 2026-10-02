export NAME=cozyplane-cilium-crds
export NAMESPACE=cozy-system

include ../../../hack/package.mk

# Pinned to the Cilium release the CRDs were vendored from (byte-identical to
# the upstream files). Keep it in step with the cilium package's upstream
# version when bumping: these CRDs exist so stock charts' CiliumNetworkPolicy
# manifests apply on the cozyplane variant, and a schema older than what those
# charts emit would reject their objects.
CILIUM_VERSION=1.19.5

update:
	curl -sSL https://raw.githubusercontent.com/cilium/cilium/v$(CILIUM_VERSION)/pkg/k8s/apis/cilium.io/client/crds/v2/ciliumnetworkpolicies.yaml -o templates/ciliumnetworkpolicies.yaml
	curl -sSL https://raw.githubusercontent.com/cilium/cilium/v$(CILIUM_VERSION)/pkg/k8s/apis/cilium.io/client/crds/v2/ciliumclusterwidenetworkpolicies.yaml -o templates/ciliumclusterwidenetworkpolicies.yaml
