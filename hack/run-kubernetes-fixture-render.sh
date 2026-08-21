#!/bin/sh
# run-kubernetes-fixture-render.sh -- render the tenant Kubernetes fixture that
# hack/e2e-chainsaw/_lib/run-kubernetes.sh applies, without a cluster.
#
# The fixture is an unquoted heredoc inside a function, so the only way to see
# what it produces is to expand it. This extracts that heredoc and expands it in
# a child shell, which is what makes the recorded render in
# hack/testdata/run-kubernetes-fixture/ a statement about the manifest the suite
# would apply rather than about the text of the script.
#
# Expansion is deliberately the same mechanism the suite uses. A renderer that
# substituted variables itself would disagree with production the moment the
# fixture grew an expansion this one does not implement, and would disagree
# silently, in the direction of looking correct.
#
# The four values the fixture takes from its caller are supplied here rather
# than by the caller, so that regenerating the recorded render by hand and
# rendering it from the test produce the same bytes. Two of them stand in for
# blocks composed elsewhere; nothing here is a statement about their content.
#
# Usage:
#   sh hack/run-kubernetes-fixture-render.sh hack/e2e-chainsaw/_lib/run-kubernetes.sh
#
# Exit codes: 2 for a source it cannot read, 3 for a source whose fixture
# heredoc it cannot find or which turns out to be empty. Neither is reported as
# an empty render: a renderer that printed nothing and exited zero would read
# exactly like a fixture that had become empty.
set -eu

src=${1:-}
if [ -z "$src" ]; then
  echo "$0: usage: $0 <path to run-kubernetes.sh>" >&2
  exit 2
fi
if [ ! -r "$src" ]; then
  echo "$0: cannot read $src" >&2
  exit 2
fi

# `exit` from the rule runs END, so the terminator arm is what clears the flag
# END tests: an anchor with no closing EOF and an anchor that is not there at
# all are both this failure, and the message names both rather than guessing.
body=$(awk '
  /^  kubectl apply -f - <<EOF$/ { inside = 1; next }
  inside && /^EOF$/ { closed = 1; exit }
  inside { print }
  END { if (!closed) exit 3 }
' "$src") || {
  echo "$0: no fixture heredoc in $src -- this reads the anchor line '  kubectl apply -f - <<EOF' through to its closing EOF, and one of the two is missing" >&2
  exit 3
}

if [ -z "$body" ]; then
  echo "$0: the fixture heredoc in $src is empty" >&2
  exit 3
fi

test_name=fixture-pin
talos_block='  talosBlockPlaceholder: true'
ouroboros_addon='    ouroborosAddonPlaceholder: true'
k8s_version=v0.0.0-fixture-pin
export test_name talos_block ouroboros_addon k8s_version

render=$(mktemp "${TMPDIR:-/tmp}/fixture-render.XXXXXX")
# Removed on the way out rather than after the run: `set -e` leaves on a failing
# render, and the line after it would not be reached.
trap 'rm -f "$render"' EXIT
printf 'cat <<EOF\n%s\nEOF\n' "$body" >"$render"
sh "$render"
