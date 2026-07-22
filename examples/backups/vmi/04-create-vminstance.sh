#!/bin/bash
# Step 04: Create a VMInstance using the previously created VMDisk
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
source "$SCRIPT_DIR/00-helpers.sh"

print_header "Step 4: Create VMInstance"

log_step "Creating VMInstance 'test' in namespace $NAMESPACE..."
log_command "kubectl apply -f - (VMInstance: test)"

kubectl apply -f - <<EOF
apiVersion: apps.cozystack.io/v1alpha1
kind: VMInstance
metadata:
  name: test
  namespace: ${NAMESPACE}
spec:
  disks:
    - name: ubuntu-source
  instanceProfile: ubuntu
  instanceType: "u1.medium"
  running: true
  sshKeys:
    #- <paste your ssh public key here>
  external: false
  externalMethod: PortList
  externalPorts:
    - 22
EOF

log_success "VMInstance created"

separator

# Wait for the VM to be fully booted before backing it up. Backing up a VM that
# is still importing its disk or has not started yields an incomplete, useless
# backup — so, like the etcd/postgres source steps, block on readiness here.
# The Ubuntu cloud image download (~700MB) plus nested KubeVirt boot is slow,
# hence the generous 900s bound.
log_step "Waiting for the VMInstance to boot (HelmRelease + VirtualMachine Ready)..."
log_command "kubectl -n $NAMESPACE wait hr vm-instance-test --for=condition=ready"
kubectl -n "$NAMESPACE" wait hr vm-instance-test --for=condition=ready --timeout=900s
log_command "kubectl -n $NAMESPACE wait virtualmachine.kubevirt.io/vm-instance-test --for=condition=Ready"
kubectl -n "$NAMESPACE" wait virtualmachine.kubevirt.io/vm-instance-test \
    --for=condition=Ready --timeout=600s

separator

log_step "Verifying VMInstance..."
log_command "kubectl get vminstance test -n $NAMESPACE"
kubectl get vminstance test -n "$NAMESPACE"

separator

log_success "VMInstance is running and ready to back up"
echo -e "\n${GREEN}${BOLD}Next step:${NC} ./05-create-backupjob.sh"
