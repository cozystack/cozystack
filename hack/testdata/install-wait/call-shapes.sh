# Fixture for hack/run-kubernetes-install-wait_test.bats.
# Not executed: read as text by the guard's matchers. Each block pairs a shape
# with the line the test expects a matcher to return for it.
##
# `git diff --check` flags the trailing space on that line, deliberately:
# the space is the subject here, not an accident, and a case reds if it is
# stripped. Nothing suppresses the warning -- a suppression would cover the
# next file written here too, where a stray space would be an accident.
# Decoys come first because every matcher here stops at its first hit. A decoy
# placed after the real thing is never reached and pins nothing.

# Same group, different kind.
kubectl apply -f - <<YAML
apiVersion: apps.cozystack.io/v1alpha1
# An apiVersion commented out directly above a column-0 kind. The apiVersion
# half is anchored at column 0 too, and this is what that anchor refuses: a
# commented line is not an applied field.
# apiVersion: apps.cozystack.io/v1alpha1
kind: Kubernetes

# The pair with a field between them. The CR matcher wants the apiVersion on
# the line IMMEDIATELY above, not merely nearby: a window would pair a kind
# with an apiVersion belonging to another object.
apiVersion: apps.cozystack.io/v1alpha1
metadata: {}
kind: Kubernetes

# A pair whose apiVersion sits at column 0 while its kind is indented. The CR
# matcher anchors both, because the heredoc it pins writes both at column 0 --
# without that anchor on the kind, this counts as a second CR and the rule that
# pairs one CR with one wait would be reasoning about two.
apiVersion: apps.cozystack.io/v1alpha1
  kind: Kubernetes

# The same pair indented, as an inline apply.resource block writes it. Valid
# YAML naming this kind, and deliberately NOT what the CR matcher takes: it
# pins the heredoc shape this script writes, at column 0. The sweep does take
# this shape -- the two answer different questions and the fixture holds both.
  apiVersion: apps.cozystack.io/v1alpha1
  kind: Kubernetes

kind: KubernetesNodes # @file-scope
metadata:
  name: "fixture-md0"
YAML

# Right kind, different group.
kubectl apply -f - <<YAML
apiVersion: v1
kind: Kubernetes
metadata:
  name: "fixture-wrong-group"
YAML

# Right pair, commented out.
# apiVersion: apps.cozystack.io/v1alpha1
# kind: Kubernetes

# A call in a helper defined BEFORE the body that holds the wait. Source
# position puts it ahead of everything; execution puts it wherever the helper
# is invoked. Its line is below the body's first line, not above its last.
early_helper_with_assertion() {
  cozy_assert_before_the_body "${test_name}" # @call
}

# A fully qualifying wait BEFORE the CR is applied. Nothing is wrong with the
# command; it waits on a release that does not exist yet, so only its position
# disqualifies it.
  kubectl wait hr -n tenant-test "kubernetes-${test_name}" --timeout=9m --for=condition=ready

