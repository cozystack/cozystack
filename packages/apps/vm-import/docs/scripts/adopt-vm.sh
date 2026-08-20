#!/bin/bash
# adopt-vm.sh - Adopt an imported VM into Cozystack Helm management
#
# Usage: ./adopt-vm.sh <vm-name> <namespace> [instance-type] [profile]
#
# Example:
#   ./adopt-vm.sh web-server default u1.medium ubuntu

set -euo pipefail

# Colors for output
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
NC='\033[0m' # No Color

# Arguments
VM_NAME="${1:?$(echo -e "${RED}Error: VM name required${NC}\nUsage: $0 <vm-name> <namespace> [instance-type] [profile]")}"
NAMESPACE="${2:?$(echo -e "${RED}Error: Namespace required${NC}\nUsage: $0 <vm-name> <namespace> [instance-type] [profile]")}"
INSTANCE_TYPE="${3:-u1.medium}"
PROFILE="${4:-ubuntu}"

# The Cozystack vm-instance ApplicationDefinition prefixes every release with
# "vm-instance-", so the Helm release that adopts the VM must carry that name and
# the VM's ownership annotations must point at it. This mirrors the release name
# the vm-adoption-controller uses (main.go: "vm-instance-" + vmInstanceName).
RELEASE_NAME="vm-instance-${VM_NAME}"

OUTPUT_FILE="/tmp/adopt-${VM_NAME}-values.yaml"

echo -e "${GREEN}=== Cozystack VM Adoption Tool ===${NC}"
echo "VM Name: $VM_NAME"
echo "Namespace: $NAMESPACE"
echo "Instance Type: $INSTANCE_TYPE"
echo "Profile: $PROFILE"
echo ""

# Check if VM exists
echo -e "${YELLOW}[1/5]${NC} Checking if VM exists..."
if ! kubectl get vm "$VM_NAME" -n "$NAMESPACE" &>/dev/null; then
    echo -e "${RED}Error: VM '$VM_NAME' not found in namespace '$NAMESPACE'${NC}"
    exit 1
fi
echo -e "${GREEN}✓${NC} VM found"

# Read the VM once. An auth or API failure must abort rather than degrade to
# empty output, which would silently produce a wrong values file.
VM_JSON=$(kubectl get vm "$VM_NAME" -n "$NAMESPACE" -o json)

# Extract current configuration. runStrategy is authoritative; spec.running is
# the deprecated form and is only consulted when runStrategy is unset (the same
# precedence the adoption controller applies).
echo -e "${YELLOW}[2/5]${NC} Extracting current VM configuration..."
RUN_STRATEGY=$(jq -r '.spec.runStrategy // (if .spec.running == true then "Always" elif .spec.running == false then "Halted" else "Always" end)' <<<"$VM_JSON")
echo "  Run strategy: $RUN_STRATEGY"

# Check if VM has Forklift labels. Forklift (release-2.11+) stamps the bare
# `plan` key, whose value is the Plan UID rather than its name.
IS_IMPORTED=$(jq -r '.metadata.labels.plan // ""' <<<"$VM_JSON")
if [ -n "$IS_IMPORTED" ]; then
    echo -e "  ${GREEN}✓${NC} VM was imported via Forklift (plan UID: $IS_IMPORTED)"
else
    echo -e "  ${YELLOW}⚠${NC}  VM was not imported via Forklift (manual creation?)"
fi

# Find attached disks. Forklift backs them either by a DataVolume or by a PVC
# populated by the CDI volume populator, so both shapes must be discovered. The
# discovered value is the real DataVolume/PVC name; it is emitted verbatim as
# each disk's dvName so vm-instance resolves the existing volume instead of the
# default vm-disk-<name> convention (which would not exist and fail rendering).
echo -e "${YELLOW}[3/5]${NC} Discovering attached disks..."
DISK_NAMES=$(jq -r '.spec.template.spec.volumes[]? | (.dataVolume.name // .persistentVolumeClaim.claimName) // empty' <<<"$VM_JSON")

DISK_COUNT=0
if [ -z "$DISK_NAMES" ]; then
    echo -e "  ${YELLOW}⚠${NC}  No disks found attached to VM"
    DISKS_YAML="  # No disks found - add manually if needed"
else
    DISKS_YAML=""
    while IFS= read -r disk; do
        if [ -n "$disk" ]; then
            echo "  - $disk"
            # name is an internal, unique disk identifier (matches the
            # vm-adoption-controller's imported-<N> scheme); dvName points at the
            # actual Forklift DataVolume/PVC so no data is copied or renamed.
            DISKS_YAML="${DISKS_YAML}  - name: imported-${DISK_COUNT}
    dvName: ${disk}
    bus: virtio
"
            DISK_COUNT=$((DISK_COUNT + 1))
        fi
    done <<< "$DISK_NAMES"
    echo -e "  ${GREEN}✓${NC} Found $DISK_COUNT disk(s)"
fi

# Check for SSH keys
echo -e "${YELLOW}[4/5]${NC} Checking for SSH keys..."
SSH_SECRET="${VM_NAME}-ssh-keys"
if kubectl get secret "$SSH_SECRET" -n "$NAMESPACE" &>/dev/null; then
    echo -e "  ${GREEN}✓${NC} Found existing SSH keys secret: $SSH_SECRET"
    SSH_KEYS_YAML="  # SSH keys found in secret: $SSH_SECRET
  # Add additional keys here if needed"
else
    echo -e "  ${YELLOW}⚠${NC}  No SSH keys secret found"
    SSH_KEYS_YAML="  # No SSH keys found - add your public keys here"
