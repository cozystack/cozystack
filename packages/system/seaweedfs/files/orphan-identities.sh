#!/bin/sh
# Removes the per-identity IAM files of BucketAccesses that no longer exist.
# Since SeaweedFS 4.08 the S3 gateways move every identity out of
# /etc/iam/identity.json into /etc/iam/identities/<name>.json, and COSI driver
# releases up to v0.3.1 revoke only from identity.json, so each deleted
# BucketAccess left its file behind.
#
# The COSI sidecar names every identity it creates ba-<BucketAccess uid>. A
# file of that shape that no BucketAccess in any namespace names, by uid or by
# status.accountID, is left over; anything else on the filer is never touched.
# The uid covers a BucketAccess still being granted, which has no accountID
# yet; the accountID covers one restored with its status under a new uid.
# One restored without its status is granted afresh, so a workload still
# holding the old keys loses them once their file is removed. A
# file changed within MIN_AGE seconds is skipped as well: the gateways rewrite
# every file on each grant, so a grant in flight is younger.
#
# MODE=delete removes; anything else only reports. A filer that other clusters
# reach as Client topology carries identities whose BucketAccesses this
# cluster cannot see, so the chart sets report there.
#
# Identity files carry secret keys, so their content is never printed. Any
# failure to list leaves everything in place, and no failure the script meets
# fails the release: it ends in a Warning Event on the HelmRelease instead.
set -eu

: "${MASTER:?}" "${FILER:?}" "${NAMESPACE:?}" "${RELEASE:?}"
JOB=${JOB:-$RELEASE-orphan-identities}
MODE=${MODE:-report}
MIN_AGE=${MIN_AGE:-600}
SHELL_TIMEOUT=${SHELL_TIMEOUT:-30}
DIR=/etc/iam/identities

# weed shell reports a failed command as an "error:" line on stderr and still
# exits 0, so stderr is kept with the output for the callers to check.
weed_shell() {
  timeout "$SHELL_TIMEOUT" weed shell -master="$MASTER" -filer="$FILER" 2>&1
}

# event <type> <reason> <message>
event() {
  message=$(printf '%s' "$3" | tr -d '"\134')
  stamp=$(date -u +%Y-%m-%dT%H:%M:%SZ)
  # kubectl describe matches events by uid as well as name.
  if [ -z "${hr_uid+set}" ]; then
    hr_uid=$(kubectl get helmrelease "$RELEASE" --request-timeout=30s --namespace "$NAMESPACE" --output jsonpath='{.metadata.uid}') || hr_uid=
  fi
  kubectl create --request-timeout=30s --filename - <<EOF >/dev/null || echo "could not record the $2 Event" >&2
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
  uid: "$hr_uid"
type: $1
reason: $2
message: "$message"
source:
  component: $JOB
firstTimestamp: "$stamp"
lastTimestamp: "$stamp"
count: 1
EOF
}

not_checked() {
  echo "$1" >&2
  event Warning OrphanIdentitiesNotChecked "$1 Nothing was removed. See the logs of Job $JOB."
  exit 0
}

# The ba-<uuid> names in a listing of $DIR, without the .json suffix.
cosi_names() {
  grep -E '^ba-[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}\.json$' | sed 's/\.json$//'
}

if ! listing=$(kubectl get bucketaccesses.objectstorage.k8s.io --all-namespaces --request-timeout=30s --output json) ||
  ! live=$(printf '%s\n' "$listing" | jq -r '
    .items[]
    | (.metadata.uid | if type == "string" and . != "" then "ba-" + . else error("no uid") end),
      (.status.accountID // empty)' 2>/dev/null); then
  not_checked "Could not list BucketAccesses."
fi

if ! files=$(echo "fs.ls $DIR" | weed_shell) || printf '%s\n' "$files" | grep -q '^error'; then
  not_checked "Could not list $DIR."
fi

candidates=
for name in $(printf '%s\n' "$files" | cosi_names); do
  printf '%s\n' "$live" | grep -qxF "$name" || candidates="$candidates $name"
done

# All ages in one session. Each entry is a protojson object whose closing
# brace carries the size trailer. A file missing from the answer, including
# one the session timed out before, is skipped and left for the next run.
ages=
if [ -n "$candidates" ]; then
  ages=$(for name in $candidates; do echo "fs.meta.cat $DIR/$name.json"; done | weed_shell |
    awk '/^\{/ { on = 1 } on { done = sub(/^\}chunks .*/, "}"); print; if (done || /^\}$/) on = 0 }' |
    jq -r '"\(.name) \(.attributes.mtime | tonumber)"' 2>/dev/null) || ages=
fi

orphans=
now=$(date +%s)
for name in $candidates; do
  mtime=$(printf '%s\n' "$ages" | awk -v n="$name.json" '$1 == n { print $2 }')
  if [ -z "$mtime" ]; then
    echo "skipping $name: could not read its age" >&2
  elif [ $((now - mtime)) -lt "$MIN_AGE" ]; then
    echo "skipping $name: changed less than $MIN_AGE seconds ago"
  else
    orphans="$orphans $name"
  fi
done
orphans=${orphans# }

if [ -z "$orphans" ]; then
  echo "nothing to remove"
  exit 0
fi

if [ "$MODE" != delete ]; then
  echo "left over from deleted BucketAccesses, not removed: $orphans"
  event Warning OrphanIdentitiesFound "Identities left over from deleted BucketAccesses, not removed because this filer may serve other clusters: $orphans"
  exit 0
fi

# An empty list next to identities on the filer is likelier a wrong answer
# than every bucket user gone at once.
if [ -z "$live" ]; then
  echo "no BucketAccess exists, not removed: $orphans" >&2
  event Warning OrphanIdentitiesFound "No BucketAccess exists in the cluster, so these identities on the filer were not removed: $orphans"
  exit 0
fi

# A failed removal does not name its file, so what is gone is read back.
for name in $orphans; do echo "fs.rm $DIR/$name.json"; done | weed_shell >/dev/null || true
if ! after=$(echo "fs.ls $DIR" | weed_shell) || printf '%s\n' "$after" | grep -q '^error'; then
  echo "could not list $DIR after removing: $orphans" >&2
  event Warning OrphanIdentityNotRemoved "Could not confirm the removal of identities left over from deleted BucketAccesses: $orphans. See the logs of Job $JOB."
  exit 0
fi
left=$(printf '%s\n' "$after" | cosi_names)
for name in $orphans; do
  if printf '%s\n' "$left" | grep -qxF "$name"; then
    echo "could not remove $name" >&2
    event Warning OrphanIdentityNotRemoved "Could not remove identity $name, left over from a deleted BucketAccess. See the logs of Job $JOB."
  else
    echo "removed $name"
    event Normal OrphanIdentityRemoved "Removed identity $name, left over from a deleted BucketAccess."
  fi
done
