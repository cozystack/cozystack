#!/bin/sh
set -e

if kubectl get secret -n cozy-system cozystack-assets-tls >/dev/null 2>&1 && kubectl get secret -n cozy-public cozystack-assets-tls >/dev/null 2>&1; then
  echo "Secret cozystack-assets-tls already exists in both cozy-system and cozy-public namespaces. Exiting."
  exit 0
fi

USER_CN="cozystack-assets-reader"
CSR_NAME="csr-${USER_CN}-$(date +%s)"

# make temp directory and cleanup handler
TMPDIR=$(mktemp -d)
trap 'rm -rf "$TMPDIR"' EXIT

# move into tmpdir
cd "$TMPDIR"

openssl genrsa -out tls.key 2048
openssl req -new -key tls.key -subj "/CN=${USER_CN}" -out tls.csr

CSR_B64=$(base64 < tls.csr | tr -d '\n')

cat <<EOF | kubectl apply -f -
apiVersion: certificates.k8s.io/v1
kind: CertificateSigningRequest
metadata:
  name: ${CSR_NAME}
spec:
  signerName: kubernetes.io/kube-apiserver-client
  request: ${CSR_B64}
  usages:
    - client auth
EOF

kubectl certificate approve "${CSR_NAME}"

echo "Waiting for .status.certificate..."
kubectl wait csr "${CSR_NAME}" \
  --for=jsonpath='{.status.certificate}' \
  --timeout=120s

kubectl get csr "${CSR_NAME}" \
  -o jsonpath='{.status.certificate}' | base64 -d > tls.crt

kubectl get -n kube-public configmap kube-root-ca.crt \
  -o jsonpath='{.data.ca\.crt}' > ca.crt

kubectl create secret generic "cozystack-assets-tls" \
  --namespace='cozy-system' \
  --type='kubernetes.io/tls' \
  --from-file=tls.crt \
  --from-file=tls.key \
  --from-file=ca.crt \
  --dry-run=client -o yaml | kubectl apply -f -

kubectl create secret generic "cozystack-assets-tls" \
  --namespace='cozy-public' \
  --type='kubernetes.io/tls' \
  --from-file=tls.crt \
  --from-file=tls.key \
  --from-file=ca.crt \
  --dry-run=client -o yaml | kubectl apply -f -
