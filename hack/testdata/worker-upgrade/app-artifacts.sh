#!/usr/bin/env bash
set -euo pipefail
. hack/e2e-chainsaw/_lib/run-kubernetes-upgrade.sh
mode=${1:-current}
source_digest=sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa
revision=${SOURCE_REVISION:-sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb}
url=http://source.example.test/packages.tar.gz
observed=$(printf 'cozystack-packages=[digest=%s,revision=%s,url=%s]' "$source_digest" "$revision" "$url" | sha256sum)
observed=sha256:${observed%% *}
list=$(jq --null-input --arg digest "$source_digest" --arg revision "$revision" --arg url "$url" --arg observed "$observed" '
  ["kubernetes", "kubernetes-nodes"] as $apps
  | {items: ([{kind:"OCIRepository",metadata:{name:"cozystack-packages",namespace:"cozy-system",generation:2},status:{observedGeneration:2,conditions:[{type:"Ready",status:"True"}],artifact:{digest:$digest,revision:$revision,url:$url}}}]
    + [$apps[] | . as $app | ("cozystack." + $app + "-application") as $ag | ("cozystack-" + $app + "-application-kubevirt-" + $app) as $ea
      | {kind:"ArtifactGenerator",metadata:{name:$ag,namespace:"cozy-system",uid:$ag,generation:3},spec:{sources:[{alias:"cozystack-packages",kind:"OCIRepository",name:"cozystack-packages",namespace:"cozy-system"}],outputArtifacts:[{name:$ea}]},status:{conditions:[{type:"Ready",status:"True",observedGeneration:3}],observedSourcesDigest:$observed,inventory:[{name:$ea,namespace:"cozy-system",digest:"sha256:unchanged"}]}}]
    + [$apps[] | . as $app | {kind:"ExternalArtifact",metadata:{name:("cozystack-" + $app + "-application-kubevirt-" + $app),namespace:"cozy-system",labels:{"source.extensions.fluxcd.io/generator":("cozystack." + $app + "-application")}},spec:{sourceRef:{apiVersion:"source.extensions.fluxcd.io/v1beta1",kind:"ArtifactGenerator",name:("cozystack." + $app + "-application"),namespace:"cozy-system"}},status:{artifact:{digest:"sha256:unchanged"}}}])}')
case "$mode" in
  current) ;;
  old-source) list=$(jq '(.items[] | select(.kind == "ArtifactGenerator") | .status.observedSourcesDigest)="sha256:old"' <<< "$list") ;;
  stale-generation) list=$(jq '(.items[] | select(.kind == "ArtifactGenerator") | .status.conditions[0].observedGeneration)=2' <<< "$list") ;;
  stale-output) list=$(jq '(.items[] | select(.kind == "ExternalArtifact") | .status.artifact.digest)="sha256:old"' <<< "$list") ;;
  stale-generator-uid) list=$(jq '(.items[] | select(.kind == "ExternalArtifact") | .metadata.labels["source.extensions.fluxcd.io/generator"])="replaced"' <<< "$list") ;;
  wrong-source-name) list=$(jq '(.items[] | select(.kind == "ExternalArtifact") | .spec.sourceRef.name)="missing-generator"' <<< "$list") ;;
  wrong-source-kind) list=$(jq '(.items[] | select(.kind == "ExternalArtifact") | .spec.sourceRef.kind)="Unrelated"' <<< "$list") ;;
  wrong-source-version) list=$(jq '(.items[] | select(.kind == "ExternalArtifact") | .spec.sourceRef.apiVersion)="source.extensions.fluxcd.io/v0"' <<< "$list") ;;
  wrong-source-namespace) list=$(jq '(.items[] | select(.kind == "ExternalArtifact") | .spec.sourceRef.namespace)="other"' <<< "$list") ;;
  owner-only) list=$(jq '(.items[] | select(.kind == "ExternalArtifact")) |= (.metadata.ownerReferences=[{apiVersion:.spec.sourceRef.apiVersion,kind:.spec.sourceRef.kind,name:.spec.sourceRef.name,uid:.metadata.labels["source.extensions.fluxcd.io/generator"],controller:true}] | del(.spec.sourceRef))' <<< "$list") ;;
  missing-generator) list=$(jq '.items |= map(select(.kind != "ArtifactGenerator"))' <<< "$list") ;;
  *) exit 2 ;;
esac
if [ "${2:-}" = emit ]; then
  printf '%s\n' "$list"
else
  printf '%s\n' "$list" | cozy_worker_upgrade_app_artifacts_current "$revision"
fi
