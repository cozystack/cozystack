export NAME=kubevirt-operator
export NAMESPACE=cozy-kubevirt

# The KubeVirt release whose operator manifest is vendored under templates/.
#
# A live migration onto the new virt-launcher can fail on virtio feature bits
# whenever its QEMU build moves at all, major or not (kubevirt/kubevirt#16386
# broke on qemu-kvm 9.1.0-19 to 9.1.0-20): test one before moving RELEASE.
RELEASE = v1.9.0

# sha256 of the kubevirt-operator.yaml release asset as published, measured
# before the two transforms below run over it. Upstream signs the source
# tarball and publishes no checksum for this generated manifest, so the pin is
# taken from a download rather than fetched alongside one. It therefore detects
# an asset that moved under its tag; it is not authenticity verification.
MANIFEST_SHA256 = f11307caafc3c23ffedf9887d8beb5a4419e2694da242fa68f63d1ec820de2e0

# sha256sum is not everywhere: current macOS carries a native one in /sbin,
# older releases carry only shasum(1). Both print "<digest>  <path>", so field
# 1 is the digest whichever gets picked. The rest of `update` needs GNU sed and
# gawk regardless, so this alone does not make the recipe portable.
SHA256SUM := $(shell command -v sha256sum >/dev/null 2>&1 && echo sha256sum || echo 'shasum -a 256')

include ../../../hack/package.mk

# Phony because hack/package.mk's .PHONY line lists only the targets that file
# defines, and hack/package-mk-phony.bats pins the omission of `test`: without
# this, a path named `test` next to this Makefile would make the target up to
# date and skip the recipe.
.PHONY: test
test:
	helm unittest .
	@# Nothing else ties RELEASE to the tree, because helm-unittest reads the
	@# vendored file and cannot see this variable. Without this check a pin
	@# bumped without re-running `update` leaves every assertion green against
	@# the old release.
	@if ! grep -q 'image: quay.io/kubevirt/virt-operator:$(RELEASE)$$' templates/kubevirt-operator.yaml; then \
	  echo "ERROR: templates/kubevirt-operator.yaml carries no virt-operator image at $(RELEASE). Re-run 'make update'." >&2; \
	  exit 1; \
	fi

update:
	mkdir -p templates
	@# Helm renders every file under templates/, and that directory also holds
	@# prometheusrule.yaml, which is ours rather than upstream's. So the
	@# download is verified and transformed where Helm cannot see it and moved
	@# in once, finished: a refresh that fails at any step leaves the vendored
	@# copy as it was rather than half-replaced.
	wget https://github.com/kubevirt/kubevirt/releases/download/$(RELEASE)/kubevirt-operator.yaml -O kubevirt-operator.yaml.download || { rm -f kubevirt-operator.yaml.download; exit 1; }
	@actual=$$($(SHA256SUM) kubevirt-operator.yaml.download | awk '{print $$1}'); \
	if [ "$$actual" != "$(MANIFEST_SHA256)" ]; then \
	  rm -f kubevirt-operator.yaml.download; \
	  echo "ERROR: kubevirt-operator.yaml for $(RELEASE) hashed $$actual," >&2; \
	  echo "       expected $(MANIFEST_SHA256). Nothing was written." >&2; \
	  echo "       Moving to a new release starts from RELEASE, MANIFEST_SHA256 and" >&2; \
	  echo "       the version expectations under tests/. If RELEASE did not" >&2; \
	  echo "       change, the asset moved under a tag that should be immutable:" >&2; \
	  echo "       find out why before pasting the new digest." >&2; \
	  exit 1; \
	fi
	sed -i 's/namespace: kubevirt/namespace: $(NAMESPACE)/g' kubevirt-operator.yaml.download
	awk -i inplace -v RS="---" '!/kind: Namespace/{printf "%s", $$0 RS}' kubevirt-operator.yaml.download
	mv kubevirt-operator.yaml.download templates/kubevirt-operator.yaml
