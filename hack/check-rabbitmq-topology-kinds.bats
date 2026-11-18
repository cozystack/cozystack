#!/usr/bin/env bats
# Unit test: the kinds the rabbitmq chart refuses to close the plaintext
# listeners over are the kinds that actually wedge when it does.
#
# Setting tls.disableNonTLSListeners forces messaging-topology-operator onto a
# management API whose per-release CA it cannot verify. Every kind that operator
# drives through its shared TopologyReconciler carries a
# deletion.finalizers.<plural>.rabbitmq.com finalizer that comes off only after a
# successful call there, so all of them stop deleting at the same instant and sit
# in Terminating with the upgrade reporting success. The chart guards the
# transition by listing what is still in the namespace, which is only as good as
# the list it iterates.
#
# The list lives in one place, templates/_topology.tpl, and is read from there by
# the guard and by the confirmation command the refusal prints. This file holds
# it against the two things it can drift from: the operator's own CRDs, which a
# `make update` in packages/system/rabbitmq-operator can change under it, and the
# runbook in the chart README, which an operator follows by hand.
#
# SuperStream is the one CRD in that group deliberately left out. It runs on its
# own reconciler, which sets no finalizer at all, and the Queue, Exchange and
# Binding objects it owns are already covered.

load test_helper

REPO_ROOT="$(cd "$(dirname "${BATS_TEST_FILENAME:-$0}")/.." && pwd)"
HELPER="$REPO_ROOT/packages/apps/rabbitmq/templates/_topology.tpl"
README="$REPO_ROOT/packages/apps/rabbitmq/README.md"
OPERATOR_CRDS="$REPO_ROOT/packages/system/rabbitmq-operator/templates/messaging-topology-operator.yml"

# The kinds the guard iterates, read off the single line the helper defines them
# on. Parsed rather than rendered because helm cannot render a bare helper.
helper_kinds() {
  sed -n '/define "rabbitmq.topology.kinds"/,/^{{- end -}}$/p' "$HELPER" \
    | sed -e '1d' -e '$d' \
    | tr ' ' '\n' \
    | sed '/^$/d'
}

# Kind and plural as the operator itself declares them, so the pluralisation the
# helper applies is checked against its source rather than against a second copy
# of the same rule.
operator_names() {
  yq eval-all \
    '[select(.kind == "CustomResourceDefinition" and .spec.group == "rabbitmq.com")
      | .spec.names.kind + " " + .spec.names.plural] | .[]' \
    "$OPERATOR_CRDS"
}

@test "the guard iterates every topology kind the operator ships except SuperStream" {
  if [ ! -f "$HELPER" ] || [ ! -f "$OPERATOR_CRDS" ]; then
    echo "Missing $HELPER or $OPERATOR_CRDS -- did either move?" >&2
    exit 1
  fi

  names=$(operator_names)
  expected=$(printf '%s\n' "$names" | cut -d' ' -f1 | grep -v '^SuperStream$' | sort)
  actual=$(helper_kinds | sort)

  # A pipeline reports only its last element, so an unreadable manifest reaches
  # here as an empty string rather than as a non-zero status.
  if [ -z "$expected" ] || [ -z "$actual" ]; then
    echo "Read no kinds: '$expected' from the CRDs, '$actual' from the helper." >&2
    exit 1
  fi

  if [ "$expected" != "$actual" ]; then
    echo "rabbitmq.topology.kinds does not match the operator's CRDs." >&2
    echo "Declared by the operator, minus SuperStream:" >&2
    printf '%s\n' "$expected" >&2
    echo "Iterated by the guard:" >&2
    printf '%s\n' "$actual" >&2
    echo "A kind missing here renders cleanly and then wedges in Terminating," >&2
    echo "because its finalizer needs the management API the flag just closed." >&2
    exit 1
  fi
}

# The refusal prints this command and the README repeats it as step 2 of the
# runbook. An operator who runs a short version of it sees an empty namespace
# that is not empty.
@test "the README runbook names the operator plural of every kind the guard iterates" {
  if [ ! -f "$README" ]; then
    echo "Chart README not found at $README -- did it move?" >&2
    exit 1
  fi

  # Built in the helper's own order, and with the operator's own plurals, so
  # this is the exact string the refusal prints rather than a second spelling
  # of it that happens to sort the same way.
  names=$(operator_names)
  expected=""
  for kind in $(helper_kinds); do
    plural=$(printf '%s\n' "$names" | awk -v k="$kind" '$1 == k { print $2 }')
    if [ -z "$plural" ]; then
      echo "No CRD in the operator manifest declares kind $kind." >&2
      exit 1
    fi
    expected="${expected:+$expected,}$plural.rabbitmq.com"
  done

  actual=$(grep -o 'kubectl get [a-z.,]*\.rabbitmq\.com' "$README" | head -1 | sed 's/^kubectl get //')

  if [ "$expected" != "$actual" ]; then
    echo "The README runbook step does not check every wedging kind." >&2
    echo "Expected: kubectl get $expected" >&2
    echo "Found:    kubectl get $actual" >&2
    exit 1
  fi
}
