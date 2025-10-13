export NAME=objectstorage-controller
export NAMESPACE=cozy-$(NAME)

include ../../../scripts/common-envs.mk
include ../../../scripts/package.mk

update:
	rm -rf templates
	mkdir -p templates
	kubectl kustomize github.com/kubernetes-sigs/container-object-storage-interface > templates/controller.yaml
	sed -i 's/namespace: container-object-storage-system/namespace: {{ .Release.Namespace }}/g' templates/controller.yaml
	sed -i 's|image:.*|image: {{ .Values.objectstorage.controller.image }}|' templates/controller.yaml

image: image-controller image-sidecar
image-controller image-sidecar:
	$(eval TARGET      := $(subst image-,,$@))
	$(eval VALUES_FILE := $(if $(filter sidecar,$(TARGET)),../seaweedfs/values.yaml,values.yaml))
	$(eval YAML_PATH   := $(if $(filter sidecar,$(TARGET)),.seaweedfs.cosi.sidecar.image,.objectstorage.controller.image))
	docker buildx build images/objectstorage \
		--target $(TARGET) \
		--tag $(REGISTRY)/objectstorage-$(TARGET):$(call settag,$(TAG)) \
		--cache-from type=registry,ref=$(REGISTRY)/objectstorage-$(TARGET):latest \
		--cache-to   type=inline \
		--metadata-file images/$(TARGET).json \
		$(BUILDX_ARGS)
	IMAGE="$(REGISTRY)/objectstorage-$(TARGET):$(call settag,$(TAG))@$$(yq e '."containerimage.digest"' images/$(TARGET).json -r)" \
		yq -i '$(YAML_PATH) = strenv(IMAGE)' $(VALUES_FILE)
	rm -f images/$(TARGET).json
	yq .seaweedfs.cosi.sidecar.image ../seaweedfs/values.yaml > ../../extra/seaweedfs/images/objectstorage-sidecar.tag
