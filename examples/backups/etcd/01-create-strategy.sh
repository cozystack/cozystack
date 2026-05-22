#!/bin/bash
# Step 01: Apply the cluster-scoped Etcd strategy. Admin operation -
# tenants do not author the strategy CR directly. The strategy carries
# the templated EtcdBackup destination shape; the BackupClass created in
# step 02 supplies bucket/endpoint/credentials per tenant via parameters.
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
source "$SCRIPT_DIR/00-helpers.sh"

print_header "Step 01: Apply Etcd strategy '${STRATEGY_NAME}'"

# The strategy template is rendered per-BackupJob with the live
# .Application and .Parameters context. Tenants pick up the per-app
# credentials Secret via the deterministic name
# "{{ .Application.metadata.name }}-etcd-backup-creds" that step 02
# materialises for the source app.
kubectl apply -f - <<EOF
apiVersion: strategy.backups.cozystack.io/v1alpha1
kind: Etcd
metadata:
  name: ${STRATEGY_NAME}
spec:
  template:
    destination:
      s3:
        bucket: "{{ .Parameters.bucket }}"
        endpoint: "{{ .Parameters.endpoint }}"
        key: "{{ .Application.metadata.name }}/"
        region: "{{ .Parameters.region }}"
        forcePathStyle: true
        credentialsSecretRef:
          name: "{{ .Application.metadata.name }}-etcd-backup-creds"
EOF

log_success "Etcd strategy '${STRATEGY_NAME}' applied."
echo -e "\n${GREEN}${BOLD}Next:${NC} ./02-create-bucket.sh"
