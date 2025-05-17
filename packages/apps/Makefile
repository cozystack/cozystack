OUT=../../_out/repos/apps
TMP := $(shell mktemp -d)

repo:
	rm -rf "$(OUT)"
	mkdir -p "$(OUT)"
	cd .. && cat apps/versions_map2 | while read package version commit ; \
	do \
	  if git ls-tree --name-only $${commit} library | grep -qx library ; \
	  then \
	    git archive --format tar $${commit} apps/$${package} library | tar -xf- -C $(TMP)/ ; \
	  else \
	    git archive --format tar $${commit} apps/$${package} | tar -xf- -C $(TMP)/ ; \
	  fi ; \
	  helm package $(TMP)/apps/$${package} -d apps/$(OUT) ; \
	  rm -rf $(TMP)/apps/$${package} $(TMP)/library ; \
	done

fix-chartnames:
	find . -maxdepth 2 -name Chart.yaml  | awk -F/ '{print $$2}' | while read i; do sed -i "s/^name: .*/name: $$i/" "$$i/Chart.yaml"; done

gen-versions-map: fix-chartnames
	../../hack/gen_versions_map.sh

check-version-map: gen-versions-map
	git diff --exit-code -- versions_map
