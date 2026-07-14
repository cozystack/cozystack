NAME=fluxcd
NAMESPACE=cozy-$(NAME)

include ../../../hack/package.mk

apply-locally:
	cozyhr apply --plain -n $(NAMESPACE) $(NAME)

update:
	rm -rf charts
	helm pull oci://ghcr.io/controlplaneio-fluxcd/charts/flux-instance --version 0.50.0 --untar --untardir charts
	patch --no-backup-if-mismatch -p1 < patches/guard-distribution-artifact.diff
