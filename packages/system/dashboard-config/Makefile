NAME := cozy-dashboard
NAMESPACE := default

help: ## Show this help.
	@awk 'BEGIN {FS = ":.*?## "} /^[a-zA-Z_-]+:.*?## / {sub("\\\\n",sprintf("\n%22c"," "), $$2);printf "\033[36m%-20s\033[0m %s\n", $$1, $$2}' $(MAKEFILE_LIST)

show: check ## Show output of rendered templates
	cozypkg show -n $(NAMESPACE) $(NAME) --plain

apply: check ## Apply Helm release to a Kubernetes cluster
	cozypkg apply -n $(NAMESPACE) $(NAME) --plain

diff: check ## Diff Helm release against objects in a Kubernetes cluster
	cozypkg diff -n $(NAMESPACE) $(NAME) --plain

delete: check ## Delete Helm release from a Kubernetes cluster
	cozypkg delete -n $(NAMESPACE) $(NAME) --plain

check:
	@if [ -z "$(NAME)" ]; then echo "env NAME is not set!" >&2; exit 1; fi
	@if [ -z "$(NAMESPACE)" ]; then echo "env NAMESPACE is not set!" >&2; exit 1; fi


