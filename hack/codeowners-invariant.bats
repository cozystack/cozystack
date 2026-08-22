#!/usr/bin/env bats
# -----------------------------------------------------------------------------
# Coverage invariant for .github/CODEOWNERS.
#
# CODEOWNERS applies only the LAST matching rule, and matching is not
# additive: a scoped rule fully replaces the * catch-all instead of extending
# it. The only way to keep the default owners able to review everything is to
# repeat their handles in every scoped rule. A rule that omits them silently
# carves its path out from under the catch-all — that is how /hack/,
# /.github/, /Makefile, /.pre-commit-config.yaml and /docs/ ended up owned by
# exactly two people, deadlocking every PR one of them authored while the
# other was away ("Require review from Code Owners" is enforced on main, and
# stale reviews are dismissed on push, so there is no way around a missing
# owner short of an admin override).
#
# The invariant: every rule that lists owners must include every owner of the
# * catch-all. Two deliberate exceptions:
#   - ownerless rules — generated files whose review is waived on purpose;
#   - the governance roster, owned by exactly @tym83 @kvaps. It is identified
#     by that owner set rather than by its position in the file, so the
#     exemption cannot leak to rules appended under other sections.
#
# Changing the catch-all owners or growing the governance roster is a policy
# change; this test failing on such a change is the point — adjust the
# expectation here in the same PR, with the policy discussion linked.
# -----------------------------------------------------------------------------

@test "every CODEOWNERS rule that lists owners repeats the catch-all owners (governance excepted)" {
  [ -f .github/CODEOWNERS ]

  awk '
    /^[[:space:]]*(#|$)/ { next }
    $1 == "*" { for (i = 2; i <= NF; i++) defaults[$i]; ndef = NF - 1; next }
    NF < 2 { next }
    NF == 3 && (($2 == "@tym83" && $3 == "@kvaps") || ($2 == "@kvaps" && $3 == "@tym83")) { next }
    {
      for (d in defaults) {
        found = 0
        for (i = 2; i <= NF; i++) if ($i == d) found = 1
        if (!found) { printf "line %d: rule %s is missing default owner %s\n", FNR, $1, d; bad = 1 }
      }
    }
    END {
      if (ndef == 0) { print "no * catch-all rule with owners found"; exit 1 }
      exit bad
    }
  ' .github/CODEOWNERS
}
