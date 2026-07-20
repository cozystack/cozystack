# shellcheck shell=sh
# Shared helper for handing Cluster/seaweedfs-db over to the <name>-db release.
#
# The 1.5.0 db split (PR #2601) moved the CNPG Cluster carrying SeaweedFS filer
# metadata out of the <name>-system Helm release into a new <name>-db release.
# The Cluster object itself did not move — only its ownership had to. Two things
# must be true BEFORE <name>-system next renders, and they are independent:
#
#   meta.helm.sh/release-name=<name>-db   so <name>-db ADOPTS the existing
#                                         Cluster instead of failing its install
#                                         on Helm's ownership check;
#   helm.sh/resource-policy=keep          so the <name>-system upgrade, whose new
#                                         chart no longer renders the Cluster,
#                                         does not GARBAGE-COLLECT it as a
#                                         removed resource.
#
# Without `keep` the Cluster is deleted, CNPG takes its PVC with it, and the
# tenant's filer metadata — hence all of its S3 — is gone. Data loss, not outage.
#
# The window is not a one-shot. Helm prunes by diffing the LAST DEPLOYED revision
# against the new manifest, so as long as <name>-system's last successful revision
# is a pre-split one that still contains the Cluster, EVERY subsequent upgrade
# attempt — including ones that fail for unrelated reasons and never become the
# new "deployed" revision — re-computes the same deletion. A tenant whose
# <name>-system is wedged therefore re-deletes the Cluster on every retry, racing
# <name>-db, which keeps recreating it. `keep` is what breaks that loop, and it
# must stay for as long as that pre-split revision remains the prune baseline
# (i.e. until <name>-system upgrades successfully at least once). Clearing it
# again is deferred to the 1.7 batch migrations.
#
# Ownership is therefore NOT a proxy for safety. A Cluster can already be owned by
# <name>-db and still need `keep`: where the hand-over was skipped, <name>-system
# prunes the Cluster and <name>-db simply RECREATES it under its own ownership
# with no keep — while <name>-system's prune baseline still lists it, so the next
# reconcile deletes it again. Both shapes are live on the upgrade stand:
# tenant-l and tenant-root are <name>-db-owned WITH keep and their <name>-system
# deployed revision (rev 1) still contains the Cluster, so only keep saves them;
# tenant-fresh is <name>-db-owned WITHOUT keep because it was installed after the
# split and its <name>-system never rendered a Cluster. Telling those two apart
# needs the release's deployed manifest, which this script cannot read cheaply or
# reliably. The costs are asymmetric: stamping keep where it was not needed leaves
# an orphan on app delete (which the extra/seaweedfs cleanup hook reclaims);
# missing one loses the database. So keep is stamped on every Cluster owned by
# either side of the split.
#
# Migration 43 shipped this logic with the owning release name hardcoded to
# "seaweedfs-system", which is only correct for an instance named `seaweedfs`.
# `SeaweedFS` is a user-creatable kind, so an instance named e.g. `foo` is owned
# by `foo-system` and was silently skipped: no re-own, no keep, Cluster pruned.
# Matching on the `-system` SUFFIX instead covers every instance name, so this
# helper is sourced by both migration 43 (the original hand-over, for clusters
# that have not run it yet) and migration 53 (repair, for clusters that already
# ran the hardcoded version).
#
# Idempotent: a Cluster already owned by <name>-db AND carrying keep is left
# alone, so re-running is a no-op and both migrations can safely fire on the same
# cluster.
#
# FAILS CLOSED. Migrations never re-run, so a transient error swallowed here would
# permanently leave at-risk tenants exposed with no later migration to catch them.
# Every kubectl failure is fatal EXCEPT the two that genuinely mean "nothing to
# do": the CNPG resource type not being served at all (a cluster without CNPG),
# and a Cluster disappearing between the scan and the read. A non-zero return
# aborts the migration before it stamps the version, so the Job retries.
#
# Sourced, not executed:
#   . "$(dirname "$0")/lib/seaweedfs-db-adopt.sh"

# _sdb_is_absent_err <file>
# True when a kubectl error means the thing simply is not there, as opposed to the
# API being unreachable, forbidden, throttled, or not yet established. Kept
# deliberately narrow: anything unrecognised is treated as fatal.
_sdb_is_absent_err() {
  grep -qiE "server doesn't have a resource type|server could not find the requested resource|could not find the requested resource|not found" "$1"
}

