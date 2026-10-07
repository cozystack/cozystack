#!/bin/sh
set -eu

api_server_endpoint=${COZY_APISERVER_ENDPOINT:-https://192.168.123.10:6443}
linstor_drbd_enabled=${COZY_LINSTOR_DRBD_ENABLED:-true}
storage_backend=${COZY_STORAGE_BACKEND:-linstor}

# Container-mode Talos shares the runner kernel and cannot load DRBD. Keep the
# normal package graph for QEMU, and let that lane replace two Packages instead:
# LINSTOR, otherwise identical with its logger sidecar disabled, and kubevirt-cdi,
# which is NOT identical -- it raises the importer memory limit from CDI's 600M
# to 4Gi and the request from 60M to 256Mi, for the reason stated above that
# override below.
#
# The consequence is worth stating where it is made, and it applies to both:
# the merge-gating lane exercises neither the shipped CDI default nor
# `drbd.enabled: true`. Whether a tenant worker disk imports at 600M, and
# whether the DRBD path works at all, are answered by the QEMU lanes
# (nightly.yaml, e2e-tag.yaml), which take no override, and not here.
case "$linstor_drbd_enabled" in
  true|false) ;;
  *)
    echo "COZY_LINSTOR_DRBD_ENABLED must be true or false, got: $linstor_drbd_enabled" >&2
    exit 2
    ;;
esac

# The storage control plane the cluster installs, passed to the platform as
# storage.backend. Blockstor creates its zpool on the QEMU lane's private
# /dev/vdc, which the container lane does not have, so the pair is refused
# rather than installed into a prep that cannot finish.
case "$storage_backend" in
  linstor) ;;
  blockstor)
    if [ "$linstor_drbd_enabled" = false ]; then
      echo "COZY_STORAGE_BACKEND=blockstor is not supported with COZY_LINSTOR_DRBD_ENABLED=false" >&2
      exit 2
    fi
    ;;
  *)
    echo "COZY_STORAGE_BACKEND must be linstor or blockstor, got: $storage_backend" >&2
    exit 2
    ;;
esac

# Only when asked: the linstor default renders the same Package it always has.
storage_values=
if [ "$storage_backend" = blockstor ]; then
  storage_values='        storage:
          backend: blockstor
'
fi

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
${storage_values}        bundles:
          enabledPackages:
            - cozystack.external-dns-application
EOF

if [ "$linstor_drbd_enabled" = false ]; then
  cat <<'EOF'
          disabledPackages:
            - cozystack.linstor
            - cozystack.kubevirt-cdi
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
---
# CDI's own 600M ceiling was seen to OOM the decompress+convert of a tenant
# worker disk near 100% on this lane, after which CDI retries from scratch and
# the DataVolume cycles forever instead of failing. It is an override here and
# not a chart default because the same import completes at 600M on a QEMU-node
# cluster, so what has been shown to need the headroom is this substrate, not
# the product. Move it into packages/system/kubevirt-cdi/values.yaml the day it
# reproduces somewhere that is not a container lane.
apiVersion: cozystack.io/v1alpha1
kind: Package
metadata:
  name: cozystack.kubevirt-cdi
spec:
  variant: default
  components:
    kubevirt-cdi:
      values:
        importerResources:
          requests:
            cpu: 100m
            memory: 256Mi
          limits:
            cpu: 750m
            memory: 4Gi
EOF
fi
