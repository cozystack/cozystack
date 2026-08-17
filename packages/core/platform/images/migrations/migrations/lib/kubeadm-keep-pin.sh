# shellcheck shell=sh
# Shared helper for pinning the CAPI kubeadm bootstrap objects with
# helm.sh/resource-policy=keep, so the 1.6 Talos worker migration cannot let Helm
# prune them while CAPI still references them.
#
# 1.6 drops KubeadmConfigTemplate from the tenant `kubernetes` app chart
# entirely — workers move to TalosConfigTemplate. On that upgrade Helm sees the
# resource present in the previous release manifest and absent from the new one,
# and issues a delete. The kubeadm-backed MachineSet is still around until CAPI
# finishes rolling workers onto Talos, so its bootstrap.configRef then points at
# a missing object: controller-manager floods with reconcile errors, and where the
# Talos image fetch from factory.talos.dev is slow or a MachineHealthCheck
# remediates, workers can hang pending with nothing to bootstrap from. Tenant
# Kubernetes only, and a noisy broken upgrade rather than data loss — but it is
# not self-healing.
#
# WHY THIS LIVES ON release-1.5 AT ALL. 1.6 ships the same pin as its own
# migration 45, and every released 1.5 (v1.5.0 through v1.5.3, all stamped
# targetVersion 45) reaches it on the way to 1.6. What breaks that is v1.5.4: the
# #3370 backport put the SeaweedFS db-split repair in release-1.5's slot 45 and
# bumped targetVersion to 46, so a v1.5.4 cluster is stamped 46 and its later 1.6
# upgrade runs `seq 46 53` — skipping 1.6's slot 45, and with it the pin. Folding
# the pin into the slot that IS run leaves the annotation already in place by the
# time 1.6 skips it. Renumbering release-1.5's repair to 1.6's slot 53 instead is
# not an option: run-migrations.sh hard-fails on a missing slot file, so it would
# need no-op stubs for 46-52, and a cluster stamped 54 would then run `seq 54 53`
# and skip every real 1.6 migration.
#
# WHY THE managed-by SELECTOR, AND WHY A LABEL. Helm injects the
# app.kubernetes.io/managed-by=Helm LABEL into every resource it applies.
# meta.helm.sh/release-name is an ANNOTATION and cannot be used as a --selector:
# doing so silently matches nothing and the pin is applied to nothing at all,
# which looks exactly like a clean run. Verified on a live v1.5 stand stamped 45:
# the chart's KubeadmConfigTemplate carries managed-by=Helm and no
# resource-policy, while the KubeadmConfig children CAPI spawns from the template
# carry no managed-by at all — they are owned by their Machine, Helm never prunes
# them, and this selector correctly leaves them alone.
#
# FAILS CLOSED, AND AGGREGATES. Every object that can be pinned is pinned before a
# non-zero return aborts the migration ahead of its version stamp, so the Job
# retries the whole slot and reports all failures at once instead of one per
# attempt. Best-effort (returning 0 on partial failure) is not an option:
# migrations never re-run, and any KubeadmConfigTemplate left un-pinned is one
# Helm prune away from breaking a live MachineSet's bootstrap.configRef
# mid-rollover, which is recovered only by recreating the template by hand with
# the right Helm ownership metadata.
#
# Idempotent: an object already carrying keep is skipped WITHOUT a write, so
# re-running is a no-op on a cluster that has already been pinned.
#
# COST OF A KEEP THAT WAS NOT NEEDED, since `keep` is stamped on every
# Helm-managed template rather than only on those a 1.6 upgrade would prune:
# within 1.5 the chart still renders these objects and `keep` only suppresses
# DELETION, so updates are unaffected. What it does leave behind is an orphan
# where a nodeGroup is removed or the whole `kubernetes` app is deleted while its
# namespace survives. That orphan keeps its Helm ownership annotations, so a
# same-named app is adopted rather than blocked on re-install, and the asymmetry
# is the same one the SeaweedFS hand-over accepts: an orphan is cleaned up by
# hand, a pruned bootstrap reference breaks a live worker rollover.
#
# Sourced, not executed:
#   . "$(dirname "$0")/lib/kubeadm-keep-pin.sh"

