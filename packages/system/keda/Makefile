export NAME=keda
export NAMESPACE=cozy-$(NAME)

include ../../../hack/package.mk

# Pin the upstream KEDA chart by version. Bump deliberately; the assert below
# aborts if the pulled Chart.yaml version drifts from the pin.
KEDA_CHART_VERSION ?= 2.20.2

test:
	helm unittest .

update:
	rm -rf charts
	helm repo add kedacore https://kedacore.github.io/charts
	helm repo update kedacore
	helm pull kedacore/keda --untar --untardir charts --version "$(KEDA_CHART_VERSION)"
	@actual_version=$$(awk '/^version:/ {print $$2}' charts/keda/Chart.yaml | tr -d '"'); \
	if [ "$$actual_version" != "$(KEDA_CHART_VERSION)" ]; then \
	  echo "ERROR: pulled chart Chart.yaml version=$$actual_version but Makefile KEDA_CHART_VERSION=$(KEDA_CHART_VERSION). Bump them in lockstep."; \
	  exit 1; \
	fi
	# Cozystack ships its own unittest coverage at tests/; drop the vendored
	# upstream fixtures so they are not mistaken for reviewed code here.
	rm -rf charts/keda/tests
