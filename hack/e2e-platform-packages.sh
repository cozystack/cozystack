#!/bin/sh
set -eu

api_server_endpoint=${COZY_APISERVER_ENDPOINT:-https://192.168.123.10:6443}
linstor_drbd_enabled=${COZY_LINSTOR_DRBD_ENABLED:-true}

# Container-mode Talos shares the runner kernel and cannot load DRBD. Keep the
# normal package graph for QEMU, but let that lane replace only LINSTOR with an
# otherwise-identical Package whose logger sidecar is disabled.
case "$linstor_drbd_enabled" in
  true|false) ;;
  *)
    echo "COZY_LINSTOR_DRBD_ENABLED must be true or false, got: $linstor_drbd_enabled" >&2
    exit 2
    ;;
esac

cat <<EOF
apiVersion: cozystack.io/v1alpha1
kind: Package
metadata:
  name: cozystack.cozystack-platform
spec:
  variant: isp-full
  components:
    platform:
      values:
        networking:
          podCIDR: "10.244.0.0/16"
          podGateway: "10.244.0.1"
          serviceCIDR: "10.96.0.0/16"
          joinCIDR: "100.64.0.0/16"
        publishing:
          host: "example.org"
          apiServerEndpoint: "$api_server_endpoint"
        bundles:
          enabledPackages:
            - cozystack.external-dns-application
EOF

if [ "$linstor_drbd_enabled" = false ]; then
  cat <<'EOF'
          disabledPackages:
            - cozystack.linstor
---
apiVersion: cozystack.io/v1alpha1
kind: Package
metadata:
  name: cozystack.linstor
spec:
  variant: default
  components:
    linstor:
      values:
        drbd:
          enabled: false
EOF
fi
