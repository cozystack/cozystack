NAME=flux-aio
NAMESPACE=cozy-$(NAME)

include ../../../scripts/common-envs.mk

show:
	cozyhr show -n $(NAMESPACE) $(NAME) --plain

apply:
	cozyhr show -n $(NAMESPACE) $(NAME) --plain | kubectl apply -f- --server-side --force-conflicts

diff:
	cozyhr show -n $(NAMESPACE) $(NAME) --plain | kubectl diff -f-

update: update-old update-new

# TODO: remove old manifest after migration to cozystack-operator
update-old:
	timoni bundle build -f flux-aio.cue > templates/fluxcd.yaml
	yq eval '(select(.kind == "Namespace") | .metadata.labels."pod-security.kubernetes.io/enforce") = "privileged"' -i templates/fluxcd.yaml
	sed -i templates/fluxcd.yaml \
		-e '/timoni/d' \
		-e 's|\.cluster\.local\.,||g' -e 's|\.cluster\.local\,||g' -e 's|\.cluster\.local\.||g' \
		-e '/value: .svc/a \            {{- include "cozy.kubernetes_envs" . | nindent 12 }}' \
		-e '/hostNetwork: true/i \      dnsPolicy: ClusterFirstWithHostNet'

update-new:
	timoni bundle build -f flux-aio.cue > ../../../internal/fluxinstall/manifests/fluxcd.yaml
	yq eval '(select(.kind == "Namespace") | .metadata.labels."pod-security.kubernetes.io/enforce") = "privileged"' -i ../../../internal/fluxinstall/manifests/fluxcd.yaml
	sed -i ../../../internal/fluxinstall/manifests/fluxcd.yaml \
		-e '/timoni/d' \
		-e 's|\.cluster\.local\.,||g' -e 's|\.cluster\.local\,||g' -e 's|\.cluster\.local\.||g'
	# TODO: solve dns issue with hostNetwork for installing helmreleases in tenant k8s clusters
	#-e '/hostNetwork: true/i \      dnsPolicy: ClusterFirstWithHostNet' 
