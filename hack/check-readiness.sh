#!/usr/bin/env bash
# Check readiness of cozystack / Flux / Kubernetes resources.
# Shows only non-ready resources, parsed from status conditions (not column heuristics).
#
# Fetches sequentially by default — intended to run during cozystack upgrades where
# the apiserver is already under heavy reconciler load, so we avoid bursts of N
# concurrent LIST requests. Use --parallel for one-shot interactive runs against a
# healthy cluster (3-4x faster wall-clock).
#
# Usage:
#   check-readiness.sh [OPTIONS]
#
# Options:
#   -w, --watch [INTERVAL]    Watch mode; refresh every INTERVAL seconds (default: 10)
#       --wait                Block until everything is ready (exit 0) or timeout (exit 1)
#       --timeout DURATION    Timeout for --wait, e.g. 30m, 1h, 600s (default: 30m)
#       --parallel            Fire all fetches concurrently (faster, more apiserver load)
#       --core                Check only essential cozystack/Flux kinds (5 instead of 14)
#   -v, --verbose             Show condition reason/message for not-ready rows
#   -n, --namespace NS        Scope to a single namespace (cluster-scoped kinds ignore this)
#   -l, --selector SELECTOR   kubectl label selector to apply to every query
#       --no-color            Disable color output (auto-disabled on non-TTY)
#   -h, --help                Show this help

set -euo pipefail

KUBECTL=(kubectl)
if [[ -n "${KUBECONFIG:-}" ]]; then
  KUBECTL+=(--kubeconfig="${KUBECONFIG}")
fi

WATCH=0
INTERVAL=10
WAIT_MODE=0
TIMEOUT_RAW="30m"
VERBOSE=0
NAMESPACE=""
SELECTOR=""
USE_COLOR=1
PARALLEL=0
CORE_ONLY=0

usage() {
  sed -n '2,24p' "$0" | sed 's/^# \{0,1\}//'
}

# Resource catalog. Each entry: "kind|scope|conditionType|supportsSuspend"
#   scope: namespaced | cluster
#   conditionType: which .type to read from .status.conditions (or "_phase" for PVCs)
#   supportsSuspend: 1 if .spec.suspend should be checked, 0 otherwise
#
# CORE_KINDS is the upgrade-safe subset — the 5 primitives that actually drive a
# cozystack install. --core limits the script to these to minimize apiserver load.
CORE_KINDS=(
  "packages.cozystack.io|cluster|Ready|0"
  "artifactgenerators.source.extensions.fluxcd.io|namespaced|Ready|1"
  "externalartifacts.source.toolkit.fluxcd.io|namespaced|Ready|0"
  "helmreleases.helm.toolkit.fluxcd.io|namespaced|Ready|1"
  "kustomizations.kustomize.toolkit.fluxcd.io|namespaced|Ready|1"
)

# Additional Flux source kinds (skipped under --core).
FLUX_EXTRA_KINDS=(
  "gitrepositories.source.toolkit.fluxcd.io|namespaced|Ready|1"
  "ocirepositories.source.toolkit.fluxcd.io|namespaced|Ready|1"
  "helmrepositories.source.toolkit.fluxcd.io|namespaced|Ready|1"
  "helmcharts.source.toolkit.fluxcd.io|namespaced|Ready|1"
  "buckets.source.toolkit.fluxcd.io|namespaced|Ready|1"
)

CLUSTER_KINDS=(
  "nodes|cluster|Ready|0"
  "apiservices.apiregistration.k8s.io|cluster|Available|0"
  "customresourcedefinitions.apiextensions.k8s.io|cluster|Established|0"
)

PVC_KIND="persistentvolumeclaims|namespaced|_phase|0"

