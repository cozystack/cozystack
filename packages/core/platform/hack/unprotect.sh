#!/bin/sh
# Strip the platform.cozystack.io/no-delete=true label from every object that
# carries it, so `helm uninstall cozy-platform` (and ad-hoc `kubectl delete`
# on a protected resource) is no longer denied by the deletion-protection
# ValidatingAdmissionPolicy.
#
# Intended for chart teardown and disaster-recovery — NOT a routine operation.
# After this runs, the cluster has no guardrail until the next `helm upgrade`
# re-stamps the label.
#
# Idempotent: a label that is already absent is a no-op.
#
# The set of guarded kinds is enumerated locally rather than discovered
# dynamically because some of the kinds may not exist in every cluster
# (e.g. linstorcluster on a non-LINSTOR install) and kubectl returns an
# error for unknown resource types. The per-kind get is best-effort.

set -eu

LABEL="platform.cozystack.io/no-delete"

# Run `kubectl get` and tolerate only the "unknown resource type" error
# (a kind that doesn't exist in this cluster — e.g. linstorcluster on a
# non-LINSTOR install). Any other error (RBAC, wrong context, API
# unreachable) is fatal — otherwise we'd silently report success while
# leaving labels behind.
#
# Keep stdout (the resource list) and stderr (kubectl warnings, errors)
# separate so unrelated warnings can't sneak into the loop variable.
kubectl_get_or_skip_unknown_kind() {
  err=$(mktemp)
  if out=$(kubectl get "$@" 2>"$err"); then
    rm -f "$err"
    printf '%s\n' "$out"
    return 0
  fi
  errmsg=$(cat "$err")
  rm -f "$err"
  case "$errmsg" in
    *"the server doesn't have a resource type"*|\
    *"the server could not find the requested resource"*)
      return 0
      ;;
  esac
  printf '%s\n' "$errmsg" >&2
  return 1
}

# Cluster-scoped guarded kinds. CRDs and Namespaces sit here, plus the
# VAP/Binding pair (which is also self-labeled) and LinstorCluster.
CLUSTER_KINDS="
customresourcedefinition
clusterissuer
namespace
validatingadmissionpolicy
validatingadmissionpolicybinding
linstorcluster
"

# Namespaced guarded kinds. ConfigMap, Flux source kinds (OCI/Git), HelmRelease.
# Both OCIRepository and GitRepository are valid platform sourceRef.kind values
# — repository.yaml templates `kind: {{ $sourceRef.kind }}` unrestricted — so
# either may carry the no-delete label and must be enumerated here.
# Use --all-namespaces with -o jsonpath so we get back (ns, name) pairs and
# can pass each to a per-object `kubectl label`.
NS_KINDS="
configmap
ocirepository
gitrepository
helmrelease
"

echo "Removing $LABEL label from all guarded objects..."

for kind in $CLUSTER_KINDS; do
  names=$(kubectl_get_or_skip_unknown_kind "$kind" --selector="$LABEL=true" --output=name)
  if [ -n "$names" ]; then
    # One kubectl per resource — `kubectl label` takes the label op as the
    # last argument, so xargs's trailing-args model would put it in the
    # wrong position. The protected set is small (<20 objects) so the
    # extra round-trips are negligible.
    printf '%s\n' "$names" | while read -r ref; do
      [ -z "$ref" ] && continue
      kubectl label "$ref" "$LABEL-"
    done
  fi
done

for kind in $NS_KINDS; do
  pairs=$(kubectl_get_or_skip_unknown_kind "$kind" --all-namespaces --selector="$LABEL=true" \
    --output=jsonpath='{range .items[*]}{.metadata.namespace}{" "}{.metadata.name}{"\n"}{end}')
  if [ -n "$pairs" ]; then
    printf '%s\n' "$pairs" | while read -r ns name; do
      [ -z "$name" ] && continue
      kubectl label --namespace "$ns" "$kind" "$name" "$LABEL-"
    done
  fi
done

echo "Done. helm uninstall is now unblocked."
