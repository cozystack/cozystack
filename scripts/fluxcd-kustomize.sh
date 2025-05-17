#!/bin/sh
# This scripts adds common fluxcd labels to all objects

if [ -z "$NAME" ]; then
  echo 'Variable $NAME is not set!' >&2
  exit 1
fi

if [ -z "$NAMESPACE" ]; then
  echo 'Variable $NAMESPACE is not set!' >&2
  exit 1
fi

TMP_DIR=$(mktemp -d)
cat - > "${TMP_DIR}/helm-generated-output.yaml"
cat > "${TMP_DIR}/global-labels.yaml" <<EOT
apiVersion: builtin
kind: LabelTransformer
metadata:
  name: global-labels
labels:
  helm.toolkit.fluxcd.io/name: ${NAME}
  helm.toolkit.fluxcd.io/namespace: ${NAMESPACE:-$HELM_NAMESPACE}
fieldSpecs:
- path: metadata/labels
  create: true
EOT
cat > "${TMP_DIR}/kustomization.yaml" <<EOT
resources:
- helm-generated-output.yaml
apiVersion: kustomize.config.k8s.io/v1beta1
kind: Kustomization
transformers:
- global-labels.yaml
EOT
kubectl kustomize "${TMP_DIR}"
rm -rf "${TMP_DIR}"
