#!/bin/sh
# Compare what packages/system/kubeovn/images/kubeovn/Dockerfile copies into the
# kube-ovn image against upstream's dist/images/Dockerfile, which it is a
# hand-kept copy of.
#
# `make update` in that package rewrites only ARG VERSION and ARG TAG, so a
# binary upstream starts shipping does not reach our image on its own: v1.15.26
# added kube-ovn-bfdd-supervisor, our copy kept the v1.15.10 set, and a
# VpcEgressGateway with spec.bfd got a container whose command did not exist.
#
# Usage: hack/check-kubeovn-image-copies.sh OURS UPSTREAM
# Either path may be `-` to read that side from stdin.

set -eu

[ $# -eq 2 ] || {
    echo "Usage: $0 OURS UPSTREAM  (either path may be '-' for stdin)" >&2
    exit 2
}

OURS=$1
UPSTREAM=$2

# Normalize the COPY instructions of every stage built FROM kubeovn/kube-ovn-base
# to `<source>... <destination>` lines.
#
# Only those stages are comparable. Our builder stage clones and patches
# kube-ovn, which upstream does not do at all, so its COPY lines have no
# counterpart and reading them would make every run fail.
#
# Flags are dropped so that `--from=builder /source/dist/images/kube-ovn` and a
# bare `kube-ovn` read the same: upstream copies out of a build context that our
# multi-stage build reproduces under /source/dist/images.
copies() {
    if [ "$1" = "-" ]; then
        cat
    else
        cat -- "$1"
    fi | awk '
        function strip(path) {
            sub(/^\/source\/dist\/images\//, "", path)
            return path
        }
        # Fold a continued instruction into one logical line before reading it.
        {
            line = line $0
            if (line ~ /\\$/) {
                sub(/\\$/, " ", line)
                next
            }
        }
        line ~ /^[[:space:]]*FROM[[:space:]]/ {
            in_scope = (line ~ /kubeovn\/kube-ovn-base/)
            line = ""
            next
        }
        in_scope && line ~ /^[[:space:]]*COPY[[:space:]]/ {
            $0 = line
            out = ""
            for (i = 2; i <= NF; i++) {
                if ($i ~ /^--/) continue
                out = out (out == "" ? "" : " ") strip($i)
            }
            if (out != "") print out
        }
        { line = "" }
    ' | LC_ALL=C sort
}

# Lines on stdin that do not appear in the newline-separated set $1.
#
# The set is fed through the same stream rather than `awk -v`: a multi-line -v
# value is a GNU extension, and the awk on a maintainer's macOS rejects it.
lines_not_in() {
    {
        printf '%s\n' "$1" | sed 's/^/S /'
        sed 's/^/C /'
    } | awk '
        $1 == "S" { sub(/^S /, ""); seen[$0] = 1; next }
        { sub(/^C /, ""); if ($0 != "" && !($0 in seen)) print }
    '
}

ours_copies=$(copies "$OURS")
upstream_copies=$(copies "$UPSTREAM")

# A side with no copies at all means the stage markers stopped matching, not
# that the two agree. Reporting that as a pass is how this check would quietly
# stop working the next time the base image is renamed.
if [ -z "$ours_copies" ]; then
    echo "ERROR: no COPY instructions found in a kubeovn/kube-ovn-base stage of $OURS" >&2
    exit 1
fi
if [ -z "$upstream_copies" ]; then
    echo "ERROR: no COPY instructions found in a kubeovn/kube-ovn-base stage of $UPSTREAM" >&2
    exit 1
fi

missing=$(printf '%s\n' "$upstream_copies" | lines_not_in "$ours_copies")
extra=$(printf '%s\n' "$ours_copies" | lines_not_in "$upstream_copies")

status=0

if [ -n "$missing" ]; then
    echo "ERROR: upstream copies these into the image and we do not:" >&2
    printf '%s\n' "$missing" | sed 's/^/  COPY /' >&2
    status=1
fi

if [ -n "$extra" ]; then
    echo "ERROR: we copy these into the image and upstream does not:" >&2
    printf '%s\n' "$extra" | sed 's/^/  COPY /' >&2
    status=1
fi

if [ "$status" -ne 0 ]; then
    echo >&2
    echo "packages/system/kubeovn/images/kubeovn/Dockerfile is a copy of upstream's" >&2
    echo "dist/images/Dockerfile. Bring the two back in step, keeping our" >&2
    echo "--from=builder /source/dist/images/ prefix." >&2
fi

exit "$status"
