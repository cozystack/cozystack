#!/bin/sh
# Builds the Proxmox VM template a `substrate: proxmox` kubernetes-nodes pool
# clones its workers from.
#
# The chart takes no image on that substrate: capmox clones whichever template
# carries the pool's proxmox.templateTags, and the pool's talos.* values reach
# only the installer written into the worker machineconfig. Nothing compares the
# two, so the template is built here from the same talos.version and
# talos.schematicID the chart renders, read from its values.yaml.
#
# Two images that look right are not:
# - the platform release's nocloud-amd64.raw.xz is the bare-metal node image.
#   Its zfs extension starts a service that waits for /dev/zfs, which a VM never
#   has, so the Talos boot sequence never completes and the guest is rebooted
#   when the boot timeout runs out (about 70 minutes on v1.13), over and over.
# - the openstack image the kubevirt substrate imports ignores the nocloud ISO
#   capmox hands the bootstrap data over on, and the worker never joins.
#
# The chart's default schematic carries qemu-guest-agent, whose service waits
# the same way for the agent's virtio port. Proxmox adds that port only to a VM
# with the agent enabled, so the template is created with it on.
#
# Usage:
#   hack/proxmox-talos-template.sh print  <vmid> <storage>
#   hack/proxmox-talos-template.sh create <vmid> <storage>
#
# print writes the program that create runs, for review. create runs it over
# ssh on COZY_PVE_SSH (user@host), or on this machine when that is unset, which
# then has to be a Proxmox VE node. The template is created on that node.
#
# Optional overrides:
#   COZY_TALOS_VERSION, COZY_TALOS_SCHEMATIC_ID, COZY_TALOS_FACTORY_URL
#     default to talos.version, talos.schematicID and talos.imageFactoryURL of
#     packages/apps/kubernetes-nodes/values.yaml. A pool that overrides those
#     needs a template built with the same values.
#   COZY_TEMPLATE_TAGS       comma-separated, default
#                            cozystack,talos,talos-<version>,schematic-<8 chars>
#   COZY_TEMPLATE_DISK_SIZE  default 20G. capmox clones the disk at the size the
#                            template has, so this is every worker's system disk.
#   COZY_TEMPLATE_CPU        default x86-64-v2-AES. Talos needs x86-64-v2, which
#                            the Proxmox default kvm64 does not provide.
#   COZY_TALOS_FACTORY_CA    path to a PEM file with the CA certificate(s) of a
#                            self-hosted factory or mirror served by an internal
#                            CA, the blocks a kubevirt pool puts in
#                            talos.imageFactoryCA. curl on the hypervisor trusts
#                            them on top of the system store, as CDI does, and
#                            the hypervisor's own trust store is left as it is.
#   COZY_PVE_SSH_KEY         default ~/.ssh/id_ed25519
set -eu

VALUES="$(cd "$(dirname "$0")/.." && pwd)/packages/apps/kubernetes-nodes/values.yaml"

die() { printf 'proxmox-talos-template: %s\n' "$*" >&2; exit 1; }

usage() {
  die "usage: $0 print|create <vmid> <storage>"
}

