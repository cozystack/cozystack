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
cozy_worker_upgrade_platform_converged synthetic "$revision"
