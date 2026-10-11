#!/usr/bin/env bats
# hack/proxmox-talos-template.sh builds the VM template a proxmox kubernetes-nodes
# pool clones. Nothing at render time can see what the template carries, so what
# the script writes is pinned here: the image is the chart's own talos.* in its
# nocloud flavour, the agent the default schematic waits for is enabled, and the
# tag set capmox selects on is checked before anything is downloaded.

load test_helper

REPO_ROOT="$(cd "$(dirname "${BATS_TEST_FILENAME:-$0}")/.." && pwd)"
SCRIPT="$REPO_ROOT/hack/proxmox-talos-template.sh"
VALUES="$REPO_ROOT/packages/apps/kubernetes-nodes/values.yaml"

chart() { sed -n "/^talos:/,/^[^ #]/s/^  $1: \"\(.*\)\"$/\1/p" "$VALUES"; }

# Stubs for the hypervisor side of the program. Each call to qm and curl is
# logged, so a test can tell what ran and, as importantly, what did not.
stub_pve() {
  stub="$1"
  printf '#!/bin/sh\necho "qm $*" >> "%s/calls"\n' "$stub" > "$stub/qm"
  cat > "$stub/pvesh" <<EOF
#!/bin/sh
case "\$2" in
  /cluster/nextid) echo "\$4" ;;
  /cluster/resources) cat "$stub/resources.json" ;;
esac
EOF
  cat > "$stub/curl" <<EOF
#!/bin/sh
echo "curl \$*" >> "$stub/calls"
while [ \$# -gt 0 ]; do [ "\$1" = -o ] && : > "\$2"; shift; done
EOF
  printf '#!/bin/sh\nmv "$2" "${2%%.xz}"\n' > "$stub/xz"
  chmod +x "$stub/qm" "$stub/pvesh" "$stub/curl" "$stub/xz"
  : > "$stub/calls"
}

@test "the template image is the chart's talos schematic and version, in the nocloud flavour" {
  out="$("$SCRIPT" print 9002 main-pool)"
  url="$(chart imageFactoryURL)/image/$(chart schematicID)/$(chart version)/nocloud-amd64.raw.xz"
  printf '%s' "$out" | grep -qxF "URL='$url'"
  if printf '%s' "$out" | grep -q openstack; then echo "FAIL: the template must not be the openstack image"; false; fi
}

@test "the template enables the guest agent whose port the default schematic waits for" {
  "$SCRIPT" print 9002 main-pool | grep -qE -- '--agent enabled=1$'
}

@test "the default tags name the version and the schematic, in the order Proxmox stores them" {
  out="$("$SCRIPT" print 9002 main-pool)"
  want="TAGS='cozystack;schematic-$(chart schematicID | cut -c1-8);talos;talos-$(chart version)'"
  printf '%s' "$out" | grep -qxF "$want"
}

@test "given tags are lowercased, deduplicated and sorted the way capmox compares them" {
  out="$(COZY_TEMPLATE_TAGS='Talos,cozystack,,talos,Pool-A' "$SCRIPT" print 9002 main-pool)"
  printf '%s' "$out" | grep -qxF "TAGS='cozystack;pool-a;talos'"
}

@test "an override of the talos values reaches the image URL" {
  sid=376567988ad370138ad8b2698212367b8edcb69b5fd68c80be1f2ec7d603b4ba
  out="$(COZY_TALOS_VERSION=v1.14.2 COZY_TALOS_SCHEMATIC_ID=$sid COZY_TALOS_FACTORY_URL=https://factory.example:8080/ "$SCRIPT" print 9002 main-pool)"
  printf '%s' "$out" | grep -qxF "URL='https://factory.example:8080/image/$sid/v1.14.2/nocloud-amd64.raw.xz'"
}

@test "values that would land in the root program unchecked are refused" {
  nl='
'
  for pair in "9002;id|main-pool" "99|main-pool" "9002|main pool" "9002|main'pool" \
    "9002${nl}id|main-pool" "9002|main-pool${nl}id"; do
    vmid="${pair%%|*}"
    storage="${pair#*|}"
    rc=0
    out="$("$SCRIPT" print "$vmid" "$storage" 2>&1)" || rc=$?
    [ "$rc" -ne 0 ] || { echo "accepted: $pair" >&2; exit 1; }
    case "$out" in *"set -eu"*) echo "printed a program for: $pair" >&2; exit 1 ;; esac
  done
  rc=0; COZY_TALOS_VERSION='v1.13.6;id' "$SCRIPT" print 9002 main-pool >/dev/null 2>&1 || rc=$?
  [ "$rc" -ne 0 ]
  rc=0; COZY_TALOS_SCHEMATIC_ID='ce4c9805' "$SCRIPT" print 9002 main-pool >/dev/null 2>&1 || rc=$?
  [ "$rc" -ne 0 ]
  rc=0; COZY_TEMPLATE_DISK_SIZE="20G${nl}id" "$SCRIPT" print 9002 main-pool >/dev/null 2>&1 || rc=$?
  [ "$rc" -ne 0 ]
  rc=0; COZY_TEMPLATE_TAGS="talos,it's" "$SCRIPT" print 9002 main-pool >/dev/null 2>&1 || rc=$?
  [ "$rc" -ne 0 ]
  rc=0; COZY_TEMPLATE_TAGS="talos-v1.${nl}13,cozystack" "$SCRIPT" print 9002 main-pool >/dev/null 2>&1 || rc=$?
  [ "$rc" -ne 0 ]
}

