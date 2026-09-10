export NAME=kubevirt-cdi
export NAMESPACE=cozy-$(NAME)

# The CDI release templates/cdi-cr.yaml is reconciled against. Moving to a
# newer CDI is an edit to this line. What the platform actually deploys is set
# by ../kubevirt-cdi-operator, which pins nothing: its recipe vendors whatever
# GitHub's releases/latest resolved to when someone last ran it. So this line
# does not track that one, it has to be pointed at it by hand, and nothing in
# either file reports the two having drifted apart. Deriving this from the
# sibling manifest would end the pin: the point is that a human chooses which
# release this object is reconciled against, and a computed value hides that
# choice from the diff.
RELEASE = v1.66.1

# sha256 of the cdi-cr.yaml release asset as published. Upstream publishes no
# checksum for it, so the pin is taken from a download rather than fetched
# alongside one: it detects an asset that moved under its tag, and it is not
# authenticity verification.
CR_SHA256 = a497f90de608c1df26f9ee4095289373f17132d74a2042ca03e00c17c964f8a7

# sha256sum is not everywhere: current macOS carries a native one in /sbin,
# older releases carry only shasum(1). Both print "<digest>  <path>", so field
# 1 is the digest whichever gets picked.
SHA256SUM = $(shell command -v sha256sum >/dev/null 2>&1 && echo sha256sum || echo 'shasum -a 256')

include ../../../hack/package.mk

# Phony because hack/package.mk's .PHONY line lists only the targets that file
# defines: with neither name declared, a path named `test` or `update` next to
# this Makefile would make that target up to date and skip the recipe.
.PHONY: test update

# hack/package.mk defines no `test` target and hack/helm-unit-tests.sh only
# runs a package whose `make -n test` resolves, so a tests/ directory with no
# `test:` target is collected by nothing and the suite is skipped in silence.
test:
	helm unittest .

# templates/cdi-cr.yaml is ours, not upstream's: it adds to upstream's bare CDI
# object, and what it adds is pinned under tests/ rather than restated here.
# The delta is a fact about one release and not a standing property of CDI:
# upstream carried WebhookPvcRendering from v1.63.0 through v1.65.0 and dropped
# it by v1.66.0, and this object never carried it at all, so the difference ran
# both ways for three releases and one way after. Re-derive it at each bump
# instead of inheriting it.
#
# So nothing here writes under templates/: fetching the release the object is
# derived from confirms the bytes still hash to the pin, and stops there. What
# that catches is an edit to RELEASE without a matching digest, and an asset
# that moved under a tag that should be immutable. Moving RELEASE is therefore
# the step that forces someone to reconcile this object by hand, which is the
# point. It does not catch ../kubevirt-cdi-operator vendoring a new release,
# because nothing here reads that package. RELEASE is checked by `update`
# rather than by `test`, because helm-unittest reads rendered output and this
# object renders no version string.
update:
	wget https://github.com/kubevirt/containerized-data-importer/releases/download/$(RELEASE)/cdi-cr.yaml -O cdi-cr.yaml.download || { rm -f cdi-cr.yaml.download; exit 1; }
	@# Kept on a mismatch rather than deleted; the error below says why.
	@actual=$$($(SHA256SUM) cdi-cr.yaml.download | awk '{print $$1}'); \
	if [ "$$actual" != "$(CR_SHA256)" ]; then \
	  echo "ERROR: cdi-cr.yaml for $(RELEASE) hashed $$actual," >&2; \
	  echo "       expected $(CR_SHA256). The download is kept as" >&2; \
	  echo "       cdi-cr.yaml.download." >&2; \
	  echo "       If RELEASE just moved, diff it against templates/cdi-cr.yaml," >&2; \
	  echo "       carry over what belongs there and update CR_SHA256. If RELEASE" >&2; \
	  echo "       did not change, the asset moved under a tag that should be" >&2; \
	  echo "       immutable: find out why first." >&2; \
	  exit 1; \
	fi; \
	rm -f cdi-cr.yaml.download
	@echo "upstream cdi-cr.yaml for $(RELEASE) matches the pin; templates/cdi-cr.yaml is maintained by hand and was left alone."
