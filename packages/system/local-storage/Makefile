export NAME=local-storage
export NAMESPACE=cozy-$(NAME)

include ../../../hack/common-envs.mk
include ../../../hack/package.mk

LOCALPV_CHART_VERSION = 4.5.1
HELPER_IMAGE_VERSION = 4.5.0

.PHONY: update update-image-pins test

test:
	helm unittest .
	python3 tests/rbac_test.py

update:
	@test "$$(yq '.dependencies[] | select(.name == "localpv-provisioner") | .version' Chart.yaml)" = "$(LOCALPV_CHART_VERSION)"
	rm -rf charts
	helm pull localpv-provisioner --repo https://openebs.github.io/dynamic-localpv-provisioner --untar --untardir charts --version $(LOCALPV_CHART_VERSION)
	patch -p1 < patches/rbac.diff
	$(MAKE) update-image-pins

# Keep third-party pins visible in umbrella values, without rebuilding or mirroring them.
update-image-pins:
	@digest=$$(skopeo inspect --format '{{.Digest}}' docker://docker.io/openebs/provisioner-localpv:$(LOCALPV_CHART_VERSION)) && \
	  IMAGE_TAG="$(LOCALPV_CHART_VERSION)@$$digest" yq -i '.localpv-provisioner.localpv.image.tag = strenv(IMAGE_TAG)' values.yaml
	@digest=$$(skopeo inspect --format '{{.Digest}}' docker://docker.io/openebs/linux-utils:$(HELPER_IMAGE_VERSION)) && \
	  IMAGE_TAG="$(HELPER_IMAGE_VERSION)@$$digest" yq -i '.localpv-provisioner.helperPod.image.tag = strenv(IMAGE_TAG)' values.yaml
