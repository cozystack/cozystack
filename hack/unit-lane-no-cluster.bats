#!/usr/bin/env bats
# The unit and controller targets of the root Makefile must reach no cluster,
# whatever the caller's environment points at. A stub written as a shell
# function does not survive `timeout`, `env`, `xargs` or a fresh `bash -c`,
# which exec the real binary from PATH, so the guard has to sit where the
# whole lane inherits it rather than in each test.
#
# The test drives the real root Makefile from a miniature tree with a hostile
# environment: a kubeconfig path and an in-cluster service host that must not
# arrive. Each recipe in the lane is replaced by an echo of what it was given.
#
# Requires: make.

load test_helper

REPO_ROOT="$(cd "$(dirname "${BATS_TEST_FILENAME}")/.." && pwd)"

@test "every unit-tests prerequisite and test-controllers run on their own with no cluster to reach" {
  parts=$(sed -n 's/^unit-tests:[[:space:]]*//p' "$REPO_ROOT/Makefile")
  if [ -z "$parts" ]; then
    echo "FAIL: no prerequisites read from the unit-tests rule, so nothing below would be checked" >&2
    false
  fi

  fixture="$(mktemp -d)"
  mkdir "$fixture/hack"
  : >"$fixture/hack/common-envs.mk"
  : >"$fixture/hack/a.bats"
  {
    printf 'include %s/Makefile\n' "$REPO_ROOT"
    printf '%s test-controllers:\n' "$(printf '%s ' $parts)"
    printf '\t@echo "$@ KUBECONFIG=$${KUBECONFIG-unset} KUBERNETES_SERVICE_HOST=$${KUBERNETES_SERVICE_HOST-unset}"\n'
  } >"$fixture/lane.mk"

  # Each one as its own goal, not through unit-tests: a guard set only on
  # unit-tests reaches its prerequisites through inheritance, and would pass
  # here while `make bats-unit-tests` on its own ran unguarded. MAKEFLAGS and
  # MAKELEVEL are dropped so a parent `make unit-tests` lends this run nothing.
  env -u MAKEFLAGS -u MFLAGS -u MAKELEVEL \
    KUBECONFIG="$fixture/must-not-be-read" KUBERNETES_SERVICE_HOST=192.0.2.1 KUBERNETES_SERVICE_PORT=443 \
    make --file=lane.mk --directory="$fixture" $parts test-controllers >"$fixture/out" 2>&1
  cat "$fixture/out"

  for part in $parts test-controllers; do
    line=$(grep "^$part " "$fixture/out" || true)
    case "$line" in
      "$part KUBECONFIG=/dev/null KUBERNETES_SERVICE_HOST=") ;;
      "")
        echo "FAIL: $part did not run" >&2
        false
        ;;
      *)
        echo "FAIL: $part ran with a cluster to reach: $line" >&2
        false
        ;;
    esac
  done
  rm -rf "$fixture"
}
