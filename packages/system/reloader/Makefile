export NAME=reloader
export NAMESPACE=cozy-$(NAME)
export REPO_NAME=stakater
export REPO_URL=https://stakater.github.io/stakater-charts
export CHART_NAME=reloader
# Pin the vendored chart exactly. An unpinned pull takes whatever is latest,
# which drags a Reloader upgrade into whatever change happens to run `update`
# next and can move the deployment.yaml layout out from under
# patches/deployment-startup-probe.diff. Raising this is a deliberate step:
# regenerate the patch against the new upstream and re-run `make test`.
#
# Read this as "the version that is vendored", not "the current version":
# upstream is at 2.2.14 / app v1.4.19 while this is 2.0.0 / app v1.3.0. The pin
# is what makes the vendored tree reproducible, and it does not itself schedule
# the catch-up bump, which is its own change.
export CHART_VERSION=2.0.0

include ../../../hack/package.mk

test:
	helm unittest .
	# CHART_VERSION names the version that is vendored, and nothing else
	# enforces that: bumping the pin without re-running `make update` leaves the
	# committed tree on the old chart and the claim quietly false.
	@actual_version=$$(awk '/^version:/ {print $$2}' charts/reloader/Chart.yaml | tr -d '"'); \
	if [ "$$actual_version" != "$(CHART_VERSION)" ]; then \
	  echo "ERROR: vendored charts/reloader/Chart.yaml version=$$actual_version but Makefile CHART_VERSION=$(CHART_VERSION). Re-run 'make update'."; \
	  exit 1; \
	fi

update: clean reloader-update
	# Re-apply local modifications carried as patches/*.diff. clean runs first,
	# so this target always patches a freshly pulled tree; --forward guards the
	# re-run by hand over a tree that already carries the change, where patch(1)
	# defaults diverge by implementation: GNU 2.7.6 refuses and exits 1, Apple
	# 2.0-12u11 takes its "Assume -R? [y]" default and strips the hunk back out
	# with exit 0, and GNU under --batch does the same. --forward refuses in all
	# three, leaving a .rej and a non-zero exit.
	patch --forward --no-backup-if-mismatch -p1 < patches/deployment-startup-probe.diff
