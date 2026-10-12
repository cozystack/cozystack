#!/bin/sh
# Fixture for hack/run-kubernetes-install-wait_test.bats.
# Not executed: read as text, and deliberately carries the fault the case
# looks for -- an apostrophe inside a comment in an awk program, which ends
# the single-quoted shell string holding it.
scan() {
  awk '
    # The heredoc's body is data, and the quote in that word is the fault: it
    # ends the string holding this program, so this file does not parse as
    # shell at all. Nothing runs it -- the case reads it as text -- and a
    # fixture that parsed would not carry what the case looks for.
    /^x/ { print }
  ' "$1"
}