fi

# Check for networks
echo -e "  Checking network configuration..."
NETWORKS=$(jq -r '.spec.template.spec.networks[]? | select(.multus) | .multus.networkName' <<<"$VM_JSON")

if [ -n "$NETWORKS" ]; then
    echo -e "  ${GREEN}✓${NC} Found Multus networks:"
    SUBNETS_YAML="subnets:"
    while IFS= read -r net; do
        if [ -n "$net" ]; then
            echo "  - $net"
            # Extract subnet name (remove namespace prefix if present)
            SUBNET_NAME=$(echo "$net" | sed 's/^.*\///')
            SUBNETS_YAML="${SUBNETS_YAML}
  - name: ${SUBNET_NAME}"
        fi
    done <<< "$NETWORKS"
else
    echo "  No Multus networks found (using pod network)"
    SUBNETS_YAML="# No additional subnets (using pod network only)"
fi

# Generate values file
echo -e "${YELLOW}[5/5]${NC} Generating Helm values file..."
cat > "$OUTPUT_FILE" <<EOF
# Cozystack vm-instance values for adopting: $VM_NAME
# Generated by adopt-vm.sh on $(date)
#
# Before applying, label/annotate the existing VM for Helm ownership (see the
# "Next Steps" printed by this script), then apply with:
#   helm upgrade --install "$RELEASE_NAME" cozystack/vm-instance \\
#     --namespace "$NAMESPACE" \\
#     --values $OUTPUT_FILE

# === Basic Configuration ===
# fullnameOverride pins the rendered VirtualMachine name to the existing VM so
# Helm adopts it in place instead of rendering a second, differently named VM.
fullnameOverride: ${VM_NAME}
runStrategy: ${RUN_STRATEGY}

# === Instance Configuration ===
instanceType: ${INSTANCE_TYPE}
instanceProfile: ${PROFILE}

# Alternatively, specify custom resources:
# resources:
#   cpu: 4
#   memory: 8Gi
#   sockets: 1

# === Disks ===
disks:
${DISKS_YAML}

# === Network Configuration ===
${SUBNETS_YAML}

# External access (LoadBalancer service)
external: false
# externalPorts:
#   - 22
#   - 80
#   - 443

# === Access Credentials ===
sshKeys:
${SSH_KEYS_YAML}

# === Cloud-Init ===
# IMPORTANT: Avoid re-running cloud-init on adopted VMs
# as it may reset networking or other configuration.
# Only enable if you need to reconfigure the VM.
cloudInit: ""

# Keep the seed EMPTY so vm-instance reuses the existing VM's firmware UUID
# instead of deriving a new one. A changed UUID makes cloud-init treat the guest
# as a fresh instance and re-run it. Set a non-empty seed only when you
# deliberately want cloud-init to re-execute.
cloudInitSeed: ""

# === GPU Configuration ===
# gpus:
#   - name: gpu1
#     deviceName: "nvidia.com/GP102GL_Tesla_P40"

# === Advanced Options ===
# Enable if VM was migrated from Windows
# This sets proper node affinity for Windows VMs
# windowsVM: false
EOF

echo -e "${GREEN}✓${NC} Values file generated: $OUTPUT_FILE"
echo ""

# Display summary
echo -e "${GREEN}=== Adoption Summary ===${NC}"
echo "VM: $VM_NAME (namespace: $NAMESPACE)"
echo "Run strategy: $RUN_STRATEGY"
echo "Disks: $DISK_COUNT"
echo "Values file: $OUTPUT_FILE"
echo ""

# Instructions
echo -e "${GREEN}=== Next Steps ===${NC}"
echo "1. Review and edit the values file:"
echo -e "   ${YELLOW}vim $OUTPUT_FILE${NC}"
echo ""
echo "2. Give the existing VM Helm ownership metadata so Helm ADOPTS it instead"
echo "   of failing with \"already exists\" or rendering a duplicate VM:"
echo -e "   ${YELLOW}kubectl label vm \"$VM_NAME\" -n \"$NAMESPACE\" \\${NC}"
echo -e "   ${YELLOW}     app.kubernetes.io/managed-by=Helm${NC}"
echo -e "   ${YELLOW}kubectl annotate vm \"$VM_NAME\" -n \"$NAMESPACE\" \\${NC}"
echo -e "   ${YELLOW}     meta.helm.sh/release-name=\"$RELEASE_NAME\" \\${NC}"
echo -e "   ${YELLOW}     meta.helm.sh/release-namespace=\"$NAMESPACE\"${NC}"
echo ""
echo "3. Apply the adoption (release name must match the annotation above):"
echo -e "   ${YELLOW}helm upgrade --install \"$RELEASE_NAME\" cozystack/vm-instance \\${NC}"
echo -e "   ${YELLOW}     --namespace \"$NAMESPACE\" \\${NC}"
echo -e "   ${YELLOW}     --values $OUTPUT_FILE${NC}"
echo ""
echo "4. Verify the adoption:"
echo -e "   ${YELLOW}helm list -n \"$NAMESPACE\"${NC}"
echo -e "   ${YELLOW}kubectl get vm \"$VM_NAME\" -n \"$NAMESPACE\" -o yaml | grep -A 5 'app.kubernetes.io/managed-by'${NC}"
echo ""
echo -e "${YELLOW}⚠  Important:${NC} The VM and its disks will NOT be modified during adoption."
echo "   The Helm release will manage future changes via declarative values."
echo ""
