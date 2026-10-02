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
	helm unittest .

# The vendoring ref in lllamnyp/cozyplane (a release tag once upstream cuts
# them; keep the three cozyplane packages on the same ref). Makefile and
# tests/ are Cozystack-side and survive the vendor, and so does the version
# stanza below: Cozystack owns the chart version, upstream keeps 0.0.0.
COZYPLANE_REF=65bade1c310c739edcf230af0a274086b1ac72f9
CHART_VERSION=0.3.29

update:
	find . -mindepth 1 -maxdepth 1 ! -name Makefile ! -name tests -exec rm -rf {} +
	curl -sSL https://github.com/lllamnyp/cozyplane/archive/$(COZYPLANE_REF).tar.gz | \
	tar -xzf - --strip-components=3 cozyplane-$(COZYPLANE_REF)/chart/cozyplane
	sed -i -e '/# Unused as a release coordinate/,/^version: 0.0.0/c\# Placeholder; the real version is stamped by the build/release pipeline.\nversion: $(CHART_VERSION)' Chart.yaml
