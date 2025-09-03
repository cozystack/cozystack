NAME=cozystack-api
NAMESPACE=cozy-system

include ../../../scripts/common-envs.mk
include ../../../scripts/package.mk

image: image-cozystack-api

image-cozystack-api:
	docker buildx build -f images/cozystack-api/Dockerfile ../../.. \
		--provenance false \
		--builder=$(BUILDER) \
		--platform=$(PLATFORM) \
		--tag $(REGISTRY)/cozystack-api:$(call settag,$(TAG)) \
		--cache-from type=registry,ref=$(REGISTRY)/cozystack-api:latest \
		--cache-to type=inline \
		--metadata-file images/cozystack-api.json \
		--push=$(PUSH) \
		--label "org.opencontainers.image.source=https://github.com/cozystack/cozystack" \
		--load=$(LOAD)
	IMAGE="$(REGISTRY)/cozystack-api:$(call settag,$(TAG))@$$(yq e '."containerimage.digest"' images/cozystack-api.json -o json -r)" \
		yq -i '.cozystackAPI.image = strenv(IMAGE)' values.yaml
	rm -f images/cozystack-api.json

generate:
	rm -rf openapi-schemas values-external
	mkdir -p openapi-schemas values-external

	find ../../apps ../../extra -maxdepth 2 -name values.schema.json -exec sh -ec 'ln -s ../{} openapi-schemas/$$(basename $$(dirname {})).json' \;

	find ../../apps ../../extra -maxdepth 2 -name values.yaml \
		-exec sh -eu -c ' \
			for f in "$$@"; do \
				d=$$(dirname "$$f"); \
				app_dir=$$(basename "$$d"); \
				app_name=""; \
				case "$$app_dir" in \
					bucket)           app_name="buckets" ;; \
					clickhouse)       app_name="clickhouses" ;; \
					ferretdb)         app_name="ferretdbs" ;; \
					http-cache)       app_name="httpcaches" ;; \
					kafka)            app_name="kafkas" ;; \
					kubernetes)       app_name="kuberneteses" ;; \
					mysql)            app_name="mysqls" ;; \
					nats)             app_name="natses" ;; \
					postgres)         app_name="postgreses" ;; \
					rabbitmq)         app_name="rabbitmqs" ;; \
					redis)            app_name="redises" ;; \
					seaweedfs)        app_name="seaweedfses" ;; \
					tcp-balancer)     app_name="tcpbalancers" ;; \
					virtual-machine)  app_name="virtualmachines" ;; \
					vm-disk)          app_name="vmdisks" ;; \
					vm-instance)      app_name="vminstances" ;; \
					info)             app_name="infos" ;; \
					monitoring)       app_name="monitorings" ;; \
					tenant)           app_name="tenants" ;; \
					ingress)          app_name="ingresses" ;; \
					etcd)             app_name="etcds" ;; \
					bootbox)          app_name="bootboxes" ;; \
					vpn)              app_name="vpns" ;; \
				esac; \
				if [ -z "$$app_name" ]; then \
					echo "skip: $$app_dir (no mapping)"; \
					continue; \
				fi; \
				echo "$$app_dir -> $$app_name"; \
				ln -sf ../"$$f" "values-external/$$app_name.yaml"; \
			done \
		' sh {} +
