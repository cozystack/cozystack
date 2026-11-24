#!/bin/sh

# Package roots visited by the Helm unit-test sweep.
# Both the runner and its coverage guard must use this list.
HELM_TEST_PACKAGE_ROOTS="packages/apps packages/core packages/extra packages/system packages/library packages/tests"

helm_test_package_root_known() {
    case " $HELM_TEST_PACKAGE_ROOTS " in
        *" $1 "*) return 0 ;;
        *) return 1 ;;
    esac
}
