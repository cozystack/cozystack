#!/bin/sh
set -eu

# The container lane has no ARP VIP, so its API endpoint is a node address
# rather than the QEMU lane's 192.168.123.10. That is the only difference
# between the lanes this manifest carries: the Package graph is the same on
# both, and what the container lane cannot run -- DRBD above all -- is worked
# around by the install harness against the live cluster, because the charts
# carry no value that would switch it off.
api_server_endpoint=${COZY_APISERVER_ENDPOINT:-https://192.168.123.10:6443}

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
