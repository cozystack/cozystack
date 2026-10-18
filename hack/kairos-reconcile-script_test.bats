#!/usr/bin/env bats
# The kairos-reconcile Job builds a pool's KairosConfigTemplate from a
# ConfigMap with envsubst, so no tenant value is ever shell text. This runs the
# rendered Job script against the rendered ConfigMap with a real envsubst and a
# stand-in kubectl, and checks that a hostile value arrives as data while the
# runtime addresses are filled in.

load test_helper

CHART="$(pwd)/packages/apps/kubernetes-nodes"

@test "a hostile tenant value reaches the applied template as data, never as shell" {
  command -v envsubst >/dev/null || { echo "envsubst (GNU gettext) is required; the Job's kubectl image ships it" >&2; exit 1; }
  dir="$(mktemp -d)"
  mkdir -p "$dir/bin" "$dir/kct"
  cat > "$dir/values.yaml" <<'VALUES'
cluster: myk8s
version: "v1.35"
os: kairos
instanceType: ""
resources: {cpu: "2", memory: 4Gi}
diskSize: 30Gi
storageClass: replicated
_cluster: {cluster-domain: cozy.local}
kairos:
  image: registry.example/worker@sha256:0000000000000000000000000000000000000000000000000000000000000000
  sshPublicKey: 'ssh-ed25519 AAAA a$HOME b`touch PWNED` $(touch PWNED) c\d "q"'
VALUES
  helm template kubernetes-nodes-myk8s-md0 "$CHART" --namespace tenant-test \
    --values "$dir/values.yaml" --show-only templates/kairos-reconcile-job.yaml > "$dir/rendered.yaml"
  yq 'select(.kind == "ConfigMap") | .data["spec.yaml"]' "$dir/rendered.yaml" > "$dir/kct/spec.yaml"
  yq 'select(.kind == "Job") | .spec.template.spec.containers[0].command[2]' "$dir/rendered.yaml" \
    | sed "s#/kct/spec.yaml#$dir/kct/spec.yaml#" > "$dir/script.sh"
  cat > "$dir/bin/kubectl" <<KUBECTL
#!/bin/sh
case "\$*" in
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
  [ "$(yq '.spec.template.spec.sshPublicKey' "$dir/applied.yaml")" = 'ssh-ed25519 AAAA a$HOME b`touch PWNED` $(touch PWNED) c\d "q"' ]
  [ "$(yq '.metadata.name' "$dir/applied.yaml")" = kct-test ]
  [ "$(yq '.metadata.ownerReferences[0].uid' "$dir/applied.yaml")" = kcp-uid ]
  [ "$(yq '.spec.template.spec.serverAddress' "$dir/applied.yaml")" = 'https://kubernetes-myk8s.tenant-test.svc:6443' ]
  yq '.spec.template.spec.files[] | select(.path == "/etc/hosts") | .content' "$dir/applied.yaml" \
    | grep -qxF '10.96.7.7 kubernetes-myk8s.tenant-test.svc kubernetes-myk8s.tenant-test.svc.cluster.local'
  yq '.spec.template.spec.files[] | select(.path == "/etc/systemd/resolved.conf.d/cozystack.conf") | .content' "$dir/applied.yaml" \
    | grep -qxF 'DNS=10.96.0.10'

  rm -rf "$dir"
}
