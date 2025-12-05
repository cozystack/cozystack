OUT=../../_out/repos/system
CHARTS := $(shell grep -F 'version: 0.0.0' */Chart.yaml | cut -f1 -d/)
VERSIONED_CHARTS := $(shell grep '^version:' */Chart.yaml | grep -Fv '0.0.0' | cut -f1 -d/)

include ../../scripts/common-envs.mk

repo:
	rm -rf "$(OUT)"
	helm package -d "$(OUT)" $(CHARTS) --version $(COZYSTACK_VERSION)
	helm package -d "$(OUT)" $(VERSIONED_CHARTS)
	cd "$(OUT)" && helm repo index .

fix-charts:
	find . -maxdepth 2 -name Chart.yaml | awk -F/ '{print $$2}' | while read i; do sed -i -e "s/^name: .*/name: cozy-$$i/" -e "s/^version: .*/version: 0.0.0 # Placeholder, the actual version will be automatically set during the build process/g" "$$i/Chart.yaml"; done
