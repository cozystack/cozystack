NAME=flux-aio
NAMESPACE=cozy-$(NAME)

include ../../../scripts/common-envs.mk

show:
	cozyhr show -n $(NAMESPACE) $(NAME) --plain

apply:
	cozyhr show -n $(NAMESPACE) $(NAME) --plain | kubectl apply -f- --server-side --force-conflicts

diff:
	cozyhr show -n $(NAMESPACE) $(NAME) --plain | kubectl diff -f-

update:
	timoni bundle build -f flux-aio.cue > templates/fluxcd.yaml
	yq eval '(select(.kind == "Namespace") | .metadata.labels."pod-security.kubernetes.io/enforce") = "privileged"' -i templates/fluxcd.yaml
	sed -i templates/fluxcd.yaml \
		-e '/timoni/d' \
		-e 's|\.cluster\.local\.,||g' -e 's|\.cluster\.local\,||g' -e 's|\.cluster\.local\.||g' \
		-e '/value: .svc/a \            {{- include "cozy.kubernetes_envs" . | nindent 12 }}' \
		-e '/hostNetwork: true/i \      dnsPolicy: ClusterFirstWithHostNet'
