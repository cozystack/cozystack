#!/bin/sh
# Points ControllerConfig/default at the passphrase Secret, the one field of
# that object this chart owns; blockstor and linstor-migrate write the rest.
#
#   PATCH     JSON merge patch that sets spec.passphraseSecretRef
#   MANIFEST  the whole object, for a cluster that has none yet
#
# A merge patch rather than `kubectl apply --server-side`, because the apply
# can delete fields it does not name. After a server-side apply kubectl looks
# for a manager with an Update entry that owns the last-applied-configuration
# annotation, merges every field that manager owns into the applying manager
# and sends the apply again (migrateToSSAIfNecessary in
# k8s.io/kubectl/pkg/cmd/apply/apply.go, csaupgrade.UpgradeManagedFieldsPatch
# in k8s.io/client-go/util/csaupgrade, v0.33). A ControllerConfig last
# written by a client-side `kubectl apply`, such as linstor-migrate's output
# applied without --server-side, then hands its extraProps and drbdOptions to
# this manager, and the second apply, which names only passphraseSecretRef,
# removes them: LINSTOR's cluster-wide DRBD options are gone and the
# satellites adjust every resource to the defaults. No field manager avoids
# that, since the upgrade moves the fields to whichever manager applies. A
# merge patch replaces the fields it carries and touches nothing else, whoever
# owns the rest.
#
# A create that loses a race to blockstor creating the object fails; the Job
# retries and the next attempt patches.
set -eu

patch=${PATCH:?PATCH holds the merge patch for spec.passphraseSecretRef}
manifest=${MANIFEST:?MANIFEST holds the ControllerConfig to create when there is none}
resource=controllerconfigs.blockstor.org
manager=cozystack-linstor

current=$(kubectl get "$resource" default --ignore-not-found -o name)
if [ -n "$current" ]; then
  kubectl patch "$resource" default --type=merge --field-manager="$manager" --patch "$patch"
else
  printf '%s\n' "$manifest" | kubectl create --field-manager="$manager" -f -
fi
