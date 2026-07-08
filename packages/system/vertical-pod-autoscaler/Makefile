export NAME=vertical-pod-autoscaler
export NAMESPACE=cozy-$(NAME)

include ../../../hack/package.mk

test:
	helm unittest .

update:
	rm -rf charts
	# VirtualPodAutoscaler operator
	helm repo add cowboysysop https://cowboysysop.github.io/charts/
	helm repo update cowboysysop
	helm pull cowboysysop/vertical-pod-autoscaler --untar --untardir charts --version 11.1.1
	# cozystack-specific: replace the upstream genCA-per-render TLS Secret
	# with a cert-manager Issuer+Certificate; gate the legacy Secret and
	# its checksum/tls-secret annotation behind admissionController.tls.
	# certManagerEnabled=false; add a runtime-lookup-based checksum/tls-cert
	# annotation so pods rotate on Secret content change (not on every
	# reconcile). See patch header + certificate.yaml template comment
	# for the full rationale.
	patch --no-backup-if-mismatch -p1 < patches/cert-manager-admission-tls.diff
