#!/usr/bin/env bats
# Unit test: the vm-disk ResourceDefinition gates release readiness on the
# DataVolume CDI actually reconciles, and gates it on the right phases.
#
# Without spec.release.healthCheckExprs the HelmRelease reports Ready the
# moment Helm has applied the DataVolume, so an import or clone that never
# completes leaves the application green forever (cozystack/cozystack#2908).
# The expressions are what turns that into something the user can see.
#
# Mutations it must not sleep through, each of which keeps the block present
# and the YAML valid:
#
#   - hollowing out `failed` (say to a literal that never holds), which leaves
#     a check that cannot fail;
#   - moving a settled phase into `inProgress`. PendingPopulation and
#     WaitForFirstConsumer are where a WaitForFirstConsumer volume rests until
#     something attaches it, and UploadReady is where a disk rests until
#     virtctl uploads to it. Treating any of them as work in flight reports
#     every untouched disk as broken;
#   - joining `inProgress` with `||` instead of `&&`, or dropping the
#     has(status.phase) guard, which leaves every phase name in place and makes
#     any DataVolume carrying a phase permanently in flight, so no disk ever
#     reports ready and every install burns the whole window.
#
# Asserted against the RENDERED chart, not against cozyrds/vm-disk.yaml on
# disk: templates/cozyrd.yaml globs cozyrds/* through .Files.Get, so what
# ships is the render. Every query below selects the vm-disk document by name,
# because a second file in that directory renders as a second document and a
# collect across the stream would otherwise read whichever document happens to
# carry the key.
#
# This lives in bats rather than helm-unittest because packages/system/
# vm-disk-rd has no `test:` target and hack/helm-unit-tests.sh skips any
# package without one.
#
# What no static check can reach: the expressions are CEL, compiled and
# evaluated by helm-controller against a live DataVolume. Nothing here proves
# they compile, and nothing here proves CDI still spells a phase the way the
# list spells it. A phase renamed upstream reads as healthy, which is the
# direction this is deliberately wrong in -- see the comment in the RD.

REPO_ROOT="$(cd "$(dirname "${BATS_TEST_FILENAME:-$0}")/.." && pwd)"
CHART="$REPO_ROOT/packages/system/vm-disk-rd"

# CDI's DataVolumePhase values that mean work is in flight, and the settled
# ones that must never be called in flight. Both from
# staging/src/kubevirt.io/containerized-data-importer-api/pkg/apis/core/v1beta1.
# Failed is settled too, and is asserted through `failed` instead.
IN_FLIGHT_PHASES="Pending PVCBound ImportScheduled ImportInProgress CloneScheduled CloneInProgress SnapshotForSmartCloneInProgress CloneFromSnapshotSourceInProgress SmartClonePVCInProgress CSICloneInProgress PrepClaimInProgress RebindInProgress ExpansionInProgress NamespaceTransferInProgress UploadScheduled"
SETTLED_PHASES="Succeeded PendingPopulation WaitForFirstConsumer UploadReady Paused"

EXPECTED_FAILED="has(status.phase) && status.phase == 'Failed'"
# Not a bare `true`: the catch-all must still exclude the two shapes that mean
# CDI is not driving the disk anywhere -- phase Unknown, and a status with no
# phase key, which is where a DataVolume whose PVC spec could not be rendered
# (a StorageClass that does not exist, say) comes to rest.
EXPECTED_CURRENT="has(status.phase) && status.phase != 'Unknown'"
# The release waits for a user-sized data transfer, so it cannot run on the
# platform default install window.
EXPECTED_INSTALL_TIMEOUT="30m"

@test "vm-disk-rd declares exactly one DataVolume health check" {
  rendered=$(helm template vm-disk-rd "$CHART") || return 1

  if [ -z "$rendered" ]; then
    echo "helm template produced no output for $CHART -- guard is blind." >&2
    return 1
  fi

  count=$(printf '%s\n' "$rendered" | yq eval-all '[select(.metadata.name == "vm-disk") | .spec.release.healthCheckExprs[] | select(.kind == "DataVolume" and .apiVersion == "cdi.kubevirt.io/v1beta1")] | length' -) || return 1

  if [ "$count" != "1" ]; then
    echo "Expected exactly 1 DataVolume entry in spec.release.healthCheckExprs, got: $count" >&2
    echo "Zero means the HelmRelease goes Ready as soon as Helm applies the" >&2
    echo "DataVolume, and a disk that never populates stays green (#2908)." >&2
    return 1
  fi
}

