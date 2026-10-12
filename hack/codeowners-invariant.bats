#!/usr/bin/env bats
# -----------------------------------------------------------------------------
# Coverage invariant for .github/CODEOWNERS.
#
# CODEOWNERS applies only the LAST matching rule, and matching is not
# additive: a scoped rule fully replaces the * catch-all instead of extending
# it. The only way to keep the default owners able to review everything is to
# repeat their handles in every scoped rule. A rule that omits one of them
# silently carves its path out from under the catch-all, and since "Require
# review from Code Owners" is enforced on main and stale reviews are dismissed
# on push, that path then deadlocks whenever its remaining owners are away,
# with no way around it short of an admin override.
#
# The invariant: every rule that lists owners must include every owner of the
# * catch-all. Ownerless rules are the one exception, being generated files
# whose review is waived on purpose.
#
# Changing the catch-all owners is a policy change; this test failing on such
# a change is the point - adjust the expectation here in the same PR, with the
# policy discussion linked.
# -----------------------------------------------------------------------------

load test_helper

@test "every CODEOWNERS rule that lists owners repeats the catch-all owners" {
  [ -f .github/CODEOWNERS ]

  awk '
    /^[[:space:]]*(#|$)/ { next }
    $1 == "*" { for (i = 2; i <= NF; i++) defaults[$i]; ndef = NF - 1; next }
    NF < 2 { next }
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
