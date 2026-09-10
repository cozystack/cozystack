#!/bin/sh
# Given the E2E app suite(s) about to run, emit the conservative set of packages
# that must be INSTALLED for those suites: the suite owners, the baseline needed
# by the E2E install itself, and their forward PackageSource dependency closure.
# This is the install-side companion to select-e2e.sh:
# select-e2e.sh picks WHICH Chainsaw suites run (reverse dependency walk);
# select-install.sh picks WHAT must be up for them (forward dependency walk).
#
# Usage:
#   hack/select-install.sh <suites> [<sources-dir>]
#   hack/select-install.sh --disabled <suites> [<sources-dir>]
#   hack/select-install.sh --validate [<sources-dir> [<suites-dir>]]
#
# Defaults: sources-dir = packages/core/platform/sources
#           suites-dir  = hack/e2e-chainsaw
#
# <suites>: whitespace-separated suite names, i.e. the output of select-e2e.sh
#           (e.g. "postgres harbor"). "-" reads the list from stdin.
#
# Output (closure mode): space-separated PackageSource names (cozystack.*) that
#   the run must enable, seeds + the platform baseline + all transitive
#   dependsOn. Empty when <suites> is empty.
#
# Output (--disabled): the COMPLEMENT of that closure, for bundles.disabledPackages
#   in the platform chart. The base package helper in
#   packages/core/platform/templates/_helpers.tpl consults disabledPackages for
#   every package rather than only the optional ones, so an ordinary variant
#   can be reduced by subtraction with no new variant. Refuses on an empty
#   selection, where the complement would be the whole platform.
#
#   Two limits worth knowing before consuming it.
#
#   Subtraction governs a FRESH install only. Both package defines stamp
#   `helm.sh/resource-policy: keep`, so adding a name on a live cluster stops
#   emitting the Package CR but does not remove the one already there; the
#   package stays installed. Fine for a per-run E2E install, wrong as a way to
#   uninstall.
#
#   Packages emitted through package.optional (nfs-driver, telepresence,
#   external-dns, external-dns-application, and the others gated the same way)
#   need bundles.enabledPackages as well. Keeping one in the closure is
#   therefore not enough to install it, and a suite whose closure contains one
#   needs that list set separately. Subtraction is correct for them either way,
#   since an optional package absent from enabledPackages is not installed
#   regardless.
#
#   The declared graph is not the whole runtime contract. The standard E2E
#   install waits for storage, load-balancer, CAPI and OIDC components which no
#   selected application necessarily reaches, so they are explicit baseline
#   seeds below. This mode describes a FRESH install using that audited path;
#   live convergence still has to be proved separately. A suite with no known
#   mapping is a HARD ERROR (fail closed) — a silently-skipped suite would
#   install nothing and let the test pass vacuously or fail later on a missing
#   CRD.
#
# --validate: check the whole graph AND the suite mapping — every dependsOn
#   target resolves to a real PackageSource, there are no dependency cycles, and
#   every Chainsaw suite under <suites-dir> resolves via suite_to_source() to
#   one or more real PackageSources. Exit non-zero on any failure. The graph
#   lives next to the packages and cannot drift from the suite directories; the
#   hand-maintained suite mapping is the only part that can, so it is guarded
#   here.
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

if [ "${1:-}" = "--disabled" ]; then
  MODE="disabled"
  shift
  suites_arg="${1?usage: select-install.sh --disabled <suites> [sources-dir]}"
  SOURCES_DIR="${2:-$SOURCES_DIR}"
  if [ "$suites_arg" = "-" ]; then
    SUITES="$(cat)"
  else
    SUITES="$suites_arg"
  fi
elif [ "${1:-}" = "--validate" ]; then
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

# suite name -> owning PackageSource(s). Prints:
#   - one or more "cozystack.*" names   every direct install target
#   - nothing                           no mapping is known (caller fails closed)
# Most suites follow the <suite>-application convention; a bare-<suite> fallback
# and the explicit cases cover suites whose source is named differently. Kept in
# lockstep with select-e2e.sh's src_to_suites() and the hack/e2e-chainsaw/ dirs;
# --validate asserts every suite dir still resolves here.
#
# A suite may have more than one direct owner. In particular, vminstance creates
# both VMInstance and VMDisk resources and observes the DataVolume behind the
# disk, so both application sources are conjunctive inputs. Fixtures which only
# name other app kinds as selectors are not owners.
suite_to_source() {
  case "$1" in
    kubernetes-latest|kubernetes-previous)
      echo cozystack.kubernetes-application ; return ;;
    vminstance)
      echo "cozystack.vm-instance-application cozystack.vm-disk-application"
      return ;;
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