@test "vm-disk-rd fails the release on a Failed DataVolume and passes everything else" {
  rendered=$(helm template vm-disk-rd "$CHART") || return 1

  if [ -z "$rendered" ]; then
    echo "helm template produced no output for $CHART -- guard is blind." >&2
    return 1
  fi

  actual_failed=$(printf '%s\n' "$rendered" | yq eval-all '[select(.metadata.name == "vm-disk") | .spec.release.healthCheckExprs[] | select(.kind == "DataVolume")] | .[0].failed' -) || return 1
  actual_current=$(printf '%s\n' "$rendered" | yq eval-all '[select(.metadata.name == "vm-disk") | .spec.release.healthCheckExprs[] | select(.kind == "DataVolume")] | .[0].current' -) || return 1

  if [ "$actual_failed" != "$EXPECTED_FAILED" ]; then
    echo "DataVolume health check 'failed' expression changed:" >&2
    echo "  actual:   $actual_failed" >&2
    echo "  expected: $EXPECTED_FAILED" >&2
    echo "This leg is the fast path for a DataVolume that does reach Failed;" >&2
    echo "most dead disks rest in an in-flight phase instead and are caught by" >&2
    echo "the install window. An expression that never holds leaves a check" >&2
    echo "that cannot fail." >&2
    return 1
  fi

  if [ "$actual_current" != "$EXPECTED_CURRENT" ]; then
    echo "DataVolume health check 'current' expression changed:" >&2
    echo "  actual:   $actual_current" >&2
    echo "  expected: $EXPECTED_CURRENT" >&2
    echo "current is the catch-all: it is what makes a phase this platform does" >&2
    echo "not know about read as healthy rather than hang the release." >&2
    return 1
  fi
}

@test "vm-disk-rd counts work in flight as in progress and settled phases as done" {
  rendered=$(helm template vm-disk-rd "$CHART") || return 1

  if [ -z "$rendered" ]; then
    echo "helm template produced no output for $CHART -- guard is blind." >&2
    return 1
  fi

  in_progress=$(printf '%s\n' "$rendered" | yq eval-all '[select(.metadata.name == "vm-disk") | .spec.release.healthCheckExprs[] | select(.kind == "DataVolume")] | .[0].inProgress' -) || return 1

  if [ -z "$in_progress" ] || [ "$in_progress" = "null" ]; then
    echo "The DataVolume health check has no inProgress expression." >&2
    echo "Without it every phase falls through to current, which is the" >&2
    echo "catch-all, so a populating disk reports ready immediately." >&2
    return 1
  fi

  # The phase checks below say which names appear, not how they are joined.
  # Both ends are anchored, because either end can invert the meaning while
  # every phase name stays in place: opening with `||` instead of `&&` makes
  # any DataVolume carrying a phase in flight forever, and a trailing `|| true`
  # does the same, while a trailing `&& false` sends every phase to the
  # catch-all and reports an importing disk ready — the failure this whole
  # file exists to keep out.
  case "$in_progress" in
    "has(status.phase) && status.phase in ["*"]") ;;
    *)
      echo "The inProgress expression is not exactly a guarded membership test:" >&2
      echo "  $in_progress" >&2
      echo "Expected it to start: has(status.phase) && status.phase in [" >&2
      echo "and to end at the closing ]. A form that still ends there but" >&2
      echo "joins another test onto it is caught by the phase checks below." >&2
      return 1
      ;;
  esac

  for phase in $IN_FLIGHT_PHASES; do
    if ! printf '%s' "$in_progress" | grep -q "'$phase'"; then
      echo "Phase $phase is missing from the inProgress expression:" >&2
      echo "  $in_progress" >&2
      echo "A disk stuck in it would report ready while nothing is populating it." >&2
      return 1
    fi
  done

  for phase in $SETTLED_PHASES; do
    if printf '%s' "$in_progress" | grep -q "'$phase'"; then
      echo "Settled phase $phase appears in the inProgress expression:" >&2
      echo "  $in_progress" >&2
      echo "PendingPopulation and WaitForFirstConsumer are where a disk rests" >&2
      echo "until a VM attaches it, and UploadReady where it rests until the" >&2
      echo "upload arrives. Calling any of them in flight reports every" >&2
      echo "untouched disk as broken and fails its release on the timeout." >&2
      return 1
    fi
  done

  # Every phase named must come from the in-flight list. A token in neither
  # list is a typo (silently never matching) or a phase from a CDI this list
  # has not been reviewed against.
  for token in $(printf '%s' "$in_progress" | grep -o "'[^']*'" | tr -d "'"); do
    case " $IN_FLIGHT_PHASES " in
      *" $token "*) ;;
      *)
        echo "Unrecognised phase '$token' in the inProgress expression:" >&2
        echo "  $in_progress" >&2
        echo "Add it to IN_FLIGHT_PHASES here if CDI really spells it that way." >&2
        return 1
        ;;
    esac
  done
}

