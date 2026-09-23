#!/usr/bin/env bats
# -----------------------------------------------------------------------------
# Unit tests for hack/check-kubeovn-image-copies.sh.
#
# packages/system/kubeovn/images/kubeovn/Dockerfile is a hand-kept copy of
# upstream's dist/images/Dockerfile, and `make update` rewrites only ARG VERSION
# and ARG TAG in it. When v1.15.26 added kube-ovn-bfdd-supervisor to the image,
# our copy kept the v1.15.10 set and the binary went missing, which nothing in
# the tree noticed. The checker compares what each file copies, and this suite
# runs it over the real Dockerfile against a verbatim copy of upstream's for the
# vendored appVersion -- a synthetic pair alone would only assert the shape we
# already imagined.
#
# cozytest.sh's awk parser recognizes only @test blocks and a bare `}` on its
# own line; there is no bats `run` or `$status`. Assertions are direct shell
# tests, and an expected failure is written as `if CHECK ...; then ... exit 1`.
# -----------------------------------------------------------------------------

REPO_ROOT="$(cd "$(dirname "${BATS_TEST_FILENAME:-$0}")/.." && pwd)"
CHECK="$REPO_ROOT/hack/check-kubeovn-image-copies.sh"
OURS="$REPO_ROOT/packages/system/kubeovn/images/kubeovn/Dockerfile"
CHART="$REPO_ROOT/packages/system/kubeovn/charts/kube-ovn/Chart.yaml"

# Upstream dist/images/Dockerfile at v1.15.26, copied verbatim. Bump it in the
# same change that bumps the chart: `make update` re-runs the same comparison
# against the tag it pulls, so the two cannot disagree for long.
UPSTREAM_VERSION=1.15.26
upstream_dockerfile() {
    cat <<'EOF'
# syntax=docker/dockerfile:1
ARG VERSION
ARG BASE_TAG=$VERSION
FROM kubeovn/kube-ovn-base:$BASE_TAG AS setcap

COPY *.sh /kube-ovn/
COPY kubectl-ko /kube-ovn/kubectl-ko
COPY 01-kube-ovn.conflist /kube-ovn/01-kube-ovn.conflist

COPY kube-ovn /kube-ovn/kube-ovn
COPY kube-ovn-cmd /kube-ovn/kube-ovn-cmd
COPY kube-ovn-bfdd-supervisor /kube-ovn/kube-ovn-bfdd-supervisor
COPY kube-ovn-daemon /kube-ovn/kube-ovn-daemon
COPY kube-ovn-controller /kube-ovn/kube-ovn-controller
RUN ln -s /kube-ovn/kube-ovn-cmd /kube-ovn/kube-ovn-monitor && \
    ln -s /kube-ovn/kube-ovn-cmd /kube-ovn/kube-ovn-speaker && \
    ln -s /kube-ovn/kube-ovn-cmd /kube-ovn/kube-ovn-webhook && \
    ln -s /kube-ovn/kube-ovn-cmd /kube-ovn/kube-ovn-leader-checker && \
    ln -s /kube-ovn/kube-ovn-cmd /kube-ovn/kube-ovn-ic-controller && \
    ln -s /kube-ovn/kube-ovn-controller /kube-ovn/kube-ovn-pinger && \
    setcap CAP_NET_BIND_SERVICE+eip /kube-ovn/kube-ovn-cmd && \
    setcap CAP_NET_RAW,CAP_NET_BIND_SERVICE+eip /kube-ovn/kube-ovn-controller && \
    setcap CAP_NET_ADMIN,CAP_NET_RAW,CAP_NET_BIND_SERVICE,CAP_SYS_ADMIN+eip /kube-ovn/kube-ovn-daemon

FROM kubeovn/kube-ovn-base:$BASE_TAG

COPY --chmod=0644 logrotate/* /etc/logrotate.d/
COPY grace_stop_ovn_controller /usr/share/ovn/scripts/grace_stop_ovn_controller

COPY --from=setcap /kube-ovn /kube-ovn
RUN /kube-ovn/iptables-wrapper-installer.sh --no-sanity-check

WORKDIR /kube-ovn
EOF
}

@test "the fixture tracks the vendored chart version" {
    # Chart.yaml quotes the value; the package Makefile leans on the shell to
    # strip those, this runs the awk output through a comparison instead.
    appversion=$(awk '$1 == "appVersion:" {gsub(/"/, "", $2); print $2}' "$CHART")
    if [ "$appversion" != "$UPSTREAM_VERSION" ]; then
        echo "FAIL: chart appVersion is $appversion, fixture is v$UPSTREAM_VERSION" >&2
        echo "Refresh upstream_dockerfile() from dist/images/Dockerfile at v$appversion." >&2
        return 1
    fi
}

@test "our Dockerfile copies everything upstream's does" {
    upstream_dockerfile | "$CHECK" "$OURS" -
}

@test "a binary upstream added and we did not is reported" {
    ours=$(mktemp)
    upstream=$(mktemp)
    upstream_dockerfile > "$upstream"
    grep -v 'kube-ovn-bfdd-supervisor' "$OURS" > "$ours"

    if "$CHECK" "$ours" "$upstream" 2>/dev/null; then
        echo "FAIL: a missing binary passed the check" >&2
        return 1
    fi
    if ! "$CHECK" "$ours" "$upstream" 2>&1 | grep -q 'kube-ovn-bfdd-supervisor'; then
        echo "FAIL: the report does not name the missing binary" >&2
        return 1
    fi
}

@test "a copy we make and upstream does not is reported" {
    ours=$(mktemp)
    upstream=$(mktemp)
    upstream_dockerfile > "$upstream"
    sed -e 's|^COPY --from=builder /source/dist/images/kube-ovn-cmd .*|&\nCOPY --from=builder /source/dist/images/kube-ovn-invented /kube-ovn/kube-ovn-invented|' "$OURS" > "$ours"

    if "$CHECK" "$ours" "$upstream" 2>/dev/null; then
        echo "FAIL: an extra copy passed the check" >&2
        return 1
    fi
    if ! "$CHECK" "$ours" "$upstream" 2>&1 | grep -q 'kube-ovn-invented'; then
        echo "FAIL: the report does not name the extra copy" >&2
        return 1
    fi
}

@test "COPY lines in the builder stage are ours alone and not compared" {
    # `COPY patches /patches` and the regression test have no upstream
    # counterpart: they feed our clone-and-patch build, which upstream does not
    # have. Reading them would make the check fail on every run.
    ours=$(mktemp)
    upstream=$(mktemp)
    upstream_dockerfile > "$upstream"
    sed -e 's|^COPY patches /patches$|COPY patches /patches\nCOPY extra-build-input /some/where|' "$OURS" > "$ours"

    "$CHECK" "$ours" "$upstream"
}

@test "a destination that differs is reported even when the source matches" {
    ours=$(mktemp)
    upstream=$(mktemp)
    upstream_dockerfile > "$upstream"
    sed -e 's|/kube-ovn/kube-ovn-daemon$|/kube-ovn/kube-ovn-daemon-typo|' "$OURS" > "$ours"

    if "$CHECK" "$ours" "$upstream" 2>/dev/null; then
        echo "FAIL: a retargeted copy passed the check" >&2
        return 1
    fi
}

@test "the checker reads the file it is given and not the tree" {
    empty=$(mktemp)
    upstream=$(mktemp)
    upstream_dockerfile > "$upstream"
    printf 'FROM scratch\n' > "$empty"

    if "$CHECK" "$empty" "$upstream" 2>/dev/null; then
        echo "FAIL: a Dockerfile with no copies at all passed the check" >&2
        return 1
    fi
}
