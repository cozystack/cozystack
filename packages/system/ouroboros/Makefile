export NAME=ouroboros
export NAMESPACE=cozy-$(NAME)

include ../../../hack/common-envs.mk
include ../../../hack/package.mk

# Pin the upstream ouroboros chart by version AND OCI manifest digest.
#
# OUROBOROS_CHART_DIGEST below is the digest of the OCI **chart**
# manifest at ghcr.io/lexfrei/charts/ouroboros — it pins the contents
# of the helm chart artefact (templates, values.yaml, Chart.yaml).
# It is NOT the digest of the controller container image. The image
# digest lives in values.yaml under image.tag (`image.tag: "0.6.0@sha256:..."`)
# and pins ghcr.io/lexfrei/ouroboros (the runtime binary). Both
# digests have to land together at every chart bump — the chart
# version, the chart-manifest digest, and the controller-image
# digest all advance in lockstep with the upstream release.
#
# Bump together with the image tag+digest in values.yaml. The digest
# pin closes the supply-chain hole that a tag-only pull would leave —
# OCI registries can republish under the same tag, but the manifest
# digest is immutable. To bump: resolve the new chart-manifest digest
# with
#   crane manifest ghcr.io/lexfrei/charts/ouroboros:<new-tag> | sha256sum
# (or `oras manifest fetch ghcr.io/lexfrei/charts/ouroboros:<new-tag> | sha256sum`)
# and update both lines below; resolve the new image digest with
#   crane digest ghcr.io/lexfrei/ouroboros:<new-tag>
# and update values.yaml.
OUROBOROS_CHART_VERSION ?= 0.6.0
OUROBOROS_CHART_DIGEST ?= sha256:ce51fb5c273af36cfd1061f2010c4ee20aab7f539a0d13b7223f63d24188fe25

test:
	helm unittest .

update:
	rm -rf charts
	helm pull oci://ghcr.io/lexfrei/charts/ouroboros@$(OUROBOROS_CHART_DIGEST) --untar --untardir charts
	# Drop vendored upstream helm-unittest fixtures: the upstream
	# fixtures hard-code upstream defaults that conflict with the
	# cozystack values.yaml overrides (target Service FQDN, mode,
	# Gateway-API gating). The cozystack-side fixtures in tests/
	# are the only ones CI should run.
	rm -rf charts/ouroboros/tests
