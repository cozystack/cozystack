.DEFAULT_GOAL=help
.PHONY=help show diff apply delete update image

help: ## Show this help.
	@awk 'BEGIN {FS = ":.*?## "} /^[a-zA-Z_-]+:.*?## / {sub("\\\\n",sprintf("\n%22c"," "), $$2);printf "\033[36m%-20s\033[0m %s\n", $$1, $$2}' $(MAKEFILE_LIST)

show: check ## Show output of rendered templates
	cozypkg show -n $(NAMESPACE) $(NAME)

apply: check suspend ## Apply Helm release to a Kubernetes cluster
	cozypkg apply -n $(NAMESPACE) $(NAME)

diff: check ## Diff Helm release against objects in a Kubernetes cluster
	cozypkg diff -n $(NAMESPACE) $(NAME)

suspend: check ## Suspend reconciliation for an existing Helm release
	cozypkg suspend -n $(NAMESPACE) $(NAME)

resume: check ## Resume reconciliation for an existing Helm release
	cozypkg resume -n $(NAMESPACE) $(NAME)

delete: check suspend ## Delete Helm release from a Kubernetes cluster
	cozypkg delete -n $(NAMESPACE) $(NAME)

check:
	@if [ -z "$(NAME)" ]; then echo "env NAME is not set!" >&2; exit 1; fi
	@if [ -z "$(NAMESPACE)" ]; then echo "env NAMESPACE is not set!" >&2; exit 1; fi

clean:
	rm -rf charts/

%-update:
	helm repo add $(REPO_NAME) $(REPO_URL)
	helm repo update $(REPO_NAME)
	helm pull $(REPO_NAME)/$(CHART_NAME) --untar --untardir charts --version "$(CHART_VERSION)"
