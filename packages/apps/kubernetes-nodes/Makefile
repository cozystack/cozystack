include ../../../hack/package.mk

.PHONY: test generate update

test:
	helm unittest .

generate:
	cozyvalues-gen -m 'kubernetesnodes' -v values.yaml -s values.schema.json -r README.md -g ../../../api/apps/v1alpha1/kubernetesnodes/types.go
	../../../hack/update-crd.sh

# Keep the worker-image inputs (Talos<->Kubernetes support matrix and the
# kubectl.tag used by the talos-reconcile Job) in lockstep with the parent
# kubernetes chart. They are copied, not symlinked, so `helm package` bundles
# them; run this after the parent chart's `make update`.
#
# _talosconfigtemplate.tpl is not copied from anywhere: the parent chart no
# longer carries worker node pools, so this chart owns the worker machine
# config outright. It stays here because the TalosConfigTemplate it renders is
# content-hash named and both consumers in this chart - nodegroup.yaml, which
# writes bootstrap.configRef, and talos-reconcile-job.yaml, which creates the
# object - have to hash the same bytes.
update:
	cp ../kubernetes/files/versions.yaml files/versions.yaml
	cp ../kubernetes/images/kubectl.tag images/kubectl.tag
	$(MAKE) generate
