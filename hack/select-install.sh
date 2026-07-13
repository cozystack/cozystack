#!/bin/sh
# Given the E2E app suite(s) about to run, emit the minimal set of packages that
# must be INSTALLED for those suites — the forward dependency closure over the
# PackageSource graph. This is the install-side companion to select-e2e.sh:
# select-e2e.sh picks WHICH Chainsaw suites run (reverse dependency walk);
# select-install.sh picks WHAT must be up for them (forward dependency walk).
#
# Usage:
#   hack/select-install.sh <suites> [<sources-dir>]
#   hack/select-install.sh --validate [<sources-dir>]
#
# Defaults: sources-dir = packages/core/platform/sources
#
# <suites>: whitespace-separated suite names, i.e. the output of select-e2e.sh
#           (e.g. "postgres harbor"). "-" reads the list from stdin.
#
# Output (closure mode): space-separated PackageSource names (cozystack.*) that
#   the run must enable, seeds + all transitive dependsOn. Empty when <suites>
#   is empty.
#
# --validate: check the whole graph — every dependsOn target resolves to a real
#   PackageSource, and there are no dependency cycles. Exit non-zero on either.
#
# Unlike select-e2e.sh's reverse walk, the forward walk KEEPS the
# cozystack.cozystack-engine edge: every *-application declares
# `dependsOn: cozystack.cozystack-engine` because the app's *-rd HelmRelease
# needs the engine to register the ApplicationDefinition CRD before it can
# reconcile. For test SELECTION that edge is noise (it would fan every app out
# to every other); for INSTALL it is a genuine prerequisite that must be up.
set -eu

SOURCES_DIR="packages/core/platform/sources"
MODE="closure"
SUITES=""

if [ "${1:-}" = "--validate" ]; then
  MODE="validate"
  SOURCES_DIR="${2:-$SOURCES_DIR}"
else
  suites_arg="${1?usage: select-install.sh <suites> [sources-dir] | --validate [sources-dir]}"
  SOURCES_DIR="${2:-$SOURCES_DIR}"
  if [ "$suites_arg" = "-" ]; then
    SUITES="$(cat)"
  else
    SUITES="$suites_arg"
  fi
fi

# suite name -> owning PackageSource. Prints:
#   - a "cozystack.*" name         the suite's primary install target
#   - "-"                          the suite targets a core-platform feature that
#                                  has no separately-enabled package (always
#                                  present via the base install), e.g.
#                                  serviceexposure (ExposureClass/ServiceExposure
#                                  live in packages/core/platform)
#   - nothing                      no mapping is known (caller warns)
# Most suites follow the <suite>-application convention; a bare-<suite> fallback
# and the explicit cases cover suites whose source is named differently. Kept in
# lockstep with select-e2e.sh's src_to_suites() and the hack/e2e-chainsaw/ dirs.
#
# Note: a suite may exercise MORE than one package (e.g. securitygroup also
# stands up a Kubernetes app). Only the primary package is mapped here; the
# forward closure covers its own deps, and the full-suite fallback covers the
# rest. Modelling multi-package suites is deferred to the test-minimal work.
suite_to_source() {
  case "$1" in
    kubernetes-latest|kubernetes-previous|kubernetes-oidc-system|kubernetes-oidc-customconfig)
      echo cozystack.kubernetes-application ; return ;;
    vminstance) echo cozystack.vm-instance-application ; return ;;
    securitygroup) echo cozystack.securitygroup-controller ; return ;;
    serviceexposure) echo - ; return ;;
  esac
  for cand in "cozystack.$1-application" "cozystack.$1"; do
    if echo "$NODES" | grep -Fxq "$cand"; then echo "$cand"; return; fi
  done
}

# yq: forward edges — "owner<TAB>dep", one per variant dependsOn entry.
build_forward_deps() {
  yq -rN '.metadata.name as $n | .spec.variants[]?.dependsOn[]? | select(. != null and . != "") | $n + "\t" + .' "$SOURCES_DIR"/*.yaml
}

NODES="$(yq -rN '.metadata.name | select(. != null and . != "")' "$SOURCES_DIR"/*.yaml | sort -u)"
FORWARD="$(build_forward_deps | sort -u)"

# forward deps of a single node
deps_of() {
  echo "$FORWARD" | awk -v s="$1" -F'\t' '$1==s {print $2}'
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

  [ "$rc" = 0 ] && echo "select-install: graph OK ($(echo "$NODES" | grep -c .) sources)" >&2
  exit "$rc"
fi

# Closure mode: map suites to seed sources, then take the forward closure.
seeds=""
for suite in $SUITES; do
  [ -z "$suite" ] && continue
  src="$(suite_to_source "$suite")"
  case "$src" in
    "-") continue ;;  # core-platform feature — nothing separate to enable
    "")  echo "select-install: warning: suite '$suite' has no known PackageSource mapping; skipping" >&2 ; continue ;;
  esac
  if echo "$NODES" | grep -Fxq "$src"; then
    case " $seeds " in *" $src "*) ;; *) seeds="$seeds $src" ;; esac
  else
    echo "select-install: warning: suite '$suite' -> '$src' not found; skipping" >&2
  fi
done

[ -z "$seeds" ] && exit 0

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