_KKP_KEEP_ANNOTATION="helm.sh/resource-policy=keep"
_KKP_HELM_MANAGED_SELECTOR="app.kubernetes.io/managed-by=Helm"

# Fully qualified so a same-named CRD in another API group can never be matched
# instead. KubeadmConfig is included for parity with 1.6's slot 45: the 1.5 chart
# renders only the template, and the managed-by selector leaves the Machine-owned
# children alone, so this is a no-op on a stock cluster and covers a Helm-managed
# KubeadmConfig if one is ever rendered.
_KKP_KINDS="kubeadmconfigtemplates.bootstrap.cluster.x-k8s.io
kubeadmconfigs.bootstrap.cluster.x-k8s.io"

# _kkp_is_absent_err <file>
# True when a kubectl error means the resource type simply is not served, as
# opposed to the API being unreachable, forbidden, throttled, or not yet
# established: a cluster with no CAPI bootstrap provider has nothing to pin.
# Deliberately narrow — anything unrecognised is fatal — and deliberately NOT
# shared with lib/seaweedfs-db-adopt.sh, because migration 43 sources that lib
# alone and neither may depend on the other having been loaded.
_kkp_is_absent_err() {
  grep -qiE "server doesn't have a resource type|server could not find the requested resource|could not find the requested resource|no matches for kind" "$1"
}

# _kkp_is_gone_err <file>
# True when a PER-OBJECT kubectl error means that one object is no longer there:
# an app being deleted concurrently with this hook can take its
# KubeadmConfigTemplate away between the fleet scan and the annotate. Nothing is at
# risk in that case — Helm cannot prune an object that does not exist — so it is a
# skip, not a failure, and must not fail a pre-upgrade hook and block the platform
# upgrade over an object nobody needs any more.
#
# Separate from _kkp_is_absent_err, and wider than it by exactly one phrase,
# because "not found" must NOT be accepted for the fleet SCAN: a list never
# answers NotFound, so accepting it there would let some genuine failure read as
# an empty fleet and leave every template un-pinned.
_kkp_is_gone_err() {
  _kkp_is_absent_err "$1" || grep -qiE "not found" "$1"
}

# _kkp_pin_one <kind> <namespace> <name>
# Stamp keep on one object, counting the outcome. Always returns 0: a failure is
# recorded in _kkp_failures and reported by the caller once the whole fleet has
# been walked, so one bad object cannot abort the loop before the rest are pinned.
_kkp_pin_one() {
  _kkp_p_kind="$1"
  _kkp_p_ns="$2"
  _kkp_p_name="$3"

  # An unreadable current value is deliberately NOT fatal here: the annotate below
  # is the operation that has to succeed and it IS checked, so a failed read costs
  # only a redundant write. There is no fail-open hiding in this `|| true`.
  _kkp_p_current=$(kubectl get "$_kkp_p_kind" --namespace "$_kkp_p_ns" "$_kkp_p_name" \
    --output 'jsonpath={.metadata.annotations.helm\.sh/resource-policy}' 2>/dev/null) \
    || _kkp_p_current=""

  if [ "$_kkp_p_current" = "keep" ]; then
    echo "$_kkp_p_kind $_kkp_p_ns/$_kkp_p_name already carries $_KKP_KEEP_ANNOTATION — nothing to do"
    _kkp_skipped=$((_kkp_skipped + 1))
    return 0
  fi

  if kubectl annotate "$_kkp_p_kind" --namespace "$_kkp_p_ns" "$_kkp_p_name" \
       "$_KKP_KEEP_ANNOTATION" --overwrite >/dev/null 2>"$_kkp_err"; then
    echo "Pinned $_kkp_p_kind $_kkp_p_ns/$_kkp_p_name with $_KKP_KEEP_ANNOTATION"
    _kkp_patched=$((_kkp_patched + 1))
  elif _kkp_is_gone_err "$_kkp_err"; then
    echo "$_kkp_p_kind $_kkp_p_ns/$_kkp_p_name disappeared between the scan and the pin — skipping"
    _kkp_skipped=$((_kkp_skipped + 1))
  else
    echo "WARNING: failed to pin $_kkp_p_kind $_kkp_p_ns/$_kkp_p_name:" >&2
    cat "$_kkp_err" >&2
    _kkp_failures=$((_kkp_failures + 1))
  fi
  return 0
}

