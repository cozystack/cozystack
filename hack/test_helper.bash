# Shared setup for the hack/*.bats unit suite.
#
# Bats enforces `set -e` on a test body but does not enable `set -u`, while
# hack/cozytest.sh -- the runner this suite used before #3453 -- ran every
# body under `set -eu -x`. Without this file a test that reads an unset
# variable silently sees an empty string and passes, which is the one
# strictness property the move to bats would otherwise drop.
#
# `set -u` here is deliberately scoped to the test body. It is a shell
# option, not an exported one, so it does not reach the hack/*.sh scripts
# the tests exercise as subprocesses. Exporting it instead (SHELLOPTS=nounset
# in the environment) does propagate, changes the behaviour of the scripts
# under test rather than the tests themselves, and was measured to break the
# suite -- so it is not an alternative to loading this file.
#
# A test file that needs its own fixture setup must call `strict_setup`
# from its `setup()` rather than relying on the definition below, since a
# later `setup()` in the file silently replaces this one.
strict_setup() {
  set -u
}

setup() {
  strict_setup
}
