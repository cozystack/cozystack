export NAME=cozyplane-kpr
export NAMESPACE=cozy-cozyplane

include ../../../hack/common-envs.mk
include ../../../hack/package.mk

# cozyplane-kpr builds and publishes its own container image from
# github.com/lllamnyp/cozyplane (kpr/); this package only vendors the Helm chart
# and references the published image in values.yaml. No image build target here.

test:
	helm lint .
	helm unittest .

# The vendoring ref in lllamnyp/cozyplane (a release tag once upstream cuts
# them; keep the three cozyplane packages on the same ref). Makefile and
# tests/ are Cozystack-side and survive the vendor, and so does the version
# stanza below: Cozystack owns the chart version, upstream keeps 0.0.0.
COZYPLANE_REF=2608ed75b4c5351a5e418d9e4a61b8575ed90565
CHART_VERSION=0.1.0

update:
	find . -mindepth 1 -maxdepth 1 ! -name Makefile ! -name tests -exec rm -rf {} +
	curl -sSL https://github.com/lllamnyp/cozyplane/archive/$(COZYPLANE_REF).tar.gz | \
	tar -xzf - --strip-components=3 cozyplane-$(COZYPLANE_REF)/chart/cozyplane-kpr
	sed -i -e '/# Unused as a release coordinate/,/^version: 0.0.0/c\# Placeholder; the real version is stamped by the build/release pipeline.\nversion: $(CHART_VERSION)' Chart.yaml
