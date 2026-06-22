export NAME=opencost
export NAMESPACE=cozy-$(NAME)

include ../../../hack/package.mk

test:
	helm unittest .

update:
	rm -rf charts
	helm repo add opencost-charts https://opencost.github.io/opencost-helm-chart
	helm repo update opencost-charts
	helm pull opencost-charts/opencost --untar --untardir charts --version 2.5.23
	# Drop the upstream chart's own helm-unittest suite: it is snapshot-based
	# and fails in the cozystack render context (configsChecksum can't resolve
	# the custom-pricing configmap glob). cozystack ships its own tests/.
	rm -rf charts/opencost/tests
