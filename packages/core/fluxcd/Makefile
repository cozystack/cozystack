NAME=fluxcd
NAMESPACE=cozy-$(NAME)

show:
	kubectl apply -R -f manifests/ --dry-run=client -o yaml

apply:
	kubectl apply -R -f manifests/ --server-side --force-conflicts

diff:
	kubectl diff -R -f manifests/

update:
	timoni bundle build -f flux-aio.cue > manifests/fluxcd.yaml
	sed -e 's|\.cluster\.local\.,||g' -e 's|\.cluster\.local\,||g' -e 's|\.cluster\.local\.||g' -e '/timoni/d' -i manifests/fluxcd.yaml
	yq eval '(select(.kind == "Namespace") | .metadata.labels."pod-security.kubernetes.io/enforce") = "privileged"' -i manifests/fluxcd.yaml
