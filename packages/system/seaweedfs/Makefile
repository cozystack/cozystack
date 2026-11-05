export NAME=seaweedfs-system
SEAWEEDFS_TAG=$(shell grep -F 'ARG VERSION=' images/seaweedfs/Dockerfile | cut -f2 -d=)

include ../../../hack/common-envs.mk
include ../../../hack/package.mk

.PHONY: image image-seaweedfs image-cosi-driver
image: image-seaweedfs image-cosi-driver

image-seaweedfs:
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
	$(MAKE) test-cosi-driver-version

# The image replaces weed in the upstream image of ARG VERSION, so a chart moved
# by `make update` without the Dockerfile would run the previous release's
# binary under the new chart.
.PHONY: test-image-version
test-image-version:
	@chart=$$(yq e '.appVersion' charts/seaweedfs/Chart.yaml); \
	[ "$(SEAWEEDFS_TAG)" = "$$chart" ] \
	  || { echo "FAIL: images/seaweedfs/Dockerfile builds $(SEAWEEDFS_TAG), the vendored chart is $$chart"; exit 1; }
	@echo "ok: images/seaweedfs builds the chart's appVersion $(SEAWEEDFS_TAG)"

# COSI_DRIVER_VERSION names the image tag, ARG VERSION the commit that is
# built; a tag left behind by a commit bump would ship one build under
# another's name.
.PHONY: test-cosi-driver-version
test-cosi-driver-version:
	@commit=$$(grep -F 'ARG VERSION=' images/seaweedfs-cosi-driver/Dockerfile | cut -f2 -d=); \
	abbrev=$$(echo "$(COSI_DRIVER_VERSION)" | sed -n 's/.*-g\([0-9a-f]\{7,\}\)$$/\1/p'); \
	case "$$abbrev:$$commit" in ?*:"$$abbrev"*) ;; *) echo "FAIL: COSI_DRIVER_VERSION $(COSI_DRIVER_VERSION) does not name the commit images/seaweedfs-cosi-driver/Dockerfile builds ($$commit)"; exit 1;; esac
	@echo "ok: COSI_DRIVER_VERSION $(COSI_DRIVER_VERSION) names the built commit"

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

# `git describe` of the upstream commit pinned by ARG VERSION in the Dockerfile;
# keep the two in lockstep.
COSI_DRIVER_VERSION = v0.3.1-10-g0df543f
# Bump when patches/ or the build changes on the same upstream commit.
COSI_DRIVER_REV = 0
COSI_DRIVER_COMPONENT_VERSION = $(COSI_DRIVER_VERSION)-cozystack.$(COSI_DRIVER_REV)

# Depends on image-seaweedfs so the two `yq -i` stamps of values.yaml never run
# concurrently under `make -j image`.
image-cosi-driver: image-seaweedfs
	docker buildx build images/seaweedfs-cosi-driver \
		$(call image-tags,seaweedfs-cosi-driver,$(COSI_DRIVER_COMPONENT_VERSION)) \
		$(call cache-args,seaweedfs-cosi-driver) \
		--metadata-file images/seaweedfs-cosi-driver.json \
		$(BUILDX_ARGS)
	IMAGE="$(REGISTRY)/seaweedfs-cosi-driver:$(COSI_DRIVER_COMPONENT_VERSION)@$$(yq e '."containerimage.digest"' images/seaweedfs-cosi-driver.json -r)" \
		yq -i '.seaweedfs.cosi.image = strenv(IMAGE)' values.yaml
	rm -f images/seaweedfs-cosi-driver.json
	yq .seaweedfs.cosi.image values.yaml > ../../extra/seaweedfs/images/seaweedfs-cosi-driver.tag

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
	$(MAKE) update-image

# Re-pins images/seaweedfs/Dockerfile to the vendored chart's appVersion: the
# source archive checksum, the upstream image digest and upstream's builder
# toolchain for that tag. The patches are Go, so they are checked against the
# whole source tree of the tag; once a release carries a fix, its patch stops
# applying here and is dropped.
.PHONY: update-image
update-image:
	version=$$(yq e '.appVersion' charts/seaweedfs/Chart.yaml) && \
	upstream=$$(docker buildx imagetools inspect "chrislusf/seaweedfs:$${version}" --format '{{json .Manifest}}' | yq e '.digest' -) && \
	case "$$upstream" in sha256:*) ;; *) echo "ERROR: no digest for chrislusf/seaweedfs:$${version}" >&2; exit 1;; esac && \
	$(SED_INPLACE) -e "s/^ARG VERSION=.*/ARG VERSION=$${version}/" -e "s/^ARG UPSTREAM_DIGEST=.*/ARG UPSTREAM_DIGEST=$${upstream}/" images/seaweedfs/Dockerfile && \
	tmpdir=$$(mktemp -d) && \
	{ curl --fail --silent --show-error --location -o "$$tmpdir/source.tar.gz" \
		"https://github.com/seaweedfs/seaweedfs/archive/refs/tags/$${version}.tar.gz" && \
		mkdir "$$tmpdir/src" && tar -xzf "$$tmpdir/source.tar.gz" --strip-components=1 -C "$$tmpdir/src" || { rm -rf "$$tmpdir"; exit 1; }; } && \
	source=$$(sha256sum "$$tmpdir/source.tar.gz" | cut -d' ' -f1) && \
	$(SED_INPLACE) "s/^ARG SOURCE_SHA256=.*/ARG SOURCE_SHA256=$${source}/" images/seaweedfs/Dockerfile && \
	{ git -C "$$tmpdir/src" apply --check $(CURDIR)/images/seaweedfs/patches/*.diff || \
		{ echo "ERROR: images/seaweedfs/patches/*.diff no longer apply to seaweedfs $${version}; regenerate them, or drop the ones the new tag already carries" >&2; \
		rm -rf "$$tmpdir"; exit 1; }; } && \
	golang=$$(sed -n 's/^FROM --platform=\$$BUILDPLATFORM \([^ ]*\) AS builder$$/\1/p' "$$tmpdir/src/docker/Dockerfile.go_build") && \
	{ [ -n "$$golang" ] || { echo "ERROR: no builder FROM line in upstream docker/Dockerfile.go_build $${version}" >&2; rm -rf "$$tmpdir"; exit 1; }; } && \
	builder=$$(docker buildx imagetools inspect "$$golang" --format '{{json .Manifest}}' | yq e '.digest' -) && \
	{ case "$$builder" in sha256:*) ;; *) echo "ERROR: no digest for $${golang}" >&2; rm -rf "$$tmpdir"; exit 1;; esac; } && \
	$(SED_INPLACE) "s|^FROM --platform=\$$BUILDPLATFORM [^ ]* AS builder$$|FROM --platform=\$$BUILDPLATFORM $${golang}@$${builder} AS builder|" images/seaweedfs/Dockerfile && \
	{ grep -qF "FROM --platform=\$$BUILDPLATFORM $${golang}@$${builder} AS builder" images/seaweedfs/Dockerfile || \
		{ echo "ERROR: the builder FROM line in images/seaweedfs/Dockerfile was not re-pinned; its shape no longer matches the update recipe" >&2; rm -rf "$$tmpdir"; exit 1; }; } && \
	rm -rf "$$tmpdir"