# The talos block of values.yaml is flat, two-space indented and quoted, so awk
# reads it without making yq a requirement of an operator's workstation.
chart_talos() {
  awk -v key="$1" '
    /^talos:/ { in_block = 1; next }
    in_block && /^[^ #]/ { exit }
    in_block && $1 == key ":" { v = $2; gsub(/"/, "", v); print v; exit }
  ' "$VALUES"
}

# Prints the certificate blocks of a PEM file and fails unless the file holds
# complete certificate blocks and nothing else: they are pasted into the root
# program, and a private key kept in the same file must not travel with them.
pem_certificates() {
  tr -d '\r' < "$1" | awk '
    { sub(/[ \t]+$/, "") }
    /^-----BEGIN CERTIFICATE-----$/ && !inside { inside = 1; body = 0; print; next }
    /^-----END CERTIFICATE-----$/ && inside && body { inside = 0; blocks++; print; next }
    inside && /^[A-Za-z0-9+\/=]+$/ { body = 1; print; next }
    !inside && /^$/ { next }
    { bad = 1; exit }
    END { exit (bad || inside || !blocks) }
  '
}

# grep -x matches line by line, so a value with a newline in it would pass on
# any one of its lines and carry the rest into the program as commands of their
# own. Such a value is refused before grep sees it.
matches() {
  case $1 in *'
'*) return 1 ;; esac
  printf '%s' "$1" | grep -Eqx "$2"
}

[ $# -eq 3 ] || usage
MODE=$1
VMID=$2
STORAGE=$3
case "$MODE" in print|create) ;; *) usage ;; esac

VERSION=${COZY_TALOS_VERSION:-$(chart_talos version)}
SCHEMATIC=${COZY_TALOS_SCHEMATIC_ID:-$(chart_talos schematicID)}
FACTORY=${COZY_TALOS_FACTORY_URL:-$(chart_talos imageFactoryURL)}
FACTORY=${FACTORY%/}
DISK_SIZE=${COZY_TEMPLATE_DISK_SIZE:-20G}
CPU=${COZY_TEMPLATE_CPU:-x86-64-v2-AES}
FACTORY_CA=${COZY_TALOS_FACTORY_CA:-}
TAGS=${COZY_TEMPLATE_TAGS:-cozystack,talos,talos-$VERSION,schematic-$(printf '%s' "$SCHEMATIC" | cut -c1-8)}

# Every value below is pasted into a program that runs as root on the
# hypervisor, so each is held to the shape it is expected to have.
matches "$VMID" '[1-9][0-9]{2,8}' || die "vmid $VMID is not a Proxmox VM id (100-999999999)"
matches "$STORAGE" '[A-Za-z][A-Za-z0-9_.-]*' || die "storage $STORAGE is not a Proxmox storage id"
# The shape the chart's render accepts for talos.version, pre-releases included.
matches "$VERSION" 'v[0-9]+\.[0-9]+\.[0-9]+(-[0-9a-z.]+)?' || die "Talos version '$VERSION' is not vMAJOR.MINOR.PATCH with an optional pre-release suffix"
matches "$SCHEMATIC" '[0-9a-f]{64}' || die "schematic '$SCHEMATIC' is not an Image Factory schematic id"
matches "$FACTORY" 'https?://[A-Za-z0-9.-]+(:[0-9]+)?(/[A-Za-z0-9._~/-]*)?' || die "factory URL '$FACTORY' is not a plain http(s) URL"
matches "$DISK_SIZE" '[1-9][0-9]*G' || die "disk size $DISK_SIZE is not a whole number of gigabytes such as 20G"
matches "$CPU" '[A-Za-z0-9_.+-]+' || die "cpu type $CPU is not a Proxmox CPU model name"
CA_PEM=
if [ -n "$FACTORY_CA" ]; then
  case $FACTORY in https://*) ;; *) die "COZY_TALOS_FACTORY_CA is set, but the factory URL '$FACTORY' is not https" ;; esac
  [ -f "$FACTORY_CA" ] || die "COZY_TALOS_FACTORY_CA: $FACTORY_CA is not a file"
  [ -r "$FACTORY_CA" ] || die "COZY_TALOS_FACTORY_CA: cannot read $FACTORY_CA"
  CA_PEM=$(pem_certificates "$FACTORY_CA") ||
    die "COZY_TALOS_FACTORY_CA: $FACTORY_CA is not a file of complete PEM certificate blocks and nothing else"
fi

# capmox lowercases the selector and compares it with the template's tags as a
# set, so the tags are normalised the same way before they are checked and
# written, and the selector printed at the end is exactly what will match. The
# split is on commas only: a newline would split a tag the operator meant whole.
case $TAGS in *'
'*) die "template tags are comma-separated and may not contain a newline" ;; esac
TAG_SET=$(printf '%s\n' "$TAGS" | tr ',' '\n' | tr '[:upper:]' '[:lower:]' | sed '/^$/d' | LC_ALL=C sort -u)
[ -n "$TAG_SET" ] || die "the template needs at least one tag"
bad=$(printf '%s\n' "$TAG_SET" | grep -Evx '[a-z0-9_][a-z0-9_+.-]*' || true)
[ -z "$bad" ] || die "not a valid Proxmox tag: $(printf '%s' "$bad" | paste -sd ' ' -)"
PVE_TAGS=$(printf '%s\n' "$TAG_SET" | paste -sd ';' -)

URL="$FACTORY/image/$SCHEMATIC/$VERSION/nocloud-amd64.raw.xz"
NAME="talos-$VERSION-$(printf '%s' "$SCHEMATIC" | cut -c1-8)"

program() {
  cat <<EOF
set -eu
VMID=$VMID
STORAGE=$STORAGE
URL='$URL'
NAME='$NAME'
TAGS='$PVE_TAGS'
DISK_SIZE=$DISK_SIZE
CPU='$CPU'
EOF
  cat <<'EOF'
if ! command -v qm >/dev/null || ! command -v pvesh >/dev/null; then
  echo "this is not a Proxmox VE node: qm and pvesh are missing" >&2
  exit 1
fi

# nextid answers with the id only when no VM in the cluster holds it.
pvesh get /cluster/nextid --vmid "$VMID" >/dev/null

# capmox requires exactly one template with the selector's tag set across the
# whole cluster and reports "found 2 VM templates" otherwise, so a second
# template with the same set would stop every pool that selects it.
same=$(pvesh get /cluster/resources --type vm --output-format json | perl -MJSON::PP -e '
  my $want = shift;
  local $/;
  for my $vm (@{ JSON::PP->new->decode(<STDIN>) }) {
    next unless $vm->{template} && defined $vm->{tags};
    my %t = map { lc($_) => 1 } grep { length } split /;/, $vm->{tags};
    print "$vm->{vmid} ($vm->{name}) on $vm->{node}\n" if join(";", sort keys %t) eq $want;
  }' "$TAGS")
if [ -n "$same" ]; then
  echo "a template already carries the tags $TAGS: $same" >&2
  exit 1
fi

work=$(mktemp -d "${TMPDIR:-/var/tmp}/talos-template.XXXXXX")
trap 'rm -rf "$work"' EXIT
EOF
  if [ -z "$CA_PEM" ]; then
    cat <<'EOF'
curl -fL --retry 3 -o "$work/disk.raw.xz" "$URL"
EOF
  else
    # The system bundle goes first so that a redirect to a publicly trusted
    # host still verifies: CDI appends talos.imageFactoryCA to its system pool
    # the same way. The blank line keeps a bundle without a final newline from
    # running into the first block. The terminator holds an underscore, which
    # no line of a checked certificate block can.
    cat <<EOF
{ cat /etc/ssl/certs/ca-certificates.crt; echo; cat; } > "\$work/factory-ca.pem" <<'FACTORY_CA_PEM'
$CA_PEM
FACTORY_CA_PEM
curl -fL --retry 3 --cacert "\$work/factory-ca.pem" -o "\$work/disk.raw.xz" "\$URL"
EOF
  fi
  cat <<'EOF'
xz -d "$work/disk.raw.xz"

# The layout of the templates the Proxmox substrate was verified on. No NIC:
# capmox writes net0 from the pool's proxmox.network on every clone.
qm create "$VMID" --name "$NAME" --tags "$TAGS" --ostype l26 \
  --cpu "$CPU" --sockets 1 --cores 2 --memory 4096 \
  --bios ovmf --efidisk0 "$STORAGE:1,efitype=4m,pre-enrolled-keys=0" \
  --scsihw virtio-scsi-pci \
  --scsi0 "$STORAGE:0,import-from=$work/disk.raw,cache=none,discard=on,ssd=1" \
  --boot order=scsi0 --serial0 socket --vga std \
  --agent enabled=1
# A failure from here on leaves a VM holding the id, and the nextid check above
# stops the next run until it is gone.
qm disk resize "$VMID" scsi0 "$DISK_SIZE" && qm template "$VMID" || {
  echo "VM $VMID was created but is not a template; remove it with: qm destroy $VMID" >&2
  exit 1
}
qm config "$VMID"
EOF
}

if [ "$MODE" = print ]; then
  program
  exit 0
fi

if [ -n "${COZY_PVE_SSH:-}" ]; then
  program | ssh -o BatchMode=yes -o StrictHostKeyChecking=accept-new \
    -i "${COZY_PVE_SSH_KEY:-$HOME/.ssh/id_ed25519}" "$COZY_PVE_SSH" sh -s
else
  program | sh -s
fi

cat <<EOF

Template $VMID ($NAME) is ready. A pool clones it with:

  talos:
    version: "$VERSION"
    schematicID: "$SCHEMATIC"
  proxmox:
    templateTags:
EOF
for tag in $TAG_SET; do
  printf '      - "%s"\n' "$tag"
done
