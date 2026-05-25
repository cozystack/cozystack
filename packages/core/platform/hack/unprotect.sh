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

# Namespaced guarded kinds. ConfigMap, Repository/OCIRepository, HelmRelease.
# Use --all-namespaces with -o jsonpath so we get back (ns, name) pairs and
# can pass each to a per-object `kubectl label`.
NS_KINDS="
configmap
ocirepository
helmrelease
"

echo "Removing $LABEL label from all guarded objects..."

for kind in $CLUSTER_KINDS; do
  names=$(kubectl get "$kind" --selector="$LABEL=true" --output=name 2>/dev/null || true)
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
  pairs=$(kubectl get "$kind" --all-namespaces --selector="$LABEL=true" \
    --output=jsonpath='{range .items[*]}{.metadata.namespace}{" "}{.metadata.name}{"\n"}{end}' \
    2>/dev/null || true)
  if [ -n "$pairs" ]; then
    printf '%s\n' "$pairs" | while read -r ns name; do
      [ -z "$name" ] && continue
      kubectl label --namespace "$ns" "$kind" "$name" "$LABEL-"
    done
  fi
done

echo "Done. helm uninstall is now unblocked."
