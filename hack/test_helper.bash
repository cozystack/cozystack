# Shared setup for the hack/*.bats unit suite.
#
# Bats enforces `set -e` on a test body but does not enable `set -u`, while
# hack/cozytest.sh -- the runner this suite used before #3453 -- ran every body
# under `set -eu -x`. Without this file a test that reads an unset variable
# silently sees an empty string and passes, which is the one strictness
# property the move to bats would otherwise drop.
#
# `set -u` here is deliberately scoped to the test body. It is a shell option,
# not an exported one, so it does not reach the hack/*.sh scripts the tests
# exercise as subprocesses. Exporting it instead (SHELLOPTS=nounset in the
# environment) does propagate, changes the behaviour of the scripts under test
# rather than the tests themselves, and was measured to break the suite -- so it
# is not an alternative to loading this file.
#
# WHY EVERY FILE CARRIES A `load` LINE, AND WHY THAT IS NOT LEFT TO MEMORY.
#
# Bats offers no hook that reaches a test body without the file's cooperation.
# Its default `setup()` is defined in bats-core's own test_functions.bash and
# sourced BEFORE the test file, so nothing outside the file can replace it;
# `setup_suite` (--setup-suite-file, or an auto-discovered setup_suite.bash)
# runs once in bats-exec-suite's own process and never in the process that runs
# a test; and the two environment routes that do cross a process boundary --
# SHELLOPTS and BASH_ENV -- are exported by definition, so they reach the
# scripts under test as well and change the code rather than the tests. A
# per-file `load` is therefore the only mechanism bats has.
#
# Which makes it exactly the kind of requirement that rots: the first version of
# this file covered 32 of 32 unit files, and by the time anyone looked again it
# covered 32 of 63, because coverage was a line an author had to remember and
# nothing anywhere enumerated the files that were missing it. hack/
# bats-strict-setup.bats now does that enumeration -- it walks the same
# hack/*.bats minus hack/e2e-*.bats set the Makefile hands to the runner and
# fails on any file that does not load this helper. A file added next month is
# in the audit's input the moment it exists, so the gap closes at the commit
# that opens it rather than at whatever later date someone counts.
#
# A test file that needs its own fixture setup must call `strict_setup` from its
# `setup()` rather than relying on the definition below, since a later `setup()`
# in the file silently replaces this one. That is the second way the strictness
# can be lost without anyone noticing, so the audit checks it too.
strict_setup() {
  set -u
}

setup() {
  strict_setup
}
