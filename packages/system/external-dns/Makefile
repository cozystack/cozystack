export NAME=external-dns
export NAMESPACE=cozy-$(NAME)

include ../../../hack/package.mk

test:
	helm unittest .

update:
	rm -rf charts
	helm repo add external-dns https://kubernetes-sigs.github.io/external-dns/
	helm repo update external-dns
	# Pin the vendored chart version. `helm pull` without --version grabs
	# whatever is latest on the repo, which can shift the deployment.yaml
	# layout and silently break patches/deployment-startup-probe.diff. Bumping
	# this pin is a deliberate step that must regenerate the patch against the
	# new upstream and re-run `make test`.
	helm pull external-dns/external-dns --untar --untardir charts --version 1.20.0
	# cozystack: re-apply local mods carried as patches/*.diff so an upstream
	# chart bump does not silently revert them. The diff is byte-consistent with
	# the vendored tree and applied against the freshly-pulled upstream.
	# See cozystack/cozystack#3459 for the startup-probe fix rationale.
	patch --no-backup-if-mismatch -p1 < patches/deployment-startup-probe.diff
