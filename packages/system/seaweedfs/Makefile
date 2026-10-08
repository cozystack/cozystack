export NAME=seaweedfs-system
SEAWEEDFS_TAG=$(shell grep -F 'ARG VERSION=' images/seaweedfs/Dockerfile | cut -f2 -d=)

include ../../../hack/common-envs.mk
include ../../../hack/package.mk

image:
	docker buildx build images/seaweedfs \
		$(call image-tags,seaweedfs,$(SEAWEEDFS_TAG)) \
		$(call cache-args,seaweedfs) \
		--metadata-file images/seaweedfs.json \
		$(BUILDX_ARGS)
	REPOSITORY="$(REGISTRY)/seaweedfs" \
		yq -i '.seaweedfs.image.repository = strenv(REPOSITORY)' values.yaml
	TAG=$(IMAGE_TAG)@$$(yq e '."containerimage.digest"' images/seaweedfs.json -o json -r) \
		yq -i '.seaweedfs.image.tag = strenv(TAG)' values.yaml
	rm -f images/seaweedfs.json

test:
	helm unittest .
	$(MAKE) test-guard-fail-closed
	$(MAKE) test-image-version

# The image replaces weed in the upstream image of ARG VERSION, so a chart moved
# by `make update` without the Dockerfile would run the previous release's
# binary under the new chart.
.PHONY: test-image-version
test-image-version:
	@chart=$$(yq e '.appVersion' charts/seaweedfs/Chart.yaml); \
	[ "$(SEAWEEDFS_TAG)" = "$$chart" ] \
	  || { echo "FAIL: images/seaweedfs/Dockerfile builds $(SEAWEEDFS_TAG), the vendored chart is $$chart"; exit 1; }
	@echo "ok: images/seaweedfs builds the chart's appVersion $(SEAWEEDFS_TAG)"

# The naming guard refuses to render an UPGRADE that cannot see the cluster
# (templates/naming-guard.yaml): a client-side lookup silently returns nothing,
# which would classify a class-S tenant as net-new and rename its workloads away
# from its data. helm-unittest 1.0.3 ignores release.isUpgrade, so .Release.IsUpgrade
# always renders false there and the refusal branch gets NO unit coverage — the
# suite passes with or without the canary. Verify with a real renderer instead.
.PHONY: test-guard-fail-closed
test-guard-fail-closed:
	@out=$$(helm template seaweedfs-system . -n tenant-root --is-upgrade 2>&1); \
	echo "$$out" | grep -q 'refusing to upgrade blind' \
	  || { echo "FAIL: a blind upgrade rendered instead of refusing (naming-guard canary is not firing)"; echo "$$out" | head -5; exit 1; }
	@echo "ok: naming guard refuses a blind upgrade"

update:
	rm -rf charts
	mkdir -p charts
	version=$$(git ls-remote --tags --sort="v:refname" https://github.com/seaweedfs/seaweedfs | grep -v '\^{}' | grep 'refs/tags/[0-9]' | awk -F'/' 'END{print $$3}') && \
	curl -sSL https://github.com/seaweedfs/seaweedfs/archive/refs/tags/$${version}.tar.gz | \
	tar xzvf - --strip 3 -C charts seaweedfs-$${version}/k8s/charts/seaweedfs
	patch --no-backup-if-mismatch -p4 < patches/resize-api-server-annotation.diff
	patch --no-backup-if-mismatch -p4 < patches/volume-resize-quantity.diff
	patch --no-backup-if-mismatch -p4 < patches/disable-ca-key-rotation.patch
	patch --no-backup-if-mismatch -p4 < patches/s3-tls-main-port.patch
	patch --no-backup-if-mismatch -p4 < patches/s3-iceberg-port-explicit.patch
	patch --no-backup-if-mismatch -p4 < patches/cosi-provisioner-sa-name.patch
	patch --no-backup-if-mismatch -p4 < patches/cosi-bucket-class-lock-readonly.patch
	patch --no-backup-if-mismatch -p4 < patches/s3-service-name.patch
	patch --no-backup-if-mismatch -p4 < patches/s3-service-name-consumers.patch
	patch --no-backup-if-mismatch -p4 < patches/cluster-scoped-names-per-namespace.patch
	#patch --no-backup-if-mismatch -p4 < patches/retention-policy-delete.yaml

