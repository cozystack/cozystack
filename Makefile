.PHONY: manifests repos assets

build:
	make -C packages/apps/http-cache image
	make -C packages/apps/postgres image
	make -C packages/apps/mysql image
	make -C packages/apps/clickhouse image
	make -C packages/apps/kubernetes image
	make -C packages/extra/monitoring image
	make -C packages/system/cozystack-api image
	make -C packages/system/cozystack-controller image
	make -C packages/system/cilium image
	make -C packages/system/kubeovn image
	make -C packages/system/kubeovn-webhook image
	make -C packages/system/dashboard image
	make -C packages/system/kamaji image
	make -C packages/system/bucket image
	make -C packages/core/testing image
	make -C packages/core/installer image
	make manifests

repos:
	rm -rf _out
	make -C packages/apps check-version-map
	make -C packages/extra check-version-map
	make -C packages/system repo
	make -C packages/apps repo
	make -C packages/extra repo
	mkdir -p _out/logos
	cp ./packages/apps/*/logos/*.svg ./packages/extra/*/logos/*.svg _out/logos/


manifests:
	mkdir -p _out/assets
	(cd packages/core/installer/; helm template -n cozy-installer installer .) > _out/assets/cozystack-installer.yaml

assets:
	make -C packages/core/installer/ assets

test:
	test -f _out/assets/nocloud-amd64.raw.xz || make -C packages/core/installer talos-nocloud
	make -C packages/core/testing apply
	make -C packages/core/testing test
	make -C packages/core/testing test-applications

generate:
	hack/update-codegen.sh

upload_assets: manifests
	hack/upload-assets.sh
