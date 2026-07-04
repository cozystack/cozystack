export NAME=snapshot-controller
export NAMESPACE=cozy-$(NAME)

include ../../../hack/package.mk

update:
	rm -rf charts
	helm repo add piraeus-charts https://piraeus.io/helm-charts/
	helm repo update piraeus-charts
	helm pull piraeus-charts/snapshot-controller --untar --untardir charts
	# cozystack: re-apply local mods carried as patches/*.diff so an upstream
	# chart bump does not silently revert them. Each diff is byte-consistent
	# with the vendored tree and applied against the freshly-pulled upstream.
	# See cozystack/cozystack#3203 for the startup-probe fix rationale.
	patch --no-backup-if-mismatch -p1 < patches/webhook-startup-probe.diff