NAME=flux-aio
NAMESPACE=cozy-$(NAME)

include ../../../scripts/common-envs.mk

show:
	cozypkg show -n $(NAMESPACE) $(NAME) --plain

apply:
	cozypkg show -n $(NAMESPACE) $(NAME) --plain | kubectl apply -f- --server-side --force-conflicts

diff:
	cozypkg show -n $(NAMESPACE) $(NAME) --plain | kubectl diff -f-

update:
	timoni bundle build -f flux-aio.cue > manifests/fluxcd.yaml
	yq eval '(select(.kind == "Namespace") | .metadata.labels."pod-security.kubernetes.io/enforce") = "privileged"' -i manifests/fluxcd.yaml
	sed -i manifests/fluxcd.yaml \
		-e '/timoni/d' \
		-e 's|\.cluster\.local\.,||g' -e 's|\.cluster\.local\,||g' -e 's|\.cluster\.local\.||g'
	# TODO: solve dns issue with hostNetwork for installing helmreleases in tenant k8s clusters
	#-e '/hostNetwork: true/i \      dnsPolicy: ClusterFirstWithHostNet' 
