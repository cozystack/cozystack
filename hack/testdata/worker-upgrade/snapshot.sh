#!/usr/bin/env bash
set -euo pipefail
. hack/e2e-chainsaw/_lib/run-kubernetes-upgrade.sh
export COZY_UPGRADE_CONTEXT=synthetic COZY_UPGRADE_TENANT_CONTEXT=synthetic KAIROS_KC=synthetic
cozy_guard_helmrelease() { :; }
kubectl() {
  local args=" $* " uid=stable version=v1.35.6 provider=kubevirt://stable phase=Running
  case "${MUTATION:-}" in
    uid) uid=replaced ;;
    version) version=v1.35.7 ;;
    provider) provider=kubevirt://replacement ;;
    not-ready) phase=Pending ;;
  esac
  case "$args" in
    *' get virtualmachines --selector '*) echo "deployment labels belong to the VM template" >&2; return 1 ;;
    *' get machinedeployments.v1beta1.cluster.x-k8s.io --selector '*) echo "CAPI puts deployment-name on a MachineDeployment's selector and template, not on the object" >&2; return 1 ;;
    *' get machinedeployments.v1beta1.cluster.x-k8s.io --output json '*)
      jq --null-input --arg uid "$uid" --arg version "$version" '["example-md0","example-custom","other-md0"] | {items:map({metadata:{name:("kubernetes-"+.),uid:($uid+.),labels:{"cluster.x-k8s.io/cluster-name":"kubernetes-example"}},spec:{template:{metadata:{labels:{"cluster.x-k8s.io/deployment-name":("kubernetes-"+.)}},spec:{version:$version}}},status:{v1beta2:{readyReplicas:1}}})}' ;;
    *' get machines.v1beta1.cluster.x-k8s.io --selector '*|*' get virtualmachines '*|*' get virtualmachineinstances '*)
      jq --null-input --arg phase "$phase" --arg uid "$uid" --arg version "$version" '["md0","custom"] | {items:map({metadata:{name:.,uid:($uid+.),labels:{"cluster.x-k8s.io/deployment-name":("kubernetes-example-"+.)}},spec:{template:{metadata:{labels:{"cluster.x-k8s.io/deployment-name":("kubernetes-example-"+.)}},spec:{version:$version,volumes:[{cloudInitNoCloud:{userData:"DO_NOT_EXPORT_BOOTSTRAP_TOKEN"}}]}}},status:{nodeRef:{name:.},phase:$phase,conditions:[{type:"Ready",status:"True"}],v1beta2:{readyReplicas:1}}})}' ;;
    *' get machinedeployments.v1beta1.cluster.x-k8s.io '*) echo '{"spec":{"template":{"spec":{"bootstrap":{"configRef":{"name":"bootstrap"}},"infrastructureRef":{"name":"infrastructure"}}}}}' ;;
    *' get talosconfigtemplates.'*|*' get kubevirtmachinetemplates.'*) printf '{"metadata":{"uid":"%s"},"spec":{"secret":"DO_NOT_EXPORT_BOOTSTRAP_TOKEN"}}\n' "$uid" ;;
    *' get kubernetesnodeses.'*|*' get kuberneteses.'*)
      if [ "${MUTATION:-}" = get-defaults ]; then
        printf '{"metadata":{"uid":"%s"},"spec":{"version":"v1.35","credential":"DO_NOT_EXPORT_BOOTSTRAP_TOKEN"}}\n' "$uid"
      else
        printf '{"metadata":{"uid":"%s"},"spec":{"version":"v1.35","talos":{"version":"v1.13.6"},"credential":"DO_NOT_EXPORT_BOOTSTRAP_TOKEN"}}\n' "$uid"
      fi ;;

    *' get helmrelease '*) printf '{"metadata":{"uid":"%s"},"spec":{"values":{"version":"v1.35"}},"status":{"conditions":[{"type":"Ready","status":"True"}]}}\n' "$uid" ;;
    *' get node '*) printf '{"metadata":{"uid":"%s","name":"node"},"spec":{"providerID":"%s"},"status":{"conditions":[{"type":"Ready","status":"True"}]}}\n' "$uid" "$provider" ;;
    *' get pod '*) printf '{"metadata":{"uid":"%s"},"spec":{"nodeName":"node"},"status":{"conditions":[{"type":"Ready","status":"True"}]}}\n' "$uid" ;;
    *) echo "unexpected synthetic request: $args" >&2; return 1 ;;
  esac
}
work=$(mktemp -d)
cozy_worker_upgrade_snapshot example "$work/before"
export MUTATION=${1:-}
cozy_worker_upgrade_snapshot example "$work/after" || exit "$?"
if grep --recursive --quiet DO_NOT_EXPORT_BOOTSTRAP_TOKEN "$work"; then
  echo "sensitive bootstrap content was exported" >&2
  exit 2
fi
printf '%s\n' 'snapshots complete'
diff --recursive "$work/before" "$work/after"
