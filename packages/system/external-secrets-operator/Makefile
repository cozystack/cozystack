export NAME=external-secrets-operator
export NAMESPACE=cozy-$(NAME)

include ../../../hack/package.mk

test:
	helm unittest .

update:
	rm -rf charts
	helm repo add external-secrets https://charts.external-secrets.io
	helm repo update external-secrets
	helm pull external-secrets/external-secrets --untar --untardir charts
	rm -rf charts/external-secrets/charts
	# Upstream already ships the named targetPort as of chart 0.19.1
	# (external-secrets/external-secrets#5108); the vendored chart predates
	# it, so the patch carries the same one-line change until the chart is
	# bumped past that release. --forward matters here: against a chart that
	# already has the named form, patch(1) would silently reverse the hunk
	# and exit 0, quietly restoring the numeric form. With it, an
	# already-applied patch fails loudly instead — the signal to drop both
	# the patch and this block.
	patch --no-backup-if-mismatch --forward -p4 < patches/webhook-service-named-target-port.patch