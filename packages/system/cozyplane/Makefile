export NAME=cozyplane
export NAMESPACE=cozy-$(NAME)

include ../../../hack/common-envs.mk
include ../../../hack/package.mk

# cozyplane builds and publishes its own container image from
# github.com/lllamnyp/cozyplane; this package only vendors the Helm chart (kept
# in that repo's chart/cozyplane) and references the published image in
# values.yaml. There is no image build target here.

test:
	helm lint .
