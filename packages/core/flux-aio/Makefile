NAME=flux-aio
NAMESPACE=cozy-$(NAME)

include ../../../hack/common-envs.mk

show:
	cozyhr show -n $(NAMESPACE) $(NAME) --plain

apply:
	cozyhr show -n $(NAMESPACE) $(NAME) --plain | kubectl apply -f- --server-side --force-conflicts

diff:
	cozyhr show -n $(NAMESPACE) $(NAME) --plain | kubectl diff -f-

MANIFESTS_DIR=../../../internal/fluxinstall/manifests

update:
	timoni bundle build -f flux-aio.cue > $(MANIFESTS_DIR)/fluxcd.yaml
	yq eval '(select(.kind == "Namespace") | .metadata.labels."pod-security.kubernetes.io/enforce") = "privileged"' -i $(MANIFESTS_DIR)/fluxcd.yaml
	# The flux-aio module does not expose a --concurrent flag for source-watcher; inject it here.
	# source-watcher builds one ExternalArtifact per PackageSource component, so raise its
	# concurrency above the binary default of 10 to match the other controllers under load.
	# The append is guarded so a future module that emits its own --concurrent is not duplicated.
	yq eval '(select(.kind == "Deployment" and .metadata.name == "flux").spec.template.spec.containers[] | select(.name == "source-watcher") | select(.args | contains(["--concurrent=20"]) | not).args) += ["--concurrent=20"]' -i $(MANIFESTS_DIR)/fluxcd.yaml
	sed -i $(MANIFESTS_DIR)/fluxcd.yaml \
		-e '/timoni/d' \
		-e 's|\.cluster\.local\.,||g' -e 's|\.cluster\.local\,||g' -e 's|\.cluster\.local\.||g' \
		-e 's|--storage-adv-addr=source-watcher.$$(RUNTIME_NAMESPACE).svc|--storage-adv-addr=flux.$$(RUNTIME_NAMESPACE).svc|'