# The subject shape: everything the ordering rule compares lives in one
# function body, which is what makes source position stand in for execution
# order at all.
run_fixture_test() { # @body-open
# An indented definition: not a body this guard delimits, because the shapes
# it walks open and close at column 0.
  inner_helper() {
    :
  }

kubectl apply -f - <<YAML
apiVersion: apps.cozystack.io/v1alpha1  # @cr-above
kind: Kubernetes 
metadata:
  name: "fixture"
YAML

# A disabled wait: every string the matcher keys on, behind a `#`.
  # kubectl wait hr -n tenant-test "kubernetes-${test_name}" --timeout=9m --for=condition=ready

# Not an invocation: the command name appears as an argument to something else,
# so nothing waits. A substring match would take this line.
  echo kubectl wait hr -n tenant-test "kubernetes-${test_name}" --timeout=9m --for=condition=ready

# Waits with no teeth: backgrounded, and with the failure discarded.
# docs/agents/e2e-testing.md, under "The install gate must have teeth", records
# a gate of exactly this shape shipping a failing
# release through green CI, so neither may satisfy the rule.
  kubectl wait hr -n tenant-test "kubernetes-${test_name}" --timeout=9m --for=condition=ready &
  kubectl wait hr -n tenant-test "kubernetes-${test_name}" --timeout=9m --for=condition=ready || true

# The right name in the wrong namespace: a HelmRelease is identified by both,
# and this one leaves the tenant's install unbounded.
  kubectl wait hr -n other-ns "kubernetes-${test_name}" --timeout=9m --for=condition=ready

# Waits that run but are not this one: a child release whose name extends the
# parent's, another release, another condition, and a wait for the negation of
# the one that matters. The child is why the match keeps the closing quote.
  kubectl wait hr -n tenant-test "kubernetes-${test_name}-csi" --timeout=9m --for=condition=ready
  kubectl wait hr -n tenant-test "postgres-${test_name}" --timeout=9m --for=condition=ready
  kubectl wait hr -n tenant-test "kubernetes-${test_name}" --timeout=9m --for=condition=reconciling
  kubectl wait hr -n tenant-test "kubernetes-${test_name}" --timeout=9m --for=condition=ready=false

# The command waits for the negation; a comment names the condition the
# matcher wants. Only cutting the comment first rejects this.
  kubectl wait hr -n tenant-test "kubernetes-${test_name}" --timeout=9m --for=condition=ready=false # --for=condition=ready
# The same rescue with no space after the `#`. The cut keys on the whitespace
# BEFORE the marker, not after it, so this is cut too -- and a cut that also
# demanded a space after `#` would read the comment as an argument.
  kubectl wait hr -n tenant-test "kubernetes-${test_name}" --timeout=9m --for=condition=reconciling #--for=condition=ready

# A wait whose failure is discarded on the NEXT physical line, joined to it by
# a backslash. Read line by line it satisfies every filter and would be taken
# as the wait; read as one command it carries `|| true` and is refused. This is
# what the fold is for, and it stands ahead of the real wait so that losing the
# fold changes which line is answered.
  kubectl wait hr -n tenant-test "kubernetes-${test_name}" --timeout=9m --for=condition=ready \
    || true

# A fully qualifying wait written inside a heredoc body. Nothing runs it -- it
# is data on its way to `kubectl apply` -- so a matcher that reads heredoc
# bodies as commands answers with this line and reports a script with no live
# wait at all as ok.
cat > /dev/null <<'HEREDOC_WAIT'
HEREDOC_WAITING: a data line that starts with the delimiter. Matched as a prefix
it would end the heredoc here and hand the lines below back as commands.
  kubectl wait hr -n tenant-test "kubernetes-${test_name}" --timeout=9m --for=condition=ready
HEREDOC_WAIT

# A here-string and a comment naming a heredoc, both immediately before the
# real wait. Neither opens a heredoc: `<<<` is a here-string, and a `<<WORD`
# inside a comment is text. Taken as openers they would swallow everything
# below, including the wait, and the answer would be "no wait" on a script that
# has one.
  grep -q ready <<<PROBE_STRING
# see the `<<HEREDOC_IN_COMMENT` form described above

# The same shape with an UNQUOTED hash that starts no comment because no space
# precedes it. `foo#bar` is one word to the shell; a cut that fires on
# any hash removes the `|| true` here too.
  kubectl wait hr -n tenant-test "kubernetes-${test_name}" --timeout=9m --for=condition=ready x#y || true

# A wait whose failure is discarded, with a quoted `#` earlier on the line. A
# cut that keys on any ` #` takes the `|| true` away with the comment that is
# not there, and the line reads as a wait with teeth.
  kubectl wait hr -n tenant-test "kubernetes-${test_name}" --timeout=9m --for=condition=ready "note #1" || true

# A qualifying wait whose line carries a second command. The shell reports the
# list's status from the last command, so this wait's failure is not what ends
# the run. The `;` sits away from the condition on purpose: next to it, the
# condition match refuses the line first and the sequence rule never runs.
  kubectl wait hr -n tenant-test "kubernetes-${test_name}" --for=condition=ready --timeout=9m; echo continued

# A negated wait whose `then` sits on the next line, so the wait's own line
# carries no `;` for the sequence rule to refuse. Only the command pattern
# rejects this one, which is what makes the two rules separable here.
  if ! kubectl wait hr -n tenant-test "kubernetes-${test_name}" --timeout=9m --for=condition=ready
  then
    :
  fi
# @live-wait-above
  kubectl wait hr -n tenant-test "kubernetes-${test_name}" --timeout \
    5m --for=condition=ready

# A second qualifying wait. The first one is what bounds the install; a later
# one bounds nothing that has not already been bounded.
  kubectl wait hr -n tenant-test "kubernetes-${test_name}" --timeout=5m --for=condition=ready

cozy_assert_at_column_zero "${test_name}" # @call
	cozy_assert_tab_indented "${test_name}" # @call
      cozy_assert_six_spaces "${test_name}" # @call
  cozy_assert_indented_opener() {
    cozy_assert_inner_of_indented "${test_name}" # @call
  }
  cozy_assert_bare "${test_name}" # @call
  if cozy_assert_in_if "${test_name}"; then :; fi # @call
  if false; then
    :
  elif cozy_assert_in_elif "${test_name}"; then # @call
    :
  fi
  ! cozy_assert_negated "${test_name}" # @call
  !cozy_assert_negated_no_space "${test_name}"   # not a negation: no space after !
  while cozy_assert_in_while "${test_name}"; do :; done # @call
  until cozy_assert_in_until "${test_name}"; do :; done # @call
  cozy_assert_piped || rc=$? # @call
  cozy_switch_and_assert_prefixed "${test_name}" # @call
  cozy_assert_noargs # @call
  cozy_assert_semicolon_terminated; # @call
  cozy_assert_nospace_piped|cat # @call
  cozy_assert_md0_ready "${test_name}" # @call
  # cozy_assert_commented_out "${test_name}"
  # A definition nested inside this body, with a space before the parens. The
  # column-0 rule that exempts an assertion helper cannot see it, and the
  # space makes it look like a call to the shape matcher.
  cozy_assert_indented_definition () {
    :
  }
  cozy_assert_not_a_call=1
} # @body-close

