export NAME=kuberture
export NAMESPACE=cozy-$(NAME)

include ../../../hack/package.mk

# Pin the upstream kuberture chart by version AND OCI manifest digest.
#
# KUBERTURE_CHART_DIGEST below is the digest of the OCI **chart**
# manifest at ghcr.io/lexfrei/kuberture/charts/kuberture — it pins
# the contents of the helm chart artefact (templates, values.yaml,
# Chart.yaml). It is NOT the digest of the controller container
# image. The controller image digest lives in the shim
# values.yaml at packages/system/kuberture/values.yaml under
# kuberture.image.tag (pinning ghcr.io/lexfrei/kuberture); both
# digests advance in lockstep with the upstream release.
#
# Bump together with the image tag+digest in values.yaml. The
# digest pin closes the supply-chain hole that a tag-only pull
# would leave — OCI registries can republish under the same tag,
# but the manifest digest is immutable. To bump:
#   crane manifest ghcr.io/lexfrei/kuberture/charts/kuberture:<new-tag> | sha256sum
#   crane digest ghcr.io/lexfrei/kuberture:<new-tag>
# (or `oras manifest fetch ...`) and update KUBERTURE_CHART_DIGEST
# below plus kuberture.image.tag in values.yaml.
KUBERTURE_CHART_VERSION ?= 0.1.1
KUBERTURE_CHART_DIGEST ?= sha256:1356710b05fcf2dff93d3c723b923b653c74ca2a69360cef0dc1fcd9f595fa95

test:
	helm unittest .

update:
	rm -rf charts
	mkdir -p charts
	# Pull by digest, then verify Chart.yaml records the expected
	# version. A digest mismatch (someone bumped DIGEST without
	# bumping VERSION, or vice versa) trips the assert and aborts.
	helm pull oci://ghcr.io/lexfrei/kuberture/charts/kuberture@$(KUBERTURE_CHART_DIGEST) --untar --untardir charts
	@actual_version=$$(awk '/^version:/ {print $$2}' charts/kuberture/Chart.yaml | tr -d '"'); \
	if [ "$$actual_version" != "$(KUBERTURE_CHART_VERSION)" ]; then \
	  echo "ERROR: pulled chart Chart.yaml version=$$actual_version but Makefile KUBERTURE_CHART_VERSION=$(KUBERTURE_CHART_VERSION). Bump both in lockstep."; \
	  exit 1; \
	fi
	# Drop vendored upstream helm-unittest fixtures: cozystack ships
	# its own unittest coverage at tests/ targeting the shim values
	# overrides, and the upstream fixtures are not reviewed code in
	# this repo. Until the kuberture chart gains a .helmignore the
	# fixtures ride along in the OCI tarball.
	rm -rf charts/kuberture/tests
