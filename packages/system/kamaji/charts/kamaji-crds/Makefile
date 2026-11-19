docs: HELMDOCS_VERSION := v1.8.1
docs: docker
	@docker run --rm -v "$$(pwd):/helm-docs" -u $$(id -u) jnorwood/helm-docs:$(HELMDOCS_VERSION)

docker:
	@hash docker 2>/dev/null || {\
		echo "You need docker" &&\
		exit 1;\
	}