@test "a pre-release Talos version the chart accepts gets a template" {
  out="$(COZY_TALOS_VERSION=v1.13.6-beta.1 "$SCRIPT" print 9002 main-pool)"
  printf '%s' "$out" | grep -qF "/v1.13.6-beta.1/nocloud-amd64.raw.xz'"
  printf '%s' "$out" | grep -qxF "NAME='talos-v1.13.6-beta.1-$(chart schematicID | cut -c1-8)'"
}

# Two certificate blocks in the shape the CA checks accept. Only the shape is
# checked here, so the bodies are base64 text and not real certificates.
write_ca() {
  printf '%s\n' '-----BEGIN CERTIFICATE-----' 'MIIBszCCAVmgAwIBAgIUZmFjdG9yeS1jYS1vbmU=' '-----END CERTIFICATE-----' \
    '' '-----BEGIN CERTIFICATE-----' 'MIIBszCCAVmgAwIBAgIUZmFjdG9yeS1jYS10d28=' 'c2Vjb25kIGxpbmU=' '-----END CERTIFICATE-----' > "$1"
}

@test "without a factory CA the download trusts the system store alone" {
  out="$("$SCRIPT" print 9002 main-pool)"
  printf '%s' "$out" | grep -qx 'curl -fL --retry 3 -o "$work/disk.raw.xz" "$URL"'
  if printf '%s' "$out" | grep -q -- '--cacert'; then echo "FAIL: no CA was given, so curl must not be handed one"; false; fi
}

@test "a factory CA is trusted on top of the system store for the download" {
  stub="$(mktemp -d)"
  stub_pve "$stub"
  cat > "$stub/curl" <<EOF
#!/bin/sh
echo "curl \$*" >> "$stub/calls"
while [ \$# -gt 0 ]; do
  case "\$1" in
    --cacert) cp "\$2" "$stub/cacert" ;;
    -o) : > "\$2" ;;
  esac
  shift
done
EOF
  echo '[]' > "$stub/resources.json"
  printf 'system bundle\n' > "$stub/system.pem"
  write_ca "$stub/ca.pem"
  sed 's/$/\r/' "$stub/ca.pem" > "$stub/ca-crlf.pem"
  COZY_TALOS_FACTORY_URL=https://factory.internal COZY_TALOS_FACTORY_CA="$stub/ca-crlf.pem" \
    "$SCRIPT" print 9002 main-pool |
    sed "s|/etc/ssl/certs/ca-certificates.crt|$stub/system.pem|" |
    PATH="$stub:$PATH" TMPDIR="$stub" sh >/dev/null
  grep -qE '^curl -fL --retry 3 --cacert [^ ]*/factory-ca.pem -o [^ ]*/disk.raw.xz https://factory.internal/' "$stub/calls"
  got="$(cat "$stub/cacert")"
  want="$(printf 'system bundle\n\n'; grep -v '^$' "$stub/ca.pem")"
  [ "$got" = "$want" ] || { printf 'curl was handed:\n%s\nexpected:\n%s\n' "$got" "$want" >&2; false; }
  rm -rf "$stub"
}

@test "a factory CA file that is anything more than certificate blocks is refused" {
  stub="$(mktemp -d)"
  write_ca "$stub/ca.pem"
  { cat "$stub/ca.pem"; printf '%s\n' '-----BEGIN PRIVATE KEY-----' 'MC4CAQAwBQYDK2VwBCIEIA==' '-----END PRIVATE KEY-----'; } > "$stub/with-key.pem"
  { cat "$stub/ca.pem"; echo 'echo INJECTED'; } > "$stub/trailing.pem"
  printf '%s\n' '-----BEGIN CERTIFICATE-----' 'MIIBszCCAVmgAwIBAgIU' > "$stub/open.pem"
  printf '%s\n' '-----BEGIN CERTIFICATE-----' '-----END CERTIFICATE-----' > "$stub/no-body.pem"
  : > "$stub/empty.pem"
  for ca in with-key.pem trailing.pem open.pem no-body.pem empty.pem missing.pem .; do
    rc=0
    out="$(COZY_TALOS_FACTORY_URL=https://factory.internal COZY_TALOS_FACTORY_CA="$stub/$ca" "$SCRIPT" print 9002 main-pool 2>&1)" || rc=$?
    [ "$rc" -ne 0 ] || { echo "accepted: $ca" >&2; false; }
    case "$out" in *"set -eu"*) echo "printed a program for: $ca" >&2; false ;; esac
  done
  rc=0
  out="$(COZY_TALOS_FACTORY_URL=http://factory.internal COZY_TALOS_FACTORY_CA="$stub/ca.pem" "$SCRIPT" print 9002 main-pool 2>&1)" || rc=$?
  [ "$rc" -ne 0 ]
  case "$out" in *"is not https"*) ;; *) echo "$out" >&2; false ;; esac
  rm -rf "$stub"
}