while [[ $# -gt 0 ]]; do
  case "$1" in
    -w|--watch)
      WATCH=1
      if [[ $# -gt 1 && "$2" =~ ^[0-9]+$ ]]; then
        INTERVAL="$2"
        shift
      fi
      shift
      ;;
    --wait)
      WAIT_MODE=1
      shift
      ;;
    --timeout)
      [[ $# -ge 2 ]] || { echo "--timeout requires a value" >&2; exit 2; }
      TIMEOUT_RAW="$2"
      shift 2
      ;;
    -v|--verbose)
      VERBOSE=1
      shift
      ;;
    -n|--namespace)
      [[ $# -ge 2 ]] || { echo "--namespace requires a value" >&2; exit 2; }
      NAMESPACE="$2"
      shift 2
      ;;
    -l|--selector)
      [[ $# -ge 2 ]] || { echo "--selector requires a value" >&2; exit 2; }
      SELECTOR="$2"
      shift 2
      ;;
    --no-color)
      USE_COLOR=0
      shift
      ;;
    --parallel)
      PARALLEL=1
      shift
      ;;
    --core)
      CORE_ONLY=1
      shift
      ;;
    -h|--help)
      usage
      exit 0
      ;;
    *)
      echo "Unknown argument: $1" >&2
      usage >&2
      exit 2
      ;;
  esac
done

# Auto-disable color on non-TTY (e.g. when piping to a file or `less`).
if [[ ! -t 1 ]]; then
  USE_COLOR=0
fi

if [[ $USE_COLOR -eq 1 ]]; then
  RED=$'\033[0;31m'
  GREEN=$'\033[0;32m'
  YELLOW=$'\033[1;33m'
  CYAN=$'\033[0;36m'
  DIM=$'\033[2m'
  BOLD=$'\033[1m'
  RESET=$'\033[0m'
else
  RED='' GREEN='' YELLOW='' CYAN='' DIM='' BOLD='' RESET=''
fi

# Parse "30m", "1h", "600s", "600" into seconds.
parse_timeout() {
  local raw="$1"
  if [[ "$raw" =~ ^([0-9]+)([smh]?)$ ]]; then
    local n="${BASH_REMATCH[1]}"
    local unit="${BASH_REMATCH[2]:-s}"
    case "$unit" in
      s) echo "$n" ;;
      m) echo $((n * 60)) ;;
      h) echo $((n * 3600)) ;;
    esac
  else
    echo "Invalid --timeout value: $raw (expected e.g. 30m, 1h, 600s)" >&2
    exit 2
  fi
}

TIMEOUT_SEC=$(parse_timeout "$TIMEOUT_RAW")

# Build the namespace/selector argument list once.
build_kubectl_scope_args() {
  local scope="$1"
  local -n out_ref="$2"
  out_ref=()
  if [[ "$scope" == "namespaced" ]]; then
    if [[ -n "$NAMESPACE" ]]; then
      out_ref+=(-n "$NAMESPACE")
    else
      out_ref+=(-A)
    fi
  fi
  if [[ -n "$SELECTOR" ]]; then
    out_ref+=(-l "$SELECTOR")
  fi
}

# Cache `kubectl api-resources` once — calling it per kind is slow and pipeline-fragile.
__API_RESOURCES_CACHE=""
__API_RESOURCES_CACHED=0

ensure_api_resources_cached() {
  if [[ $__API_RESOURCES_CACHED -eq 0 ]]; then
    __API_RESOURCES_CACHE=$("${KUBECTL[@]}" api-resources --no-headers 2>/dev/null || true)
    __API_RESOURCES_CACHED=1
  fi
}

# Does the API server know about this CRD/kind?
# `kind` may be the bare resource name ("nodes") or "name.group" ("packages.cozystack.io").
# api-resources columns: NAME [SHORTNAMES] APIVERSION NAMESPACED KIND
# (SHORTNAMES is omitted entirely when there are none, so column index shifts.)
kind_exists() {
  local kind="$1"
  ensure_api_resources_cached
  # If we couldn't list resources at all (e.g. API server unreachable), assume the
  # kind exists and let the actual fetch fail with a real error.
  [[ -z "$__API_RESOURCES_CACHE" ]] && return 0

  local name="${kind%%.*}"
  local group=""
  [[ "$kind" == *.* ]] && group="${kind#*.}"

  awk -v n="$name" -v g="$group" '
    $1 == n {
      apiv = $(NF-2)
      m = split(apiv, parts, "/")
      grp = (m == 2 ? parts[1] : "")
      if (g == "" || g == grp) found = 1
    }
    END { exit !found }
  ' <<<"$__API_RESOURCES_CACHE"
}

