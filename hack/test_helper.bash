# Bats supplies errexit but not nounset. Enable nounset in each test process
# without exporting shell options to production scripts launched by the tests.
# A file-local setup() must call strict_setup first; bats-strict-setup.bats
# audits that contract against the Makefile's discovered unit files.
strict_setup() {
  set -u
}

setup() {
  strict_setup
}
