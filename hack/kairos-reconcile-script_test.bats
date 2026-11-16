#!/usr/bin/env bats
# The kairos-reconcile Job builds a pool's KairosConfigTemplate from a
# ConfigMap by replacing two reserved markers, so no tenant value is ever shell
# text or rewritten. This runs the rendered Job script against the rendered
# ConfigMap with a stand-in kubectl, and checks that a hostile value arrives
# unchanged while the runtime addresses are filled in.

load test_helper

CHART="$(pwd)/packages/apps/kubernetes-nodes"

@test "a hostile tenant value reaches the applied template as data, never as shell" {
  dir="$(mktemp -d)"
  mkdir -p "$dir/bin" "$dir/kct"
  cat > "$dir/values.yaml" <<'VALUES'
cluster: myk8s
version: "v1.35"
workerClassName: kairos
instanceType: ""
resources: {cpu: "2", memory: 4Gi}
diskSize: 30Gi
storageClass: replicated
_cluster: {cluster-domain: cozy.local}
sshKeys:
  - 'ssh-ed25519 AAAA a$HOME b`touch PWNED` $(touch PWNED) c\d "q" ${NS} $SVC_IP ${COREDNS_IP} ${RELEASE}'
VALUES
  helm template kubernetes-nodes-myk8s-md0 "$CHART" --namespace tenant-test \
    --values "$dir/values.yaml" --show-only templates/kairos-reconcile-job.yaml > "$dir/rendered.yaml"
  yq 'select(.kind == "ConfigMap") | .data["spec.yaml"]' "$dir/rendered.yaml" > "$dir/kct/spec.yaml"
  yq 'select(.kind == "Job") | .spec.template.spec.containers[0].command[2]' "$dir/rendered.yaml" \
    | sed "s#/kct/spec.yaml#$dir/kct/spec.yaml#" > "$dir/script.sh"
  cat > "$dir/bin/kubectl" <<KUBECTL
#!/bin/sh
case "\$*" in
  *cozystack*vpc*) ;;
  *"get svc kube-dns"*) echo 10.96.0.10 ;;
  *"get svc"*) echo 10.96.7.7 ;;
  *kamajicontrolplane*) echo kcp-uid ;;
  *apply*) cat > "$dir/applied.yaml" ;;
esac
KUBECTL
  chmod +x "$dir/bin/kubectl"

  (cd "$dir" && PATH="$dir/bin:$PATH" RELEASE=kubernetes-myk8s NS=tenant-test GROUP_NAME=md0 KCT_NAME=kct-test \
    sh -ec "$(cat "$dir/script.sh")") >/dev/null

  [ ! -e "$dir/PWNED" ] || { echo "a tenant value ran as a command" >&2; exit 1; }
  [ "$(yq '.spec.template.spec.sshPublicKey' "$dir/applied.yaml")" = 'ssh-ed25519 AAAA a$HOME b`touch PWNED` $(touch PWNED) c\d "q" ${NS} $SVC_IP ${COREDNS_IP} ${RELEASE}' ]
  [ "$(yq '.metadata.name' "$dir/applied.yaml")" = kct-test ]
  [ "$(yq '.metadata.ownerReferences[0].uid' "$dir/applied.yaml")" = kcp-uid ]
  [ "$(yq '.spec.template.spec.serverAddress' "$dir/applied.yaml")" = 'https://kubernetes-myk8s.tenant-test.svc:6443' ]
  yq '.spec.template.spec.files[] | select(.path == "/etc/hosts") | .content' "$dir/applied.yaml" \
    | grep -qxF '10.96.7.7 kubernetes-myk8s.tenant-test.svc kubernetes-myk8s.tenant-test.svc.cluster.local'
  yq '.spec.template.spec.files[] | select(.path == "/etc/systemd/resolved.conf.d/cozystack.conf") | .content' "$dir/applied.yaml" \
    | grep -qxF 'DNS=10.96.0.10'

  rm -rf "$dir"
}

# The template spec reaches kubectl through a pipe, and the Job's sh has no
# pipefail, so a failing substitution would hand kubectl a template without its
# spec and the Job would still succeed. It has to fail before kubectl runs.
@test "a failing substitution fails the Job before anything is applied" {
  dir="$(mktemp -d)"
  mkdir -p "$dir/bin"
  cat > "$dir/values.yaml" <<'VALUES'
cluster: myk8s
version: "v1.35"
workerClassName: kairos
instanceType: ""
resources: {cpu: "2", memory: 4Gi}
diskSize: 30Gi
storageClass: replicated
_cluster: {cluster-domain: cozy.local}
VALUES
  helm template kubernetes-nodes-myk8s-md0 "$CHART" --namespace tenant-test \
    --values "$dir/values.yaml" --show-only templates/kairos-reconcile-job.yaml > "$dir/rendered.yaml"
  mkdir -p "$dir/kct"
  yq 'select(.kind == "ConfigMap") | .data["spec.yaml"]' "$dir/rendered.yaml" > "$dir/kct/spec.yaml"
  yq 'select(.kind == "Job") | .spec.template.spec.containers[0].command[2]' "$dir/rendered.yaml" \
    | sed "s#/kct/spec.yaml#$dir/kct/spec.yaml#" > "$dir/script.sh"
  cat > "$dir/bin/kubectl" <<KUBECTL
#!/bin/sh
case "\$*" in
  *cozystack*vpc*) ;;
  *"get svc kube-dns"*) echo 10.96.0.10 ;;
  *"get svc"*) echo 10.96.7.7 ;;
  *kamajicontrolplane*) echo kcp-uid ;;
  *apply*) cat > "$dir/applied.yaml" ;;
