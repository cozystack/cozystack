#!/bin/sh
# Given the E2E app suite(s) about to run, emit the minimal set of packages that
# must be INSTALLED for those suites — the forward dependency closure over the
# PackageSource graph. This is the install-side companion to select-e2e.sh:
# select-e2e.sh picks WHICH Chainsaw suites run (reverse dependency walk);
# select-install.sh picks WHAT must be up for them (forward dependency walk).
#
# Usage:
#   hack/select-install.sh <suites> [<sources-dir>]
#   hack/select-install.sh --validate [<sources-dir> [<suites-dir>]]
#
# Defaults: sources-dir = packages/core/platform/sources
#           suites-dir  = hack/e2e-chainsaw
#
# <suites>: whitespace-separated suite names, i.e. the output of select-e2e.sh
#           (e.g. "postgres harbor"). "-" reads the list from stdin.
#
# Output (closure mode): space-separated PackageSource names (cozystack.*) that
#   the run must enable, seeds + the engine + all transitive dependsOn. Empty
#   when <suites> is empty. A suite with no known mapping is a HARD ERROR (fail
#   closed) — a silently-skipped suite would install nothing and let the test
#   pass vacuously or fail later on a missing CRD.
#
# --validate: check the whole graph AND the suite mapping — every dependsOn
#   target resolves to a real PackageSource, there are no dependency cycles, and
#   every Chainsaw suite under <suites-dir> resolves via suite_to_source() to a
#   real PackageSource. Exit non-zero on any failure. The graph lives
#   next to the packages and cannot drift from the suite directories; the
#   hand-maintained suite mapping is the only part that can, so it is guarded here.
#
# Unlike select-e2e.sh's reverse walk, the forward walk KEEPS the
# cozystack.cozystack-engine edge: every *-application declares
# `dependsOn: cozystack.cozystack-engine` because the app's *-rd HelmRelease
# needs the engine to register the ApplicationDefinition CRD before it can
# reconcile. For test SELECTION that edge is noise (it would fan every app out
# to every other); for INSTALL it is a genuine prerequisite that must be up.
# Suites backed by a system package (e.g. securitygroup) carry no such edge, so
# the engine is seeded explicitly for any non-empty closure (see below).
set -eu

SOURCES_DIR="packages/core/platform/sources"
SUITES_DIR="hack/e2e-chainsaw"
MODE="closure"
SUITES=""

if [ "${1:-}" = "--validate" ]; then
  MODE="validate"
  SOURCES_DIR="${2:-$SOURCES_DIR}"
  SUITES_DIR="${3:-$SUITES_DIR}"
else
  suites_arg="${1?usage: select-install.sh <suites> [sources-dir] | --validate [sources-dir [suites-dir]]}"
  SOURCES_DIR="${2:-$SOURCES_DIR}"
  if [ "$suites_arg" = "-" ]; then
    SUITES="$(cat)"
  else
    SUITES="$suites_arg"
  fi
fi

# suite name -> owning PackageSource. Prints:
#   - a "cozystack.*" name         the suite's primary install target
#   - nothing                      no mapping is known (caller fails closed)
# Most suites follow the <suite>-application convention; a bare-<suite> fallback
# and the explicit cases cover suites whose source is named differently. Kept in
# lockstep with select-e2e.sh's src_to_suites() and the hack/e2e-chainsaw/ dirs;
# --validate asserts every suite dir still resolves here.
#
# Note: a suite may exercise fixtures or selectors that name other app kinds
# (e.g. securitygroup's SecurityGroup references Postgres/Kubernetes by kind and
# name as selectors — it does not stand those apps up). Only the primary package
# is mapped here; the forward closure covers its own deps, the seeded engine
# covers the aggregated *.cozystack.io APIs, and the full-suite fallback covers
# the rest. Modelling multi-package suites is deferred to the test-minimal work.
suite_to_source() {
  case "$1" in
    kubernetes-latest|kubernetes-previous|kubernetes-oidc-system|kubernetes-oidc-customconfig)
      echo cozystack.kubernetes-application ; return ;;
    vminstance) echo cozystack.vm-instance-application ; return ;;
    securitygroup) echo cozystack.securitygroup-controller ; return ;;
  esac
  for cand in "cozystack.$1-application" "cozystack.$1"; do
    if echo "$NODES" | grep -Fxq "$cand"; then echo "$cand"; return; fi
  done
}

# yq: forward edges — "owner<TAB>dep", one per variant dependsOn entry.
# NOTE: this unions dependsOn across ALL spec.variants[] rather than the single
# variant actually installed (e.g. cozystack.networking has 6 variants; only 5
# declare gateway-api-crds). That OVER-selects, never under-selects, so the
# emitted set is a safe superset of the true minimal set for the chosen variant.
build_forward_deps() {
  yq -rN '.metadata.name as $n | .spec.variants[]?.dependsOn[]? | select(. != null and . != "") | $n + "\t" + .' "$SOURCES_DIR"/*.yaml
}

NODES="$(yq -rN '.metadata.name | select(. != null and . != "")' "$SOURCES_DIR"/*.yaml | sort -u)"
FORWARD="$(build_forward_deps | sort -u)"

# forward deps of a single node
deps_of() {
  echo "$FORWARD" | awk -v s="$1" -F'\t' '$1==s {print $2}'
}

# all Chainsaw suite names under a suites-dir (dirs holding chainsaw-test.yaml),
# discovered exactly like select-e2e.sh.
discover_suites() {
  find "$1" -mindepth 2 -maxdepth 2 -name chainsaw-test.yaml 2>/dev/null \
    | sed -e 's,/chainsaw-test\.yaml$,,' -e 's,.*/,,' | sort
}

