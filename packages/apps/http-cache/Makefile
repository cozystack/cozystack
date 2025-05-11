NGINX_CACHE_TAG = $(shell awk '$$1 == "version:" {print $$2}' Chart.yaml)

include ../../../scripts/common-envs.mk
include ../../../scripts/package.mk

image: image-nginx

image-nginx:
	docker buildx build images/nginx-cache \
		--provenance false \
		--builder=$(BUILDER) \
		--platform=$(PLATFORM) \
		--tag $(REGISTRY)/nginx-cache:$(call settag,$(NGINX_CACHE_TAG)) \
		--cache-from type=registry,ref=$(REGISTRY)/nginx-cache:latest \
		--cache-to type=inline \
		--metadata-file images/nginx-cache.json \
		--push=$(PUSH) \
		--label "org.opencontainers.image.source=https://github.com/cozystack/cozystack" \
		--load=$(LOAD)
	echo "$(REGISTRY)/nginx-cache:$(call settag,$(NGINX_CACHE_TAG))@$$(yq e '."containerimage.digest"' images/nginx-cache.json -o json -r)" \
		> images/nginx-cache.tag
	rm -f images/nginx-cache.json

generate:
	readme-generator -v values.yaml -s values.schema.json -r README.md

update:
	tag=$$(git ls-remote --tags --sort="v:refname" https://github.com/chrislim2888/IP2Location-C-Library | awk -F'[/^]' 'END{print $$3}') && \
	sed -i "/^ARG IP2LOCATION_C_VERSION=/ s/=.*/=$$tag/" images/nginx/Dockerfile
	tag=$$(git ls-remote --tags --sort="v:refname" https://github.com/ip2location/ip2proxy-c | awk -F'[/^]' 'END{print $$3}') && \
	sed -i "/^ARG IP2PROXY_C_VERSION=/ s/=.*/=$$tag/" images/nginx/Dockerfile
	tag=$$(git ls-remote --tags --sort="v:refname" https://github.com/ip2location/ip2location-nginx | awk -F'[/^]' 'END{print $$3}') && \
	sed -i "/^ARG IP2LOCATION_NGINX_VERSION=/ s/=.*/=$$tag/" images/nginx/Dockerfile
	tag=$$(git ls-remote --tags --sort="v:refname" https://github.com/ip2location/ip2proxy-nginx | awk -F'[/^]' 'END{print $$3}') && \
	sed -i "/^ARG IP2PROXY_NGINX_VERSION=/ s/=.*/=$$tag/" images/nginx/Dockerfile
	tag=$$(git ls-remote --tags --sort="v:refname" https://github.com/nginx/nginx | awk -F'[/^]' 'END{print $$3}' | awk -F- '{print $$2}') && \
	sed -i "/^ARG NGINX_VERSION=/ s/=.*/=$$tag/" images/nginx/Dockerfile
	tag=$$(git ls-remote --tags --sort="v:refname" https://github.com/nginx-modules/ngx_cache_purge | awk -F'[/^]' 'END{print $$3}') && \
	sed -i "/^ARG NGINX_CACHE_PURGE_VERSION=/ s/=.*/=$$tag/" images/nginx/Dockerfile
	tag=$$(git ls-remote --tags --sort="v:refname" https://github.com/vozlt/nginx-module-vts | awk -F'[/^]' 'END{print $$3}' | sed 's/^v//') && \
	sed -i "/^ARG NGINX_VTS_VERSION=/ s/=.*/=$$tag/" images/nginx/Dockerfile
	tag=$$(git ls-remote --tags --sort="v:refname" https://github.com/51Degrees/Device-Detection | awk -F'[/^]' 'END{print $$3}' | sed 's/^v//') && \
	sed -i "/^ARG FIFTYONEDEGREES_NGINX_VERSION=/ s/=.*/=$$tag/" images/nginx/Dockerfile
