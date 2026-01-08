NAME=platform
NAMESPACE=cozy-system

show:
	cozyhr show -n $(NAMESPACE) $(NAME) --plain

apply:
	cozyhr show -n $(NAMESPACE) $(NAME) --plain | kubectl apply -f-
	kubectl delete helmreleases.helm.toolkit.fluxcd.io -l cozystack.io/marked-for-deletion=true -A

reconcile: apply

namespaces-show:
	cozyhr show -n $(NAMESPACE) $(NAME) --plain -s templates/namespaces.yaml

namespaces-apply:
	cozyhr show -n $(NAMESPACE) $(NAME) --plain -s templates/namespaces.yaml | kubectl apply -f-

diff:
	cozyhr show -n $(NAMESPACE) $(NAME) --plain | kubectl diff -f-
