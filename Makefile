.PHONY: manifests repos assets unit-tests helm-unit-tests

build-deps:
	@command -V find docker skopeo jq gh helm > /dev/null
	@yq --version | grep -q "mikefarah" || (echo "mikefarah/yq is required" && exit 1)
	@tar --version | grep -q GNU || (echo "GNU tar is required" && exit 1)
	@sed --version | grep -q GNU || (echo "GNU sed is required" && exit 1)
	@awk --version | grep -q GNU || (echo "GNU awk is required" && exit 1)

build: build-deps
	make -C packages/apps/http-cache image
	make -C packages/apps/mysql image
	make -C packages/apps/clickhouse image
	make -C packages/apps/kubernetes image
	make -C packages/extra/monitoring image
	make -C packages/system/cozystack-api image
	make -C packages/system/cozystack-controller image
	make -C packages/system/backup-controller image
	make -C packages/system/lineage-controller-webhook image
	make -C packages/system/cilium image
	make -C packages/system/kubeovn-webhook image
	make -C packages/system/kubeovn-plunger image
	make -C packages/system/dashboard image
	make -C packages/system/metallb image
	make -C packages/system/kamaji image
	make -C packages/system/bucket image
	make -C packages/system/objectstorage-controller image
	make -C packages/core/testing image
	make -C packages/core/installer image
	make manifests

repos:
	rm -rf _out
	make -C packages/system repo
	make -C packages/apps repo
	make -C packages/extra repo

manifests:
	mkdir -p _out/assets
	(cd packages/core/installer/; helm template -n cozy-installer installer .) > _out/assets/cozystack-installer.yaml

assets:
	make -C packages/core/installer assets

test:
	make -C packages/core/testing apply
	make -C packages/core/testing test

unit-tests: helm-unit-tests

helm-unit-tests:
	hack/helm-unit-tests.sh

prepare-env:
	make -C packages/core/testing apply
	make -C packages/core/testing prepare-cluster

generate:
	hack/update-codegen.sh

upload_assets: manifests
	hack/upload-assets.sh
