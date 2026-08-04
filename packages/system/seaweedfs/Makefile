export NAME=seaweedfs-system

include ../../../hack/common-envs.mk
include ../../../hack/package.mk

test:
	helm unittest .
	$(MAKE) test-guard-fail-closed

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
	patch --no-backup-if-mismatch -p4 < patches/disable-ca-key-rotation.patch
	patch --no-backup-if-mismatch -p4 < patches/s3-tls-main-port.patch
	patch --no-backup-if-mismatch -p4 < patches/cosi-provisioner-sa-name.patch
	patch --no-backup-if-mismatch -p4 < patches/cosi-bucket-class-lock-readonly.patch
	patch --no-backup-if-mismatch -p4 < patches/s3-service-name.patch
	patch --no-backup-if-mismatch -p4 < patches/s3-service-name-consumers.patch
	patch --no-backup-if-mismatch -p4 < patches/cluster-scoped-names-per-namespace.patch
	#patch --no-backup-if-mismatch -p4 < patches/retention-policy-delete.yaml