if [ "$MODE" = "validate" ]; then
  rc=0

  # 1. Reachability: every dependsOn target must be a real PackageSource.
  echo "$FORWARD" | awk -F'\t' '{print $2}' | sort -u | while IFS= read -r dep; do
    [ -z "$dep" ] && continue
    if ! echo "$NODES" | grep -Fxq "$dep"; then
      owners="$(echo "$FORWARD" | awk -v x="$dep" -F'\t' '$2==x {print $1}' | paste -sd ',' -)"
      echo "select-install: dangling dependency '$dep' (referenced by: $owners) has no PackageSource" >&2
      echo dangling
    fi
  done | grep -q dangling && rc=1

  # 2. Cycles: from every node with outgoing edges, walk forward; if the walk
  #    returns to its start, the graph has a cycle through it.
  owners_list="$(echo "$FORWARD" | awk -F'\t' '{print $1}' | sort -u)"
  for start in $owners_list; do
    visited=""
    frontier="$(deps_of "$start")"
    while [ -n "$frontier" ]; do
      next=""
      for f in $frontier; do
        if [ "$f" = "$start" ]; then
          echo "select-install: dependency cycle detected involving '$start'" >&2
          rc=1
          next=""
          break
        fi
        case " $visited " in *" $f "*) continue ;; esac
        visited="$visited $f"
        next="$next $(deps_of "$f")"
      done
      frontier="$next"
    done
  done

  # 3. Suite mapping: every Chainsaw suite dir must resolve to a real source.
  #    The suite universe is auto-discovered while suite_to_source() is
  #    hand-maintained, so this is the part that actually drifts. A missing
  #    suites dir is itself a failure — otherwise a moved/misspelled path would
  #    make discovery yield nothing and silently pass this whole check.
  if [ ! -d "$SUITES_DIR" ]; then
    echo "select-install: suites dir '$SUITES_DIR' does not exist" >&2
    rc=1
  else
    for suite in $(discover_suites "$SUITES_DIR"); do
      src="$(suite_to_source "$suite")"
      if [ -z "$src" ]; then
        echo "select-install: suite '$suite' has no PackageSource mapping (add it to suite_to_source)" >&2
        rc=1
      elif ! echo "$NODES" | grep -Fxq "$src"; then
        echo "select-install: suite '$suite' maps to '$src', not a PackageSource in $SOURCES_DIR" >&2
        rc=1
      fi
    done
  fi

  [ "$rc" = 0 ] && echo "select-install: graph OK ($(echo "$NODES" | grep -c .) sources)" >&2
  exit "$rc"
fi

# Closure mode: map suites to seed sources, then take the forward closure.
seeds=""
map_rc=0
for suite in $SUITES; do
  [ -z "$suite" ] && continue
  src="$(suite_to_source "$suite")"
  case "$src" in
    "")
      # Fail closed: a suite that maps to nothing must abort, not silently emit
      # an empty set. Collect every bad suite so one run reports them all.
      echo "select-install: error: suite '$suite' has no known PackageSource mapping (add it to suite_to_source)" >&2
      map_rc=1
      continue ;;
  esac
  if echo "$NODES" | grep -Fxq "$src"; then
    case " $seeds " in *" $src "*) ;; *) seeds="$seeds $src" ;; esac
  else
    echo "select-install: error: suite '$suite' -> '$src' is not a PackageSource in $SOURCES_DIR" >&2
    map_rc=1
  fi
done
[ "$map_rc" = 0 ] || exit "$map_rc"

[ -z "$seeds" ] && exit 0

# Any non-empty install set needs the engine up: it registers the
# ApplicationDefinition CRD every *-rd HelmRelease reconciles against, and its
# cozystack-api component serves the aggregated *.cozystack.io APIs some suites
# exercise (e.g. securitygroup -> sdn.cozystack.io). *-application sources carry
# a dependsOn edge to it; system-package-backed suites reach it via no edge, so
# seed it explicitly here. Its absence from the graph is a hard error — nothing
# can install without it.
engine="cozystack.cozystack-engine"
if echo "$NODES" | grep -Fxq "$engine"; then
  case " $seeds " in *" $engine "*) ;; *) seeds="$seeds $engine" ;; esac
else
  echo "select-install: error: required engine PackageSource '$engine' not found in $SOURCES_DIR" >&2
  exit 1
fi

all="$seeds"
while :; do
  new=""
  for s in $all; do
    for d in $(deps_of "$s"); do
      case " $all $new " in *" $d "*) ;; *) new="$new $d" ;; esac
    done
  done
  [ -z "$new" ] && break
  all="$all $new"
done

echo "$all" | tr ' ' '\n' | grep -v '^$' | sort -u | paste -sd ' ' -
