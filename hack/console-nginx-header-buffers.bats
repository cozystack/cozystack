#!/usr/bin/env bats
# The console image bakes its nginx config into the Containerfile as an inline
# heredoc, so no chart template and no helm-unittest case can see it. This pins
# the header-buffer pair that image ships. The per-buffer size in
# large_client_header_buffers is what bounds a single Cookie header: on the
# default 4 8k an access token carrying many group claims does not fit one
# buffer, and nginx answers 400 "request header or cookie too large" before the
# request reaches the /apis, /api or /k8s/ upstream. client_header_buffer_size
# is raised alongside it so such a request is read without escalating to a large
# buffer at all. Either line is cheap to drop in an unrelated rewrite of that
# config, and the loss is invisible until a user with a big token logs in.
#
# The check reads the heredoc body, not the whole Containerfile, so a directive
# that moved out of the served config -- into a comment, or into a build stage
# that ships nothing -- does not read as present.
#
# Harness note: the CI path is hack/cozytest.sh, NOT real bats. No `run`,
# `$status`, `$output`, `skip` or setup()/teardown(); each @test is a shell
# function under `set -eu -x`, and paths resolve from the repo root. The two
# runners disagree on negative assertions, so an assertion written for bats can
# be vacuous under the one that gates a merge.
# Run with: hack/cozytest.sh hack/console-nginx-header-buffers.bats

REPO_ROOT="$(cd "$(dirname "${BATS_TEST_FILENAME:-$0}")/.." && pwd)"
CONSOLE_CONTAINERFILE="packages/system/dashboard/images/console/Containerfile"

# Prints an nginx size in KiB, or -1 for a spelling this check cannot read.
nginx_size_kib() {
    printf '%s\n' "$1" | awk '
        /^[0-9]+[kK]$/ { print int(substr($0, 1, length($0) - 1)); next }
        /^[0-9]+[mM]$/ { print int(substr($0, 1, length($0) - 1)) * 1024; next }
        /^[0-9]+$/     { print int($0 / 1024); next }
        { print -1 }
    '
}

@test "console nginx config reads client request headers into buffers larger than the nginx defaults" {
    cd "$REPO_ROOT"

    conf=$(awk '
        /^COPY <<.EOF. \/etc\/nginx\/conf\.d\/default\.conf$/ { inblock = 1; next }
        inblock && /^EOF$/ { inblock = 0; next }
        inblock { print }
    ' "$CONSOLE_CONTAINERFILE")
    [ -n "$conf" ] || { echo "no /etc/nginx/conf.d/default.conf heredoc found in $CONSOLE_CONTAINERFILE" >&2; exit 1; }

    # An extraction that silently grabbed the wrong slice would let every
    # assertion below pass on an empty-ish config, so anchor on a line the
    # served config is known to carry.
    printf '%s\n' "$conf" | grep -q 'proxy_pass https://kubernetes.default.svc:443;' \
        || { echo "extracted heredoc carries no apiserver proxy_pass; the extraction is wrong, not the config" >&2; exit 1; }

    header_buffer=$(printf '%s\n' "$conf" | awk '$1 == "client_header_buffer_size" { sub(/;$/, "", $2); print $2 }')
    [ -n "$header_buffer" ] || { echo "client_header_buffer_size is absent; the console ships it at 16k next to large_client_header_buffers" >&2; exit 1; }
    header_buffer_kib=$(nginx_size_kib "$header_buffer")
    [ "$header_buffer_kib" -ge 16 ] \
        || { echo "client_header_buffer_size reads as $header_buffer, and this check wants at least 16k" >&2; exit 1; }

    # Third field, because the directive is `large_client_header_buffers <number> <size>`
    # and it is the per-buffer size that bounds one Cookie header.
    large_buffer=$(printf '%s\n' "$conf" | awk '$1 == "large_client_header_buffers" { sub(/;$/, "", $3); print $3 }')
    [ -n "$large_buffer" ] || { echo "large_client_header_buffers is absent: a Cookie header over 8k gets a 400 from the console nginx" >&2; exit 1; }
    large_buffer_kib=$(nginx_size_kib "$large_buffer")
    [ "$large_buffer_kib" -ge 16 ] \
        || { echo "large_client_header_buffers reads as $large_buffer per buffer, and a large OIDC token needs at least 16k" >&2; exit 1; }

    # Placement, not only presence. A header field read before the Host header
    # has selected the virtual server is measured against the default server's
    # buffers, so the same lines inside a server block cover that server only
    # while it stays the default one. The shipped config puts them ahead of the
    # first server block, and that is the shape checked here.
    prelude=$(printf '%s\n' "$conf" | awk '/^server[[:space:]]*{/ { exit } { print }')
    printf '%s\n' "$prelude" | grep -q '^client_header_buffer_size' \
        || { echo "client_header_buffer_size does not stand at the top of the config, ahead of the first server block" >&2; exit 1; }
    printf '%s\n' "$prelude" | grep -q '^large_client_header_buffers' \
        || { echo "large_client_header_buffers does not stand at the top of the config, ahead of the first server block" >&2; exit 1; }
}
