export NAME=kubevirt-cdi-operator
export NAMESPACE=cozy-kubevirt-cdi

# The CDI release whose operator manifest is vendored under templates/. This
# package decides which CDI the platform runs, so moving to another release is
# an edit to this line, MANIFEST_SHA256 and the expectations under tests/,
# rather than a side effect of running `update`.
RELEASE = v1.66.1

# sha256 of the cdi-operator.yaml release asset as published, measured before
# the transforms below run over it. It detects an asset that moved under its
# tag; it is not authenticity verification.
MANIFEST_SHA256 = c7d92bd1b2ee1a3952a5990000437c08d58109f31c0bd48ab6188ec27a13e224

# sha256sum is not everywhere: current macOS carries a native one in /sbin,
# older releases carry only shasum(1). Both print "<digest>  <path>", so field
# 1 is the digest whichever gets picked. The rest of `update` needs GNU sed and
# gawk regardless, so this alone does not make the recipe portable.
SHA256SUM := $(shell command -v sha256sum >/dev/null 2>&1 && echo sha256sum || echo 'shasum -a 256')

include ../../../hack/package.mk

# Phony because hack/package.mk's .PHONY line lists only the targets that file
# defines: with neither name declared, a path named `test` or `update` next to
# this Makefile would make that target up to date and skip the recipe.
.PHONY: test update
test:
	helm unittest .
	@# helm-unittest reads the vendored file and cannot see RELEASE, so without
	@# this a pin moved without re-running `update` leaves every assertion
	@# green against the old release.
	@if ! grep -q 'image: quay.io/kubevirt/cdi-operator:$(RELEASE)$$' templates/cdi-operator.yaml; then \
	  echo "ERROR: templates/cdi-operator.yaml carries no cdi-operator image at $(RELEASE). Re-run 'make update'." >&2; \
	  exit 1; \
	fi

update:
	@# The download is verified, transformed and patched outside templates/ and
	@# moved in once finished, so a refresh that fails at any step leaves the
	@# vendored copy as it was rather than half-replaced.
	wget https://github.com/kubevirt/containerized-data-importer/releases/download/$(RELEASE)/cdi-operator.yaml -O cdi-operator.yaml.download || { rm -f cdi-operator.yaml.download; exit 1; }
	@actual=$$($(SHA256SUM) cdi-operator.yaml.download | awk '{print $$1}'); \
	if [ "$$actual" != "$(MANIFEST_SHA256)" ]; then \
	  rm -f cdi-operator.yaml.download; \
	  echo "ERROR: cdi-operator.yaml for $(RELEASE) hashed $$actual," >&2; \
	  echo "       expected $(MANIFEST_SHA256). Nothing was written." >&2; \
	  echo "       Moving to a new release starts from RELEASE, MANIFEST_SHA256 and" >&2; \
	  echo "       the version expectations under tests/. If RELEASE did not" >&2; \
	  echo "       change, the asset moved under a tag that should be immutable:" >&2; \
	  echo "       find out why before pasting the new digest." >&2; \
	  exit 1; \
	fi
	sed -i 's/namespace: cdi/namespace: $(NAMESPACE)/g' cdi-operator.yaml.download
	awk -i inplace -v RS="---" '!/kind: Namespace/{printf "%s", $$0 RS}' cdi-operator.yaml.download
	patch --no-backup-if-mismatch cdi-operator.yaml.download < patches/startupProbe.diff
	patch --no-backup-if-mismatch cdi-operator.yaml.download < patches/tolerations.diff
	mkdir -p templates
	mv cdi-operator.yaml.download templates/cdi-operator.yaml
