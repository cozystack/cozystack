export NAME=cozyplane-kpr
export NAMESPACE=cozy-cozyplane

include ../../../hack/common-envs.mk
include ../../../hack/package.mk

# cozyplane-kpr builds and publishes its own container image from
# github.com/lllamnyp/cozyplane (kpr/); this package only vendors the Helm chart
# and references the published image in values.yaml. No image build target here.

test:
	helm lint .