# adopt_seaweedfs_db_clusters
# Re-own and/or protect every Cluster/seaweedfs-db the split left exposed.
# Returns non-zero (aborting the migration) on any error that is not "absent".
adopt_seaweedfs_db_clusters() {
  _sdb_err=$(mktemp)

  # Fleet scan. Assigning inside `if !` keeps errexit from firing so the exit
  # status can be inspected — `for ns in $(kubectl ...)` would silently iterate
  # zero times on failure and let the caller stamp the version regardless, which
  # is the whole bug this guard exists for.
  if ! _sdb_namespaces=$(kubectl get cluster.postgresql.cnpg.io -A \
        -o jsonpath='{range .items[?(@.metadata.name=="seaweedfs-db")]}{.metadata.namespace}{"\n"}{end}' \
        2>"$_sdb_err"); then
    if _sdb_is_absent_err "$_sdb_err"; then
      echo "CNPG Cluster resource type is not served on this cluster — no SeaweedFS databases to hand over"
      rm -f "$_sdb_err"
      return 0
    fi
    echo "FATAL: cannot list Cluster/seaweedfs-db across namespaces; refusing to stamp past an unverified fleet:" >&2
    cat "$_sdb_err" >&2
    rm -f "$_sdb_err"
    return 1
  fi

  for ns in $_sdb_namespaces; do
    [ -n "$ns" ] || continue

    if ! _sdb_current=$(kubectl get cluster.postgresql.cnpg.io seaweedfs-db -n "$ns" \
          -o jsonpath='{.metadata.annotations.meta\.helm\.sh/release-name}' 2>"$_sdb_err"); then
      if _sdb_is_absent_err "$_sdb_err"; then
        echo "Cluster/seaweedfs-db in $ns disappeared between scan and read — skipping"
        continue
      fi
      echo "FATAL: cannot read the Helm owner of Cluster/seaweedfs-db in $ns:" >&2
      cat "$_sdb_err" >&2
      rm -f "$_sdb_err"
      return 1
    fi

    if ! _sdb_keep=$(kubectl get cluster.postgresql.cnpg.io seaweedfs-db -n "$ns" \
          -o jsonpath='{.metadata.annotations.helm\.sh/resource-policy}' 2>"$_sdb_err"); then
      if _sdb_is_absent_err "$_sdb_err"; then
        echo "Cluster/seaweedfs-db in $ns disappeared between scan and read — skipping"
        continue
      fi
      echo "FATAL: cannot read the resource-policy of Cluster/seaweedfs-db in $ns:" >&2
      cat "$_sdb_err" >&2
      rm -f "$_sdb_err"
      return 1
    fi

    case "$_sdb_current" in
      # No Helm ownership annotation at all — distinct from "unreadable", which is
      # fatal above. Guessing an owner would be worse than doing nothing, but a
      # SeaweedFS database that nobody owns is worth saying out loud.
      "")
        echo "WARNING: Cluster/seaweedfs-db in $ns carries no meta.helm.sh/release-name — not Helm-managed, leaving it alone. If this tenant runs SeaweedFS, verify by hand that its database is not about to be pruned." >&2
        ;;

      # Owned by the data-plane release: this is the hand-over. The instance name
      # is whatever precedes -system, so `foo-system` -> `foo-db` exactly as
      # `seaweedfs-system` -> `seaweedfs-db`.
      *-system)
        _sdb_instance="${_sdb_current%-system}"
        # A release literally named "-system" has no instance name; refusing is
        # safer than annotating an owner of "-db" that no release will claim.
        if [ -z "$_sdb_instance" ]; then
          echo "WARNING: Cluster/seaweedfs-db in $ns is owned by a release named '$_sdb_current' with no instance name — skipping" >&2
          continue
        fi
        echo "Re-annotating Cluster/seaweedfs-db in $ns: $_sdb_current -> ${_sdb_instance}-db (+keep)"
        kubectl annotate cluster.postgresql.cnpg.io seaweedfs-db -n "$ns" \
          meta.helm.sh/release-name="${_sdb_instance}-db" \
          helm.sh/resource-policy=keep \
          --overwrite
        # release-namespace stays the same (the tenant namespace); both releases
        # live there, so the ownership check only needed release-name rewritten.
        ;;

      # Already owned by the db release. Ownership is correct, but that does NOT
      # imply it is protected — see the note above. Stamp keep if it is missing.
      *-db)
        if [ "$_sdb_keep" = "keep" ]; then
          echo "Cluster/seaweedfs-db in $ns already handed over to $_sdb_current and protected — nothing to do"
        else
          echo "Protecting Cluster/seaweedfs-db in $ns: owned by $_sdb_current but missing helm.sh/resource-policy=keep"
          kubectl annotate cluster.postgresql.cnpg.io seaweedfs-db -n "$ns" \
            helm.sh/resource-policy=keep \
            --overwrite
        fi
        ;;

      # Owned by some unrelated release. Not ours to touch.
      *)
        echo "WARNING: Cluster/seaweedfs-db in $ns is owned by unrelated release '$_sdb_current' — leaving it alone" >&2
        ;;
    esac
  done

  rm -f "$_sdb_err"
  return 0
}