# Format reason/message into a short suffix.
format_extra() {
  local reason="$1" message="$2"
  local extra=""
  if [[ -n "$reason" && "$reason" != "<none>" ]]; then
    extra="${reason}"
  fi
  if [[ -n "$message" && "$message" != "<none>" ]]; then
    # Trim to first line + cap at 120 chars to keep output readable.
    local trimmed
    trimmed=$(echo "$message" | head -n1 | cut -c1-120)
    extra="${extra:+$extra: }${trimmed}"
  fi
  echo "$extra"
}

# Returns one row per object, fields separated by ASCII Unit Separator (0x1f):
#   namespace<US>name<US>status<US>reason<US>message<US>suspended
#
# Why \x1f and not \t: bash `read -d ... IFS=$'\t'` treats tab as whitespace IFS
# and silently strips leading empty fields — so a cluster-scoped row that starts
# with an empty namespace would shift every subsequent column. The Unit Separator
# is non-whitespace, so leading empty fields survive intact.
#
# `status` is the literal condition value ("True"/"False"/"Unknown"/"") or the
# phase value for PVCs.
fetch_rows() {
  local kind="$1" scope="$2" cond_type="$3" supports_suspend="$4"
  local args=()
  build_kubectl_scope_args "$scope" args

  local jsonpath
  if [[ "$cond_type" == "_phase" ]]; then
    jsonpath='{range .items[*]}{.metadata.namespace}{"\x1f"}{.metadata.name}{"\x1f"}{.status.phase}{"\x1f"}{""}{"\x1f"}{""}{"\x1f"}{"false"}{"\n"}{end}'
  else
    local suspend_field='{"false"}'
    if [[ "$supports_suspend" == "1" ]]; then
      suspend_field='{.spec.suspend}'
    fi
    jsonpath='{range .items[*]}'
    jsonpath+='{.metadata.namespace}{"\x1f"}'
    jsonpath+='{.metadata.name}{"\x1f"}'
    jsonpath+='{.status.conditions[?(@.type=="'"$cond_type"'")].status}{"\x1f"}'
    jsonpath+='{.status.conditions[?(@.type=="'"$cond_type"'")].reason}{"\x1f"}'
    jsonpath+='{.status.conditions[?(@.type=="'"$cond_type"'")].message}{"\x1f"}'
    jsonpath+="${suspend_field}"'{"\n"}'
    jsonpath+='{end}'
  fi

  "${KUBECTL[@]}" get "$kind" "${args[@]}" -o jsonpath="$jsonpath" 2>/dev/null || true
}

process_rows() {
  local entry="$1" rows_file="$2"
  local kind scope cond_type supports_suspend
  IFS='|' read -r kind scope cond_type supports_suspend <<<"$entry"

  [[ -s "$rows_file" ]] || return 0

  local header_printed=0 not_ready_count=0 total=0

  while IFS=$'\x1f' read -r ns name status reason message suspended; do
    [[ -z "$name" ]] && continue
    total=$((total + 1))

    local label="" color=""
    if [[ "$suspended" == "true" ]]; then
      label="SUSPENDED"
      color="$CYAN"
    elif [[ "$cond_type" == "_phase" ]]; then
      if [[ "$status" != "Bound" ]]; then
        label="$status"
        color="$RED"
      fi
    else
      if [[ "$status" != "True" ]]; then
        label="${status:-Unknown}"
        color="$RED"
      fi
    fi

    [[ -z "$label" ]] && continue

    if [[ $header_printed -eq 0 ]]; then
      echo -e "${BOLD}${YELLOW}=== ${kind} (not ready) ===${RESET}"
      header_printed=1
    fi
    not_ready_count=$((not_ready_count + 1))

    local prefix
    if [[ -n "$ns" ]]; then
      prefix=$(printf '%-30s %-50s %s' "$ns" "$name" "$label")
    else
      prefix=$(printf '%-50s %s' "$name" "$label")
    fi
    echo -e "${color}${prefix}${RESET}"

    if [[ $VERBOSE -eq 1 ]]; then
      local extra
      extra=$(format_extra "$reason" "$message")
      [[ -n "$extra" ]] && echo -e "  ${DIM}${extra}${RESET}"
    fi

    # SUSPENDED is a warning, not a hard failure.
    [[ "$label" != "SUSPENDED" ]] && found_issues=1
  done < "$rows_file"

  if [[ $header_printed -eq 1 && $VERBOSE -eq 1 ]]; then
    echo -e "${DIM}  -> ${not_ready_count}/${total} not ready${RESET}"
  fi
}

