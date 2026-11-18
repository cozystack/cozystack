#!/bin/sh

set -eu

for makefile in ./packages/*/*/Makefile; do
    [ -f "$makefile" ] || continue
    if awk '/^generate[[:space:]]*:/ { found=1 } END { exit !found }' "$makefile"; then
        dir=${makefile%/Makefile}
        echo "Running make generate in $dir"
        make generate -C "$dir"
    fi
done
