#!/bin/sh
# Lets a release of this chart through piraeus-operator's validating webhooks
# while the operator is stopped, and puts them back afterwards.
#
#   piraeus-webhooks.sh relax    pre-upgrade and pre-rollback
#   piraeus-webhooks.sh restore  post-upgrade and post-rollback
#
# Helm patches every object of a release on every upgrade and rollback, even
# one whose render did not change: it stamps its ownership label and
# annotations onto the new objects only, so the client-side merge patch is
# never empty, and a server-side apply sends the whole object regardless. The
# apiserver calls the webhooks for that patch, and with failurePolicy Fail and
# nobody serving them the upgrade fails.
#
# relax sets Ignore on the webhooks for the kinds this chart renders, and only
# when their Service has no ready endpoint, so a running operator still
# validates every change. The names it changed are recorded in an annotation
# on the configuration, and restore sets Fail on exactly those, so a policy
# someone chose by hand is left alone. A relax that finds the webhooks served
# again restores whatever an earlier, failed upgrade left relaxed.
set -eu

phase=${1:?usage: piraeus-webhooks.sh relax|restore}
config=${PIRAEUS_WEBHOOKS:?PIRAEUS_WEBHOOKS names the ValidatingWebhookConfiguration}
mark=linstor.cozystack.io/relaxed-webhooks

current=$(kubectl get validatingwebhookconfiguration "$config" --ignore-not-found -o json)
if [ -z "$current" ]; then
  echo "$config: not found, nothing to do"
  exit 0
fi

apply() {
  if [ "$1" = "[]" ]; then
    return
  fi
  kubectl patch validatingwebhookconfiguration "$config" --type json --patch "$1"
}

# The webhooks this chart's objects pass through: LinstorCluster and
# LinstorSatelliteConfiguration. The test on each name guards the index
# against a configuration rewritten between the read and the patch.
selected='[.webhooks | to_entries[]
  | select(any(.value.rules[]?; any(.apiGroups[]?; . == "piraeus.io")
      and any(.resources[]?; . == "linstorclusters" or . == "linstorsatelliteconfigurations")))]'

restore() {
  patch=$(printf '%s' "$current" | jq -c --arg mark "$mark" '
    ((.metadata.annotations // {})[$mark] // "" | split(",") | map(select(. != ""))) as $names
    | if ($names | length) == 0 then [] else
        [.webhooks | to_entries[] | select(.value.name as $n | $names | any(. == $n))
          | select(.value.failurePolicy == "Ignore")
          | {op: "test", path: "/webhooks/\(.key)/name", value: .value.name},
            {op: "replace", path: "/webhooks/\(.key)/failurePolicy", value: "Fail"}]
        + [{op: "remove", path: "/metadata/annotations/\($mark | gsub("/"; "~1"))"}]
      end')
  apply "$patch"
  echo "$config: restored failurePolicy Fail on $(printf '%s' "$patch" | jq -r '[.[] | select(.op == "test") | .value] | join(",") | if . == "" then "nothing" else . end')"
}

served() {
  for service in $(printf '%s' "$current" | jq -r "$selected"' | .[].value.clientConfig.service // empty | "\(.namespace)/\(.name)"' | sort -u); do
    ready=$(kubectl get endpointslices --namespace "${service%%/*}" \
      --selector "kubernetes.io/service-name=${service#*/}" -o json |
      jq '[.items[].endpoints[]? | select(.conditions.ready != false)] | length')
    if [ "$ready" -eq 0 ]; then
      echo "$config: $service has no ready endpoint"
      return 1
    fi
  done
}

case $phase in
restore)
  restore
  ;;
relax)
  if served; then
    restore
    exit 0
  fi
  patch=$(printf '%s' "$current" | jq -c --arg mark "$mark" "$selected"' as $selected
    | ((.metadata.annotations // {})[$mark] // "" | split(",") | map(select(. != ""))) as $before
    | [$selected[] | select(.value.failurePolicy == "Fail")] as $relax
    | ($before + [$relax[].value.name] | unique | join(",")) as $names
    | if ($relax | length) == 0 then [] else
        [if .metadata.annotations == null
          then {op: "add", path: "/metadata/annotations", value: {($mark): $names}}
          else {op: "add", path: "/metadata/annotations/\($mark | gsub("/"; "~1"))", value: $names} end]
        + [$relax[] | {op: "test", path: "/webhooks/\(.key)/name", value: .value.name},
            {op: "replace", path: "/webhooks/\(.key)/failurePolicy", value: "Ignore"}]
      end')
  apply "$patch"
  echo "$config: set failurePolicy Ignore on $(printf '%s' "$patch" | jq -r '[.[] | select(.op == "test") | .value] | join(",") | if . == "" then "nothing" else . end')"
  ;;
*)
  echo "unknown phase $phase" >&2
  exit 2
  ;;
esac
