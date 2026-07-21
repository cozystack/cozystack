export NAME=cozyplane-apiserver
export NAMESPACE=cozy-cozyplane

include ../../../hack/common-envs.mk
include ../../../hack/package.mk

# Vendored from github.com/lllamnyp/cozyplane (chart/cozyplane-apiserver),
# adapted for Cozystack's etcd-operator (etcd-operator.cozystack.io/v1alpha2,
# memory-backed). The image is built and published from that repo; there is no
# image build target here.

test:
	helm lint .