# Both assertions here are negative, so they would pass on an empty selection.
# What keeps them honest is the count assertion in the first test, which fails
# loudly when nothing matches metadata.name == "vm-disk". Keep that test, and
# keep its selector in step with the ones below.
@test "vm-disk-rd leaves the health checks reachable" {
  rendered=$(helm template vm-disk-rd "$CHART") || return 1

  if [ -z "$rendered" ]; then
    echo "helm template produced no output for $CHART -- guard is blind." >&2
    return 1
  fi

  wait_strategy=$(printf '%s\n' "$rendered" | yq eval-all '[select(.metadata.name == "vm-disk") | .spec.release.waitStrategy] | .[0] // ""' -) || return 1
  disable_wait=$(printf '%s\n' "$rendered" | yq eval-all '[select(.metadata.name == "vm-disk") | .metadata.annotations."release.cozystack.io/helm-install-disable-wait"] | .[0] // ""' -) || return 1

  if [ -n "$wait_strategy" ] && [ "$wait_strategy" != "poller" ]; then
    echo "spec.release.waitStrategy is $wait_strategy, not poller." >&2
    echo "healthCheckExprs are evaluated under the poller strategy only, so" >&2
    echo "any other value makes them a silent no-op. Leaving it unset is fine:" >&2
    echo "the generated HelmRelease then defaults to poller because the" >&2
    echo "expressions are present." >&2
    return 1
  fi

  if [ "$disable_wait" = "true" ]; then
    echo "release.cozystack.io/helm-install-disable-wait is true." >&2
    echo "Upstream evaluates health checks only when the Helm action waits, so" >&2
    echo "this annotation makes them a no-op with no error and no warning." >&2
    return 1
  fi
}

@test "vm-disk-rd gives the import a window it can finish in" {
  rendered=$(helm template vm-disk-rd "$CHART") || return 1

  if [ -z "$rendered" ]; then
    echo "helm template produced no output for $CHART -- guard is blind." >&2
    return 1
  fi

  timeout=$(printf '%s\n' "$rendered" | yq eval-all '[select(.metadata.name == "vm-disk") | .metadata.annotations."release.cozystack.io/helm-install-timeout"] | .[0] // ""' -) || return 1

  if [ "$timeout" != "$EXPECTED_INSTALL_TIMEOUT" ]; then
    echo "release.cozystack.io/helm-install-timeout is \"$timeout\", expected \"$EXPECTED_INSTALL_TIMEOUT\"." >&2
    echo "The health checks above hold the release open until CDI has populated" >&2
    echo "the disk, so this window is how long a healthy import may take before" >&2
    echo "the application reports it as failed. Empty means the platform default" >&2
    echo "(10m), which a multi-gigabyte image beats routinely. Changing it is" >&2
    echo "fine; say in the RD why the new number is the right one." >&2
    return 1
  fi
}