esac
KUBECTL
  printf '#!/bin/sh\nexit 1\n' > "$dir/bin/sed"
  chmod +x "$dir/bin/kubectl" "$dir/bin/sed"

  rc=0
  (cd "$dir" && PATH="$dir/bin:$PATH" RELEASE=kubernetes-myk8s NS=tenant-test GROUP_NAME=md0 KCT_NAME=kct-test \
    sh -ec "$(cat "$dir/script.sh")") >/dev/null 2>&1 || rc=$?
  [ "$rc" -ne 0 ]
  [ ! -e "$dir/applied.yaml" ]
  rm -rf "$dir"
}

@test "the Job applies nothing while any runtime address is missing" {
  dir="$(mktemp -d)"
  mkdir -p "$dir/bin" "$dir/kct"
  printf '%s\n' 'cluster: myk8s' 'version: "v1.35"' 'workerClassName: kairos' 'instanceType: ""' \
    'resources: {cpu: "2", memory: 4Gi}' 'diskSize: 30Gi' 'storageClass: replicated' '_cluster: {cluster-domain: cozy.local}' > "$dir/values.yaml"
  helm template kubernetes-nodes-myk8s-md0 "$CHART" --namespace tenant-test \
    --values "$dir/values.yaml" --show-only templates/kairos-reconcile-job.yaml > "$dir/rendered.yaml"
  yq 'select(.kind == "ConfigMap") | .data["spec.yaml"]' "$dir/rendered.yaml" > "$dir/kct/spec.yaml"
  yq 'select(.kind == "Job") | .spec.template.spec.containers[0].command[2]' "$dir/rendered.yaml" \
    | sed "s#/kct/spec.yaml#$dir/kct/spec.yaml#" > "$dir/script.sh"
  printf '#!/bin/sh\nexit 0\n' > "$dir/bin/sleep"
  chmod +x "$dir/bin/sleep"
  for missing in svc coredns kcp; do
    rm -f "$dir/applied.yaml"
    cat > "$dir/bin/kubectl" <<KUBECTL
#!/bin/sh
case "\$*" in
  *cozystack*vpc*) ;;
  *"get svc kube-dns"*) [ "$missing" = coredns ] || echo 10.96.0.10 ;;
  *"get svc"*) [ "$missing" = svc ] || echo 10.96.7.7 ;;
  *kamajicontrolplane*) [ "$missing" = kcp ] || echo kcp-uid ;;
  *apply*) cat > "$dir/applied.yaml" ;;
esac
KUBECTL
    chmod +x "$dir/bin/kubectl"
    rc=0
    (cd "$dir" && PATH="$dir/bin:$PATH" RELEASE=kubernetes-myk8s NS=tenant-test GROUP_NAME=md0 KCT_NAME=kct-test \
      sh -ec "$(cat "$dir/script.sh")") >/dev/null 2>"$dir/err" || rc=$?
    [ "$rc" -ne 0 ] || { echo "the Job succeeded without $missing" >&2; exit 1; }
    [ ! -e "$dir/applied.yaml" ] || { echo "the Job applied a template without $missing" >&2; exit 1; }
    grep -qF 'inputs not ready' "$dir/err"
  done
  rm -rf "$dir"
}

@test "the Job applies nothing when a runtime address is not an IP address" {
  dir="$(mktemp -d)"
  mkdir -p "$dir/bin" "$dir/kct"
  printf '%s\n' 'cluster: myk8s' 'version: "v1.35"' 'workerClassName: kairos' 'instanceType: ""' \
    'resources: {cpu: "2", memory: 4Gi}' 'diskSize: 30Gi' 'storageClass: replicated' '_cluster: {cluster-domain: cozy.local}' > "$dir/values.yaml"
  helm template kubernetes-nodes-myk8s-md0 "$CHART" --namespace tenant-test \
    --values "$dir/values.yaml" --show-only templates/kairos-reconcile-job.yaml > "$dir/rendered.yaml"
  yq 'select(.kind == "ConfigMap") | .data["spec.yaml"]' "$dir/rendered.yaml" > "$dir/kct/spec.yaml"
  yq 'select(.kind == "Job") | .spec.template.spec.containers[0].command[2]' "$dir/rendered.yaml" \
    | sed "s#/kct/spec.yaml#$dir/kct/spec.yaml#" > "$dir/script.sh"
  cat > "$dir/bin/kubectl" <<KUBECTL
#!/bin/sh
case "\$*" in
  *cozystack*vpc*) ;;
  *"get svc kube-dns"*) echo 10.96.0.10 ;;
  *"get svc"*) echo '10.96.7.7/g;s/x' ;;
  *kamajicontrolplane*) echo kcp-uid ;;
  *apply*) cat > "$dir/applied.yaml" ;;