@test "a template already carrying the same tag set stops the program before the download" {
  stub="$(mktemp -d)"
  stub_pve "$stub"
  printf '[{"vmid":9000,"name":"old","node":"pve1","template":1,"tags":"talos-v1.13.6;Talos;schematic-%s;cozystack"}]' \
    "$(chart schematicID | cut -c1-8)" > "$stub/resources.json"
  rc=0
  out="$("$SCRIPT" print 9002 main-pool | PATH="$stub:$PATH" TMPDIR="$stub" sh 2>&1)" || rc=$?
  calls="$(cat "$stub/calls")"
  [ "$rc" -ne 0 ]
  case "$out" in *"already carries"*"9000 (old) on pve1"*) ;; *) echo "$out" >&2; exit 1 ;; esac
  [ -z "$calls" ] || { echo "$calls" >&2; exit 1; }
  rm -rf "$stub"
}

@test "a template whose tags are a superset does not count, and the template is built" {
  stub="$(mktemp -d)"
  stub_pve "$stub"
  printf '[{"vmid":9000,"name":"old","node":"pve1","template":1,"tags":"cozystack;schematic-%s;talos;talos-v1.13.6;zfs"},{"vmid":101,"name":"vm","node":"pve1","template":0}]' \
    "$(chart schematicID | cut -c1-8)" > "$stub/resources.json"
  "$SCRIPT" print 9002 main-pool | PATH="$stub:$PATH" TMPDIR="$stub" sh >/dev/null
  calls="$(cat "$stub/calls")"
  printf '%s\n' "$calls" | grep -q '^curl .*/nocloud-amd64.raw.xz$'
  printf '%s\n' "$calls" | grep -qE '^qm create 9002 .*--scsi0 main-pool:0,import-from=[^ ]*/disk.raw,'
  printf '%s\n' "$calls" | grep -qx 'qm disk resize 9002 scsi0 20G'
  printf '%s\n' "$calls" | grep -qx 'qm template 9002'
  rm -rf "$stub"
}

@test "a VM left behind by a failed conversion is named with the command that removes it" {
  stub="$(mktemp -d)"
  stub_pve "$stub"
  printf '#!/bin/sh\necho "qm $*" >> "%s/calls"\n[ "$1" != template ]\n' "$stub" > "$stub/qm"
  echo '[]' > "$stub/resources.json"
  rc=0
  out="$("$SCRIPT" print 9002 main-pool | PATH="$stub:$PATH" TMPDIR="$stub" sh 2>&1)" || rc=$?
  calls="$(cat "$stub/calls")"
  [ "$rc" -ne 0 ]
  printf '%s\n' "$calls" | grep -qx 'qm template 9002'
  case "$out" in *"remove it with: qm destroy 9002"*) ;; *) echo "$out" >&2; exit 1 ;; esac
  rm -rf "$stub"
}

@test "create runs the printed program on the hypervisor over ssh and prints the pool's selector" {
  stub="$(mktemp -d)"
  printf '#!/bin/sh\necho "$*" > "%s/args"\ncat > "%s/stdin"\n' "$stub" "$stub" > "$stub/ssh"
  chmod +x "$stub/ssh"
  out="$(PATH="$stub:$PATH" COZY_PVE_SSH=root@pve "$SCRIPT" create 9002 main-pool)"
  args="$(cat "$stub/args")"
  sent="$(cat "$stub/stdin")"
  case "$args" in *"root@pve sh -s") ;; *) echo "$args" >&2; exit 1 ;; esac
  [ "$sent" = "$("$SCRIPT" print 9002 main-pool)" ]
  printf '%s' "$out" | grep -qxF '      - "talos-'"$(chart version)"'"'
  printf '%s' "$out" | grep -qxF '    schematicID: "'"$(chart schematicID)"'"'
  rm -rf "$stub"
}

@test "create fails when the hypervisor program fails, and prints no selector" {
  stub="$(mktemp -d)"
  printf '#!/bin/sh\ncat >/dev/null\necho "ssh: connect to host pve: Connection refused" >&2\nexit 255\n' > "$stub/ssh"
  chmod +x "$stub/ssh"
  rc=0
  out="$(PATH="$stub:$PATH" COZY_PVE_SSH=root@pve "$SCRIPT" create 9002 main-pool 2>&1)" || rc=$?
  [ "$rc" -ne 0 ]
  case "$out" in *templateTags*) echo "$out" >&2; exit 1 ;; esac
  rm -rf "$stub"
}
