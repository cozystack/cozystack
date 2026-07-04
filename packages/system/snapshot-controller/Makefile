export NAME=snapshot-controller
export NAMESPACE=cozy-$(NAME)

include ../../../hack/package.mk

test:
	helm unittest .

update:
	rm -rf charts
	helm repo add piraeus-charts https://piraeus.io/helm-charts/
	helm repo update piraeus-charts
	# Pin the vendored version — running `helm pull` without --version would
	# grab whatever is latest on the piraeus-charts repo and break
	# `patches/webhook-startup-probe.diff` (the templates layout changed
	# between v3.x and v5.x). Bumping this pin is a deliberate step that
	# must regenerate the patch against the new upstream.
	helm pull piraeus-charts/snapshot-controller --untar --untardir charts --version 3.0.5
	# cozystack: re-apply local mods carried as patches/*.diff so an upstream
	# chart bump does not silently revert them. Each diff is byte-consistent
	# with the vendored tree and applied against the freshly-pulled upstream.
	# See cozystack/cozystack#3203 for the startup-probe fix rationale.
	patch --no-backup-if-mismatch -p1 < patches/webhook-startup-probe.diff