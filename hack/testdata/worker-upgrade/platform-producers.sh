#!/usr/bin/env bash
set -euo pipefail
. hack/e2e-chainsaw/_lib/run-kubernetes-upgrade.sh
mode=${1:-current}
revision=sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb
producer=cozystack.capi-provider-infra-kubevirt
artifact=cozystack-capi-provider-infra-kubevirt-default-capi-providers-infraprovider
PLATFORM_LIST=$(bash hack/testdata/worker-upgrade/app-artifacts.sh current emit)
PLATFORM_LIST=$(jq --arg producer "$producer" --arg artifact "$artifact" '
  (.items | map(select(.kind == "ArtifactGenerator")) | .[0]) as $model
  | "sha256:cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc" as $digest
  | .items += [
      ($model | .metadata.name=$producer | .metadata.uid="platform-producer"
        | .spec.outputArtifacts=[{name:$artifact}]
        | .status.inventory=[{name:$artifact,namespace:"cozy-system",digest:$digest}]),
      {kind:"ExternalArtifact",metadata:{name:$artifact,namespace:"cozy-system",labels:{"source.extensions.fluxcd.io/generator":"platform-producer"}},spec:{sourceRef:{apiVersion:"source.extensions.fluxcd.io/v1beta1",kind:"ArtifactGenerator",name:$producer,namespace:"cozy-system"}},status:{artifact:{digest:$digest,revision:$digest}}},
      {kind:"HelmRelease",metadata:{name:"capi-providers-infraprovider",namespace:"cozy-cluster-api",generation:2},spec:{chartRef:{kind:"ExternalArtifact",name:$artifact,namespace:"cozy-system"}},status:{observedGeneration:2,conditions:[{type:"Ready",status:"True"}],lastAttemptedRevisionDigest:$digest,history:[{version:2,status:"deployed"}]}}
    ]' <<< "$PLATFORM_LIST")
case "$mode" in
  current) ;;
  platform-source)
    input=$(jq --raw-output '.items[] | select(.kind == "OCIRepository") | .status.artifact | "cozystack-platform=[digest=\(.digest),revision=\(.revision),url=\(.url)]"' <<< "$PLATFORM_LIST")
    observed=$(printf '%s' "$input" | sha256sum)
    PLATFORM_LIST=$(jq --arg producer "$producer" --arg observed "sha256:${observed%% *}" '
      .items += [.items[] | select(.kind == "OCIRepository") | .metadata.name="cozystack-platform"]
      | (.items[] | select(.kind == "ArtifactGenerator" and .metadata.name == $producer)) |=
        (.spec.sources=[{alias:"cozystack-platform",kind:"OCIRepository",name:"cozystack-platform",namespace:"cozy-system"}] | .status.observedSourcesDigest=$observed)' <<< "$PLATFORM_LIST")
    ;;
  wrong-source-name) PLATFORM_LIST=$(jq --arg artifact "$artifact" '(.items[] | select(.kind == "ExternalArtifact" and .metadata.name == $artifact) | .spec.sourceRef.name)="missing-generator"' <<< "$PLATFORM_LIST") ;;
  wrong-source-namespace) PLATFORM_LIST=$(jq --arg artifact "$artifact" '(.items[] | select(.kind == "ExternalArtifact" and .metadata.name == $artifact) | .spec.sourceRef.namespace)="other"' <<< "$PLATFORM_LIST") ;;
  stale-generator-uid) PLATFORM_LIST=$(jq --arg artifact "$artifact" '(.items[] | select(.kind == "ExternalArtifact" and .metadata.name == $artifact) | .metadata.labels["source.extensions.fluxcd.io/generator"])="replaced"' <<< "$PLATFORM_LIST") ;;
  old-producer) PLATFORM_LIST=$(jq --arg producer "$producer" '(.items[] | select(.kind == "ArtifactGenerator" and .metadata.name == $producer) | .status.observedSourcesDigest)="sha256:baseline"' <<< "$PLATFORM_LIST") ;;
  stale-inventory) PLATFORM_LIST=$(jq --arg producer "$producer" '(.items[] | select(.kind == "ArtifactGenerator" and .metadata.name == $producer) | .status.inventory[0].digest)="sha256:baseline"' <<< "$PLATFORM_LIST") ;;
  *) exit 2 ;;
esac
printf '%s\n' "$PLATFORM_LIST" | cozy_worker_upgrade_app_artifacts_current "$revision"
clock=$(mktemp)
printf '0\n' > "$clock"
date() {
  tick=$(cat "$clock")
  printf '%s\n' "$((tick + 1000))" > "$clock"
  printf '%s\n' "$tick"
}
sleep() { :; }
kubectl() {
  case " $* " in *' --context synthetic '*) ;; *) return 1 ;; esac
  case " $* " in
    *artifactgenerators*) printf '%s\n' "$PLATFORM_LIST" ;;
    *) jq '.items |= map(select(.kind != "ArtifactGenerator"))' <<< "$PLATFORM_LIST" ;;
  esac
}
# HISTORY names the platform release's history against the baseline digests:
# baseline-failed fails an action the baseline already had; failed and
# uninstalled add one after it; reset reinstalls from version 1; note fails one
# on a release that does not retry in place; new-release fails one on a release
# the baseline did not have; suspended-failed fails one on a suspended release;
# missing-baseline fails one with no baseline recorded at all.
hr='.items[] | select(.kind == "HelmRelease" and .metadata.name == "capi-providers-infraprovider")'
key=cozy-cluster-api/capi-providers-infraprovider
known='["sha256:d1","sha256:d2","sha256:d3"]'
history='[{version:3,status:"deployed",digest:"sha256:d3"},{version:2,status:"failed",digest:"sha256:d2"}]'
case "${HISTORY:-}" in
  failed|suspended-failed|note|new-release|missing-baseline) history='[{version:4,status:"failed",digest:"sha256:d4"},{version:3,status:"deployed",digest:"sha256:d3"}]' ;;
  uninstalled) history='[{version:4,status:"uninstalled",digest:"sha256:d4"},{version:3,status:"deployed",digest:"sha256:d3"}]' ;;
  reset) history='[{version:2,status:"deployed",digest:"sha256:r2"},{version:1,status:"failed",digest:"sha256:r1"}]' ;;
esac
if [ -n "${HISTORY:-}" ]; then
  strategy='"RetryOnFailure"'
  [ "$HISTORY" != note ] || strategy=null
  PLATFORM_LIST=$(jq "($hr) |= (.spec.install.strategy.name = $strategy | .status.history = $history)" <<< "$PLATFORM_LIST")
fi
[ "${HISTORY:-}" != suspended-failed ] || PLATFORM_LIST=$(jq "($hr).spec.suspend = true" <<< "$PLATFORM_LIST")
baseline="{\"$key\":$known}"
[ "${HISTORY:-}" != new-release ] || baseline='{}'
work=$(mktemp -d)
mkdir -p "$work/_out"
[ "${HISTORY:-}" = missing-baseline ] || printf '%s\n' "$baseline" > "$work/_out/worker-upgrade-platform-history-before.json"
cd "$work"
rc=0
cozy_worker_upgrade_platform_converged synthetic "$revision" || rc=$?
cd /
rm -rf "$work"
exit "$rc"