esac
KUBECTL
  chmod +x "$dir/bin/kubectl"
  rc=0
  (cd "$dir" && PATH="$dir/bin:$PATH" RELEASE=kubernetes-myk8s NS=tenant-test GROUP_NAME=md0 KCT_NAME=kct-test \
    sh -ec "$(cat "$dir/script.sh")") >/dev/null 2>"$dir/err" || rc=$?
  [ "$rc" -ne 0 ]
  [ ! -e "$dir/applied.yaml" ]
  grep -qF 'not an IP address' "$dir/err"
  rm -rf "$dir"
}

# Inside a cozyplane VPC the workers reach the apiserver by the VIP the VPC
# resolver answers with, not by the ClusterIP, and the tenant's own endpoint
# has to advertise that VIP too.
@test "a pool inside a VPC writes the apiserver VIP and advertises it" {
  dir="$(mktemp -d)"
  mkdir -p "$dir/bin" "$dir/kct"
  printf '%s\n' 'cluster: myk8s' 'version: "v1.35"' 'workerClassName: kairos' 'instanceType: ""' \
    'resources: {cpu: "2", memory: 4Gi}' 'diskSize: 30Gi' 'storageClass: replicated' '_cluster: {cluster-domain: cozy.local}' \
    'network: {name: myvpc}' > "$dir/values.yaml"
  helm template kubernetes-nodes-myk8s-md0 "$CHART" --namespace tenant-test --api-versions sdn.cozystack.io/v1alpha1/VPC \
    --values "$dir/values.yaml" --show-only templates/kairos-reconcile-job.yaml > "$dir/rendered.yaml"
  yq 'select(.kind == "ConfigMap") | .data["spec.yaml"]' "$dir/rendered.yaml" > "$dir/kct/spec.yaml"
  yq 'select(.kind == "Job") | .spec.template.spec.containers[0].command[2]' "$dir/rendered.yaml" \
    | sed "s#/kct/spec.yaml#$dir/kct/spec.yaml#" > "$dir/script.sh"
  printf '#!/bin/sh\nexit 0\n' > "$dir/bin/sleep"
  for parent in virtualprivatecloud-myvpc ""; do
    rm -f "$dir/applied.yaml" "$dir/patched"
    cat > "$dir/bin/kubectl" <<KUBECTL
#!/bin/sh
case "\$*" in
  *cozystack*vpc*) echo "$parent" ;;
  *status.phase*) echo Succeeded ;;
  *terminated.message*) echo 10.200.0.5 ;;
  *"patch kamajicontrolplane"*) echo "\$*" >> "$dir/patched" ;;
  *"get svc kube-dns"*) echo 10.96.0.10 ;;
  *"get svc"*) echo 10.96.7.7 ;;
  *kamajicontrolplane*) echo kcp-uid ;;
  *server-side*) cat > "$dir/applied.yaml" ;;
  *apply*) cat > /dev/null ;;
esac
KUBECTL
    chmod +x "$dir/bin/kubectl" "$dir/bin/sleep"
    rc=0
    (cd "$dir" && PATH="$dir/bin:$PATH" RELEASE=kubernetes-myk8s NS=tenant-test GROUP_NAME=md0 KCT_NAME=kct-test \
      sh -ec "$(cat "$dir/script.sh")") >"$dir/err" 2>&1 || rc=$?
    if [ -n "$parent" ]; then
      [ "$rc" -eq 0 ] || { cat "$dir/err" >&2; exit 1; }
      yq '.spec.template.spec.files[] | select(.path == "/etc/hosts") | .content' "$dir/applied.yaml" \
        | grep -qxF '10.200.0.5 kubernetes-myk8s.tenant-test.svc kubernetes-myk8s.tenant-test.svc.cluster.local'
      grep -qF '"advertiseAddress":"10.200.0.5"' "$dir/patched"
    else
      [ "$rc" -ne 0 ] || { echo "a VPC pool joined a control plane outside the VPC" >&2; exit 1; }
      [ ! -e "$dir/applied.yaml" ]
      grep -qF 'must match the parent' "$dir/err"
    fi
  done
  rm -rf "$dir"
}