# Capture each producer before sorting. This file runs under dash through
# cozytest, so pipefail is not available; checking the producer explicitly is
# what prevents a failed yq from turning into a successful empty graph.
if ! nodes_raw=$(yq -rN '.metadata.name | select(. != null and . != "")' "$SOURCES_DIR"/*.yaml); then
  echo "select-install: yq failed while reading PackageSource names from $SOURCES_DIR" >&2
  exit 1
fi
NODES=$(printf '%s\n' "$nodes_raw" | sort -u)
if [ -z "$NODES" ]; then
  echo "select-install: found no PackageSources in $SOURCES_DIR" >&2
  exit 1
fi

if ! forward_raw=$(build_forward_deps); then
  echo "select-install: yq failed while reading PackageSource dependencies from $SOURCES_DIR" >&2
  exit 1
fi
FORWARD=$(printf '%s\n' "$forward_raw" | sort -u)

# forward deps of a single node
deps_of() {
  echo "$FORWARD" | awk -v s="$1" -F'\t' '$1==s {print $2}'
}

# all Chainsaw suite names under a suites-dir (dirs holding chainsaw-test.yaml),
# discovered exactly like select-e2e.sh.
discover_suites() {
  if ! suite_files=$(find "$1" -mindepth 2 -maxdepth 2 -name chainsaw-test.yaml); then
    echo "select-install: find failed listing Chainsaw suites under $1" >&2
    return 1
  fi
  if [ -z "$suite_files" ]; then
    echo "select-install: found no chainsaw-test.yaml under $1" >&2
    return 1
  fi
  printf '%s\n' "$suite_files" | sed -e 's,/chainsaw-test\.yaml$,,' -e 's,.*/,,' | sort
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
  elif ! suites=$(discover_suites "$SUITES_DIR"); then
    rc=1
  else
    for suite in $suites; do
      owners="$(suite_to_source "$suite")"
      if [ -z "$owners" ]; then
        echo "select-install: suite '$suite' has no PackageSource mapping (add it to suite_to_source)" >&2
        rc=1
        continue
      fi
      for src in $owners; do
        if ! echo "$NODES" | grep -Fxq "$src"; then
          echo "select-install: suite '$suite' maps to '$src', not a PackageSource in $SOURCES_DIR" >&2
          rc=1
        fi
      done
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
  owners="$(suite_to_source "$suite")"
  if [ -z "$owners" ]; then
    # Fail closed: a suite that maps to nothing must abort, not silently emit an
    # empty set. Collect every bad suite so one run reports them all.
    echo "select-install: error: suite '$suite' has no known PackageSource mapping (add it to suite_to_source)" >&2
    map_rc=1
    continue
  fi
  for src in $owners; do
    if echo "$NODES" | grep -Fxq "$src"; then
      case " $seeds " in *" $src "*) ;; *) seeds="$seeds $src" ;; esac
    else
      echo "select-install: error: suite '$suite' -> '$src' is not a PackageSource in $SOURCES_DIR" >&2
      map_rc=1
    fi
  done
done
[ "$map_rc" = 0 ] || exit "$map_rc"

# An empty seed set means no suite was selected. In closure mode that is a valid
# answer -- install nothing extra. In disabled mode it is not: the complement of
# an empty closure is the whole platform, which as an install instruction is a
# platform with no packages at all, so refuse rather than emit it.
if [ -z "$seeds" ]; then
  if [ "$MODE" = "disabled" ]; then
    echo "select-install: refusing to emit a disable list for an empty suite selection — the complement would be the entire platform" >&2
    exit 1
  fi
  exit 0
fi

# The standard E2E install has unconditional runtime requirements which are not
# owned by any selected application. Seed those direct requirements here rather
# than inventing PackageSource edges: graph edges also drive select-e2e.sh's
# reverse test selection, while these dependencies belong to the install harness.
#
# Root platform: aggregated APIs, tenant namespaces and the four root services
# are all waited before a selected suite can finish. Storage/network prep waits
# LINSTOR, CDI and MetalLB. The install then waits the CAPI operator plus all four
# providers, and later enables and waits the Keycloak OIDC stack. Kamaji,
# KubeVirt, storage controllers and CRDs remain graph-derived so losing those
# declared edges stays observable in tests.
baseline="cozystack.cozystack-engine
cozystack.cozystack-basics
cozystack.tenant-application
cozystack.etcd-application
cozystack.ingress-application
cozystack.monitoring-application
cozystack.seaweedfs-application
cozystack.linstor
cozystack.kubevirt-cdi
cozystack.metallb
cozystack.capi-operator
cozystack.capi-provider-bootstrap-kubeadm
cozystack.capi-provider-core
cozystack.capi-provider-cp-kamaji
cozystack.capi-provider-infra-kubevirt
cozystack.keycloak
cozystack.keycloak-operator"

missing=""
for b in $baseline; do
  if echo "$NODES" | grep -Fxq "$b"; then
    case " $seeds " in *" $b "*) ;; *) seeds="$seeds $b" ;; esac
  else
    missing="${missing:+$missing }$b"
  fi
done
if [ -n "$missing" ]; then
  echo "select-install: error: required baseline PackageSource(s) not found in $SOURCES_DIR: $missing" >&2
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

closure="$(echo "$all" | tr ' ' '\n' | grep -v '^$' | sort -u)"

# These runtime prerequisites are intentionally graph-derived rather than
# redundant seeds. That keeps their PackageSource edges meaningful: if a CAPI
# provider loses Kamaji or KubeVirt, or LINSTOR loses one of its controllers,
# the selector fails closed instead of silently emitting an under-sized install.
derived_baseline="cozystack.cert-manager
cozystack.networking
cozystack.gateway-api-crds
cozystack.etcd-operator
cozystack.grafana-operator
cozystack.postgres-operator
cozystack.victoria-metrics-operator
cozystack.prometheus-operator-crds
cozystack.vertical-pod-autoscaler
cozystack.reloader
cozystack.snapshot-controller
cozystack.gateway-application
cozystack.info-application
cozystack.kamaji
cozystack.kubevirt"
missing=""
for required in $derived_baseline; do
  if ! echo "$closure" | grep -Fxq "$required"; then
    missing="${missing:+$missing }$required"
  fi
done
if [ -n "$missing" ]; then
  echo "select-install: error: runtime baseline dependency closure is missing: $missing" >&2
  exit 1
fi

if [ "$MODE" = "disabled" ]; then
  # The complement is every PackageSource minus the audited baseline, selected
  # owners and their declared forward closure. No declared dependency of a kept
  # package can enter it. The explicit baseline covers the current install
  # harness's known undeclared requirements; only a fresh live run can establish
  # that no other operational dependency is missing.
  echo "$NODES" | grep -vxF "$closure" | paste -sd ' ' -
  exit 0
fi

echo "$closure" | paste -sd ' ' -
