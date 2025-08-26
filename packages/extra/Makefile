OUT=../_out/repos/extra
TMP := $(shell mktemp -d)

repo:
	cd .. && ../hack/package_chart.sh extra $(OUT) $(TMP) library

fix-chartnames:
	find . -maxdepth 2 -name Chart.yaml | awk -F/ '{print $$2}' | while read i; do sed "s/^name: .*/name: $$i/" "$$i/Chart.yaml"; done

gen-versions-map: fix-chartnames
	../../hack/gen_versions_map.sh

check-version-map: gen-versions-map
	git diff --exit-code -- versions_map
