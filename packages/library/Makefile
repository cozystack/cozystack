OUT=../../_out/repos/library
TMP := $(shell mktemp -d)

repo:
	rm -rf "$(OUT)"
	mkdir -p "$(OUT)"
	cd .. && cat library/versions_map | while read package version commit ; \
	do git archive --format tar $${commit} library/$${package} | tar -xf- -C $(TMP)/ ; \
	helm package $(TMP)/library/$${package} -d $(OUT) ; \
	rm -rf $(TMP)/library/$${package} ; \
	done

fix-chartnames:
	find . -maxdepth 2 -name Chart.yaml  | awk -F/ '{print $$2}' | while read i; do sed -i "s/^name: .*/name: $$i/" "$$i/Chart.yaml"; done

gen-versions-map: fix-chartnames
	../../hack/gen_versions_map.sh

check-version-map: gen-versions-map
	git diff --exit-code -- versions_map

