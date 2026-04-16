# Helpers for asserting that a Flux HelmRelease did not fall into an
# install/upgrade remediation cycle during an e2e run.
#
# A non-zero installFailures/upgradeFailures counter means flux
# helm-controller hit its wait timeout, ran remediation (uninstall),
# and re-installed. That is exactly the race this guard is meant to
# catch, so the function returns success (0) when a cycle is detected
# and failure (1) otherwise.
#
# Both arguments may be empty strings, the literal "0", or a positive
# integer. Shell's && and || have equal precedence with left-to-right
# associativity, so each half of the disjunction is grouped explicitly
# to avoid (A && B) || C && D parsing that masks the common
# install_failures=1, upgrade_failures="" case.

helmrelease_has_remediation_cycle() {
    install_failures="$1"
    upgrade_failures="$2"
    if { [ -n "${install_failures}" ] && [ "${install_failures}" != "0" ]; } || \
       { [ -n "${upgrade_failures}" ] && [ "${upgrade_failures}" != "0" ]; }; then
        return 0
    fi
    return 1
}
