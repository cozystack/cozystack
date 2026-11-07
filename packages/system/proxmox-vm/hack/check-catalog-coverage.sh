#!/usr/bin/env bash
# Every KubeVirt instance type and preference name the platform ships must
# mean something on Proxmox too, so that a values file that works on the
# kubevirt substrate works on a platform with `vmSubstrate: proxmox`. Architectures this
# hypervisor cannot run (arm64, s390x) are the one exception: on KubeVirt such
# a machine is just as unschedulable on an x86 cluster.
set -euo pipefail
cd "$(dirname "$0")/.."
src=../kubevirt-instancetypes/templates
status=0
missing=$(comm -23 \
  <(yq -r 'select(.kind=="VirtualMachineClusterPreference") | .metadata.name' $src/preferences.yaml | grep -vE '^---$|^$|\.(arm64|s390x)$' | sort -u) \
  <(yq -r '.catalog.profiles | keys | .[]' values.yaml | grep -v '^_' | sort -u))
if [ -n "$missing" ]; then echo "preferences without a catalog profile:"; echo "$missing"; status=1; fi
missing=$(comm -23 \
  <(yq -r 'select(.kind=="VirtualMachineClusterInstancetype") | .metadata.name' $src/instancetypes.yaml | grep -vE '^---$|^$' | sort -u) \
  <(yq -r 'keys | .[]' files/instancetypes.yaml | sort -u))
if [ -n "$missing" ]; then echo "instance types without a catalog entry (run make instancetypes):"; echo "$missing"; status=1; fi
exit $status
