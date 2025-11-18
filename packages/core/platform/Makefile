NAME=platform
NAMESPACE=cozy-system

show:
	cozypkg show -n $(NAMESPACE) $(NAME) --plain

apply:
	cozypkg show -n $(NAMESPACE) $(NAME) --plain | kubectl apply -f-

reconcile: apply

namespaces-show:
	cozypkg show -n $(NAMESPACE) $(NAME) --plain -s templates/namespaces.yaml

namespaces-apply:
	cozypkg show -n $(NAMESPACE) $(NAME) --plain -s templates/namespaces.yaml | kubectl apply -f-

diff:
	cozypkg show -n $(NAMESPACE) $(NAME) --plain | kubectl diff -f-