cozy_assert_definition_at_col0() {
  :
}

# An assertion helper that calls another. The inner call runs wherever this
# helper is called from, so it is composition and not a call site of its own.
cozy_assert_composed() {
  cozy_assert_inner "${test_name}"
}

# The same shape under the other assertion prefix. Without it only half of the
# composition rule's alternative has a case behind it, and the subject already carries a
# `cozy_switch_and_assert_*` definition -- the day it calls an assert helper
# inside, the inner call would be counted as a call site of its own.
cozy_switch_and_assert_composed() {
  cozy_assert_inner_of_switch "${test_name}"
}

# The same definition with a space before the parens, which is a definition
# just as much and must not read as a call.
cozy_assert_definition_with_space () { # @spaced-open
  cozy_assert_inner_of_spaced "${test_name}"
} # @spaced-close

# A call in another function body. Source position cannot place it: it runs
# wherever this helper is invoked, which may be before or after the wait.
helper_with_assertion() {
  cozy_assert_inside_another_function "${test_name}" # @call
}

# Spellings of one budget, for the matcher that reads it. Prefixed with `echo`
# rather than commented out, and that is the only form that works here: the
# budget matcher cuts comments before reading the value, so a `#` in front of
# these lines strips them to nothing and the matcher finds no budget at all.
# The `echo` keeps them off the wait matcher's answer -- it reads a command in
# command position -- while leaving the line for the budget matcher to read by
# number.
  echo BUDGET_PROBE kubectl wait hr -n tenant-test "kubernetes-${test_name}" --timeout=5m --for=condition=ready # @budget-probe
  echo BUDGET_PROBE kubectl wait hr -n tenant-test "kubernetes-${test_name}" --timeout="5m" --for=condition=ready # @budget-probe
  echo BUDGET_PROBE kubectl wait hr -n tenant-test "kubernetes-${test_name}" --timeout 5m --for=condition=ready # @budget-probe

# A line at file scope, followed by a column-0 `}` inside a heredoc -- which is
# how one really occurs, and why this file still parses as shell. The brace
# must not close a body, and must not extend the one that closed above it to
# cover this line.
at_file_scope_between_a_close_and_a_stray_brace=1 # @after-close
kubectl patch something --type=json -p "$(cat <<'JSON'
{"op": "replace", "path": "/spec/x", "value": {
}
JSON
)"
  echo BUDGET_PROBE kubectl wait hr -n tenant-test "kubernetes-${test_name}" --for=condition=ready # --timeout=5m # @budget-in-comment

# Command position that is not the start of a line. Each of these runs where it
# stands whenever its condition holds -- which is what the ordering rule has to
# place, since a conditional call before the wait is still a call before it --
# and the script this guard reads writes the and-list form elsewhere.
# A comment carrying the widened matcher's own shape. It is not a call site,
# and what excludes it is the comment cut running before the match -- not the
# pattern, which would take it. Remove the cut and this line is counted.
#   [ x ] && cozy_assert_in_comment "$1"

cozy_shapes_in_command_position() {
  [ "$enable_oidc" = true ] && cozy_assert_oidc_system "$1" # @call
  [ "$enable_oidc" = true ] || cozy_assert_fallback "$1" # @call
  for _n in 1; do cozy_assert_in_loop "$1"; done # @call
  if [ -n "$1" ]; then cozy_assert_after_then "$1"; fi # @call
  { cozy_assert_in_group "$1"; } # @call
  if [ -n "$1" ]; then :; else cozy_assert_after_else "$1"; fi # @call
  ( cozy_assert_in_subshell "$1" ) # @call
  # No space after the operator, which the shell accepts and which the
  # tolerance in the pattern exists for.
  [ "$enable_oidc" = true ] &&cozy_assert_no_space "$1" # @call
  # Excluded by the closing quote, not by being inside a string: the matcher
  # requires whitespace, a `|`, a `;`, an `&` or the line end after the name,
  # and a `"` is none of them.
  echo "not a call: && cozy_assert_quoted"
}
