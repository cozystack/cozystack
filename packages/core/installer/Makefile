NAME=installer
NAMESPACE=cozy-system

TALOS_VERSION=$(shell awk '/^version:/ {print $$2}' images/talos/profiles/installer.yaml)

include ../../../scripts/common-envs.mk

pre-checks:
	../../../hack/pre-checks.sh

show:
	cozypkg show -n $(NAMESPACE) $(NAME) --plain

apply:
	cozypkg show -n $(NAMESPACE) $(NAME) --plain | kubectl apply -f-

diff:
	cozypkg show -n $(NAMESPACE) $(NAME) --plain | kubectl diff -f -

image: image-packages

image-packages:
	mkdir -p ../../../_out/assets images
	flux push artifact \
		oci://$(REGISTRY)/platform-packages:$(call settag,$(TAG)) \
		--path=../../../packages \
		--source=https://github.com/cozystack/cozystack \
		--revision="$$(git describe --tags):$$(git rev-parse HEAD)" \
	2>&1 | tee images/cozystack-packages.log
	DIGEST=$$(awk -F@ '/artifact successfully pushed/ {print $$2}' images/cozystack-packages.log; rm -f images/cozystack-packages.log) \
		yq -i '.packagesDigest = strenv(DIGEST)' values.yaml
