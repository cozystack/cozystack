OUT=../../_out/repos/apps
CHARTS := $(shell find . -maxdepth 2 -name Chart.yaml | awk -F/ '{print $$2}')

include ../../scripts/common-envs.mk

repo:
	rm -rf "$(OUT)"
	helm package -d "$(OUT)" $(CHARTS) --version $(COZYSTACK_VERSION)
	helm repo index "$(OUT)"

fix-charts:
	find . -maxdepth 2 -name Chart.yaml | awk -F/ '{print $$2}' | while read i; do sed -i -e "s/^name: .*/name: $$i/" -e "s/^version: .*/version: 0.0.0 # Placeholder, the actual version will be automatically set during the build process/g" "$$i/Chart.yaml"; done