run_once() {
  found_issues=0
  local tmpdir
  tmpdir=$(mktemp -d -t check-readiness.XXXXXX)

  ensure_api_resources_cached

  local -a entries=("${CORE_KINDS[@]}")
  if [[ $CORE_ONLY -eq 0 ]]; then
    entries+=("${FLUX_EXTRA_KINDS[@]}" "${CLUSTER_KINDS[@]}" "$PVC_KIND")
  fi

  local i entry kind k scope cond_type supports_suspend

  # Phase 1: fetch rows. Default is sequential to avoid a burst of N concurrent
  # LIST requests during upgrades (the apiserver watch cache is hot then, and
  # reconcilers are already hammering it). --parallel opts into burst mode.
  if [[ $PARALLEL -eq 1 ]]; then
    local -a pids=()
    for i in "${!entries[@]}"; do
      entry="${entries[$i]}"
      kind="${entry%%|*}"
      if kind_exists "$kind"; then
        IFS='|' read -r k scope cond_type supports_suspend <<<"$entry"
        fetch_rows "$k" "$scope" "$cond_type" "$supports_suspend" > "$tmpdir/$i" 2>/dev/null &
        pids+=($!)
      fi
    done
    if [[ ${#pids[@]} -gt 0 ]]; then
      wait "${pids[@]}" 2>/dev/null || true
    fi
  else
    for i in "${!entries[@]}"; do
      entry="${entries[$i]}"
      kind="${entry%%|*}"
      if kind_exists "$kind"; then
        IFS='|' read -r k scope cond_type supports_suspend <<<"$entry"
        fetch_rows "$k" "$scope" "$cond_type" "$supports_suspend" > "$tmpdir/$i" 2>/dev/null
      fi
    done
  fi

  # Phase 2: process in catalog order.
  for i in "${!entries[@]}"; do
    entry="${entries[$i]}"
    kind="${entry%%|*}"
    if ! kind_exists "$kind"; then
      echo -e "${DIM}--- ${kind}: CRD not installed, skipping${RESET}"
      continue
    fi
    process_rows "$entry" "$tmpdir/$i"
  done

  rm -rf "$tmpdir"

  if [[ $found_issues -eq 0 ]]; then
    echo -e "${GREEN}${BOLD}All resources are ready.${RESET}"
  fi
}

# tput-based smooth refresh: home cursor and clear-to-end-of-screen, no flicker.
clear_screen_smooth() {
  if [[ $USE_COLOR -eq 1 ]] && command -v tput >/dev/null 2>&1; then
    tput cup 0 0
    tput ed
  else
    clear
  fi
}

if [[ $WAIT_MODE -eq 1 ]]; then
  start=$(date +%s)
  while true; do
    output=$(run_once)
    now=$(date +%s)
    elapsed=$((now - start))
    if echo "$output" | grep -q "All resources are ready."; then
      echo -e "$output"
      echo -e "${GREEN}Ready after ${elapsed}s.${RESET}"
      exit 0
    fi
    if (( elapsed >= TIMEOUT_SEC )); then
      echo -e "$output"
      echo -e "${RED}${BOLD}Timeout after ${TIMEOUT_SEC}s — resources still not ready.${RESET}" >&2
      exit 1
    fi
    sleep "$INTERVAL"
  done
elif [[ $WATCH -eq 1 ]]; then
  trap 'tput cnorm 2>/dev/null || true; exit 0' INT TERM
  command -v tput >/dev/null 2>&1 && tput civis 2>/dev/null || true
  while true; do
    output=$(run_once 2>&1)
    clear_screen_smooth
    echo -e "${BOLD}Last updated: $(date)  (refreshing every ${INTERVAL}s, Ctrl+C to stop)${RESET}"
    echo
    echo -e "$output"
    sleep "$INTERVAL"
  done
else
  run_once
  [[ $found_issues -eq 0 ]] || exit 1
fi