# pin_kubeadm_bootstrap_objects
# Stamp helm.sh/resource-policy=keep on every Helm-managed kubeadm bootstrap
# object. Returns non-zero (aborting the migration before its version stamp) on
# any error that is not "this resource type is not served".
pin_kubeadm_bootstrap_objects() {
  _kkp_err=$(mktemp)
  _kkp_items=$(mktemp)
  _kkp_patched=0
  _kkp_skipped=0
  _kkp_failures=0

  for _kkp_kind in $_KKP_KINDS; do
    echo "==> Pinning Helm-managed $_kkp_kind with $_KKP_KEEP_ANNOTATION"

    # Redirecting to a file inside `if !` keeps errexit from firing so the exit
    # status can be inspected. `for x in $(kubectl ...)` would not trip errexit
    # either: on failure it iterates zero times and lets the caller stamp the
    # version regardless, which is the fail-open this guard exists for.
    if ! kubectl get "$_kkp_kind" --all-namespaces \
          --selector "$_KKP_HELM_MANAGED_SELECTOR" \
          --output 'jsonpath={range .items[*]}{.metadata.namespace}{"/"}{.metadata.name}{"\n"}{end}' \
          >"$_kkp_items" 2>"$_kkp_err"; then
      if _kkp_is_absent_err "$_kkp_err"; then
        echo "$_kkp_kind is not served on this cluster — nothing to pin"
        continue
      fi
      echo "FATAL: cannot list $_kkp_kind across namespaces; refusing to stamp past an unverified fleet:" >&2
      cat "$_kkp_err" >&2
      rm -f "$_kkp_err" "$_kkp_items"
      return 1
    fi

    # Read from a file, not a pipe: `kubectl ... | while read` runs the loop body
    # in a subshell, so the counters would be discarded on exit and a failed pin
    # would be reported as a clean run. A namespace and an object name are both
    # DNS labels and cannot contain "/", so the separator is unambiguous.
    while IFS= read -r _kkp_item; do
      [ -n "$_kkp_item" ] || continue
      _kkp_ns="${_kkp_item%%/*}"
      _kkp_name="${_kkp_item#*/}"
      if [ -z "$_kkp_ns" ] || [ -z "$_kkp_name" ] || [ "$_kkp_ns" = "$_kkp_item" ]; then
        echo "WARNING: cannot parse '$_kkp_item' as <namespace>/<name> for $_kkp_kind — refusing to guess" >&2
        _kkp_failures=$((_kkp_failures + 1))
        continue
      fi
      _kkp_pin_one "$_kkp_kind" "$_kkp_ns" "$_kkp_name"
    done <"$_kkp_items"
  done

  rm -f "$_kkp_err" "$_kkp_items"

  echo "==> kubeadm keep-pin summary: pinned=$_kkp_patched already-pinned=$_kkp_skipped failures=$_kkp_failures"

  if [ "$_kkp_failures" -gt 0 ]; then
    echo "ERROR: $_kkp_failures kubeadm bootstrap object(s) could not be pinned; refusing to stamp the version so this migration retries on the next platform upgrade." >&2
    echo "ERROR: an un-pinned KubeadmConfigTemplate can be pruned by the 1.6 kubernetes chart upgrade, breaking the bootstrap.configRef of the kubeadm-backed MachineSet that is still rolling workers over to Talos." >&2
    return 1
  fi

  return 0
}
