#!/bin/sh
set -eu

api_server_endpoint=${COZY_APISERVER_ENDPOINT:-https://192.168.123.10:6443}
linstor_drbd_enabled=${COZY_LINSTOR_DRBD_ENABLED:-true}

# Container-mode Talos shares the runner kernel and cannot load DRBD. Keep the
# normal package graph for QEMU, and let that lane replace two Packages instead:
# LINSTOR, otherwise identical with its logger sidecar disabled, and kubevirt-cdi,
# which is NOT identical -- it raises the importer memory limit from CDI's 600M
# to 4Gi and the request from 60M to 256Mi, for the reason stated above that
# override below.
#
# The consequence is worth stating where it is made, and it applies to both:
# the merge-gating lane exercises neither the shipped CDI default nor
# `drbd.enabled: true`. Every e2e lane runs on containers, so whether a
# tenant worker disk imports at 600M, and whether the DRBD path works at all,
# is answered by no CI lane.
case "$linstor_drbd_enabled" in
  true|false) ;;
  *)
    echo "COZY_LINSTOR_DRBD_ENABLED must be true or false, got: $linstor_drbd_enabled" >&2
    exit 2
    ;;
esac

# Packages left out of this install. The container lane always drops the two it
# replaces below; COZY_DISABLED_PACKAGES adds the complement computed by
# hack/select-install.sh for the selected suites. Both land under one
# disabledPackages key, so the list is merged here rather than emitted twice.
disabled=""
if [ "$linstor_drbd_enabled" = false ]; then
  disabled="cozystack.linstor cozystack.kubevirt-cdi"
fi
for pkg in ${COZY_DISABLED_PACKAGES:-}; do
  case "$pkg" in
    cozystack.*[!a-z0-9.-]* | cozystack.)
      echo "COZY_DISABLED_PACKAGES: invalid package name: $pkg" >&2
      exit 2
      ;;
    cozystack.?*) ;;
    *)
      echo "COZY_DISABLED_PACKAGES: invalid package name: $pkg" >&2
      exit 2
      ;;
  esac
  case " $disabled " in
    *" $pkg "*) ;;
    *) disabled="${disabled:+$disabled }$pkg" ;;
  esac
done

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
        # Must match Tenant/root spec.monitoring, which the install sets to
        # false. Left true, cozystack-basics renders Services into
        # cozy-monitoring, which a narrow suite selection never creates.
        monitoring:
          rootEnabled: false
        bundles:
          enabledPackages:
            - cozystack.external-dns-application
            # Opt-in in the iaas bundle; the proxmox-network suite needs it.
            # A run that does not select that suite gets it in
            # COZY_DISABLED_PACKAGES from hack/select-install.sh, which wins.
            - cozystack.proxmox-network
EOF

if [ -n "$disabled" ]; then
  echo "          disabledPackages:"
  for pkg in $disabled; do
    echo "            - $pkg"
  done
fi

if [ "$linstor_drbd_enabled" = false ]; then
  cat <<'EOF'
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
