#!/bin/sh
# SeaweedFS 4.48 requires dedicated Object Lock actions. Upgrading the COSI
# driver does not re-grant access for Ready BucketAccesses, so old identities
# still need those actions. Governance bypass was never implied by Write.
# The IAM listing and apply output contain secret keys; neither is printed.
# Grant migration is best-effort: handled errors must not fail the Helm action.
set -eu

: "${MASTER:?}" "${FILER:?}" "${NAMESPACE:?}" "${RELEASE:?}"
JOB=${JOB:-$RELEASE-object-lock-actions}
# weed shell waits for the master without a deadline.
ATTEMPTS=${ATTEMPTS:-3}
RETRY_DELAY=${RETRY_DELAY:-20}
SHELL_TIMEOUT=${SHELL_TIMEOUT:-30}

weed_shell() {
  timeout "$SHELL_TIMEOUT" weed shell -master="$MASTER" -filer="$FILER"
}

# Static identities come from a config file the IAM API cannot change.
plan_from_config() {
  sed -n '/^{/,$p' | jq -c '
    def implied:
      if test("^Write(:|$)") then
        sub("^Write"; "") as $s
        | ["PutObjectRetention", "PutObjectLegalHold", "PutBucketObjectLockConfiguration"] | map(. + $s)
      elif test("^Read(:|$)") then
        sub("^Read"; "") as $s
        | ["GetObjectRetention", "GetObjectLegalHold", "GetBucketObjectLockConfiguration"] | map(. + $s)
      else [] end;
    if has("identities") | not then error("no IAM configuration in the output") else . end
    | [.identities[]?
       | select(.isStatic != true)
       | (.actions // []) as $have
       | {name, have: $have, missing: ([$have[] | implied[]] | unique - $have)}
       | select(.missing | length > 0)]'
}

# s3.configure resolves the user on its own, and creates a fresh identity
# with no keys when it cannot, which would then replace the one listed. The
# dry run shows what it resolved; only an identity that still carries every
# action the listing showed is applied.
safe_commands() {
  sed '/^Running in simulation mode/d' | jq -rs --argjson plan "$1" '
    (map({key: .name, value: (.actions // [])}) | from_entries) as $resolved
    | $plan[]
    | select(($resolved[.name] // []) as $r | .have - $r | length == 0)
    | "s3.configure -user=\(.name) -actions=\(.missing | join(",")) -apply"'
}

# weed shell splits a line on ";" and on whitespace, so an identity whose name
# or actions carry anything else is never put on a command line. It stays in
# the plan and is reported.
dry_run_commands() {
  jq -r '.[]
    | select(.name | test("^[A-Za-z0-9._@+=-]+$"))
    | select(.missing | all(test("^[A-Za-z0-9._@+=:/*?-]+$")))
    | "s3.configure -user=\(.name) -actions=\(.missing | join(","))"'
}

# Each attempt reads, then grants; the read after the last attempt is what
# decides the outcome. A failed read keeps the previous plan for the next
# grant, but the outcome is never reported from it.
plan='[]'
attempt=0
while :; do
  read_ok=false
  if config=$(echo s3.configure | weed_shell) && fresh=$(printf '%s\n' "$config" | plan_from_config) && [ -n "$fresh" ]; then
    read_ok=true
    plan=$fresh
    if [ "$plan" = '[]' ]; then
      echo "every identity carries the Object Lock actions its grants imply"
      exit 0
    fi
  fi
  [ "$attempt" -ge "$ATTEMPTS" ] && break
  attempt=$((attempt + 1))
  if [ -n "$plan" ] && dry=$(printf '%s\n' "$plan" | dry_run_commands) && [ -n "$dry" ] &&
    commands=$(printf '%s\n' "$dry" | weed_shell | safe_commands "$plan") && [ -n "$commands" ]; then
    printf '%s\n' "$commands" | sed 's/^s3.configure -user=\([^ ]*\) -actions=\([^ ]*\) -apply$/granting \2 to \1/'
    printf '%s\n' "$commands" | weed_shell >/dev/null || true
  fi
  sleep "$RETRY_DELAY"
done

if $read_ok; then
  pending=$(printf '%s\n' "$plan" | jq -r '[.[].name] | join(" ")')
  message="Object Lock actions not granted after $ATTEMPTS attempts; identities still missing them: $pending. See the logs of Job $JOB."
else
  message="Object Lock actions unverified after $ATTEMPTS attempts: the IAM configuration could not be read. See the logs of Job $JOB."
fi
message=$(printf '%s' "$message" | tr -d '"\134')
echo "$message" >&2
now=$(date -u +%Y-%m-%dT%H:%M:%SZ)
# kubectl describe matches events by uid as well as name.
uid=$(kubectl get helmrelease "$RELEASE" --namespace "$NAMESPACE" --output jsonpath='{.metadata.uid}') || uid=
kubectl create --filename - <<EOF || echo "could not record the Warning Event" >&2
apiVersion: v1
kind: Event
metadata:
  generateName: $JOB-
  namespace: $NAMESPACE
involvedObject:
  apiVersion: helm.toolkit.fluxcd.io/v2
  kind: HelmRelease
  name: $RELEASE
  namespace: $NAMESPACE
  uid: "$uid"
type: Warning
reason: ObjectLockActionsNotGranted
message: "$message"
source:
  component: $JOB
firstTimestamp: "$now"
lastTimestamp: "$now"
count: 1
EOF
