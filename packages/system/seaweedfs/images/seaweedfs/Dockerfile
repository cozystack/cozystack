# Rebuild the weed binary from the upstream sources with the Cozystack patches
# applied, and lay it over the upstream image of the same tag. Only the Go
# binary changes: the Rust volume server and worker, the entrypoint and the
# runtime packages are the upstream image's own, byte for byte.
#
# patches/reader-cache-prefetch-window.diff bounds the read-ahead of a
# sequential chunk reader to the prefetch count it was given. Upstream counts
# only the downloads it newly starts toward that window and runs the prefetch
# after every ReadAt, so the S3 GET path, reading 256 KiB at a time out of
# 4 MiB chunks, pushes the window four chunks further on each read until the
# ReaderCache limit of 256 downloaders is reached. The gateway then holds up to
# about a gigabyte of a single object in memory, is OOM-killed under the 512Mi
# limit the S3 deployment gets from its resources preset, and the client sees
# the response cut off tens of megabytes in:
# https://github.com/cozystack/cozystack/issues/3782. Upstream carries the same
# defect on master, tracked as seaweedfs/seaweedfs#11672; drop the patch on the
# first release that carries a fix.

# Keep in step with the chart appVersion pulled by `make update`, and with the
# base image below: the patched binary replaces this version's, so building it
# from another tag would run one version's weed against the rest of another's
# image.
ARG VERSION=4.45

FROM --platform=$BUILDPLATFORM golang:1.26@sha256:eb36c1664dd974cde625f736e02c204383deebe03977365caaec5bf49f794348 AS builder
ARG GOPROXY
ARG VERSION
ARG TARGETOS
ARG TARGETARCH

WORKDIR /workspace

RUN curl -sSL https://github.com/seaweedfs/seaweedfs/archive/refs/tags/${VERSION}.tar.gz | tar -xzf- --strip=1

COPY patches /patches
RUN git apply /patches/*.diff

# go build never compiles a _test.go, so nothing else runs the tests the
# patches carry. The list is read into a variable first: a for loop drops the
# exit status of a command substitution, so a patch git cannot read would leave
# the list empty and the step green.
RUN files=$(git apply --numstat /patches/*.diff) || exit 1; \
    for dir in $(printf '%s\n' "$files" | cut -f3 | grep '\.go$' | xargs -r -n1 dirname | sort -u); do \
      go test -race "./$dir" || exit 1; \
    done

RUN cd weed && GOOS=$TARGETOS GOARCH=$TARGETARCH CGO_ENABLED=0 go build -o /workspace/weed-bin .

FROM chrislusf/seaweedfs:${VERSION}@sha256:fc9f76fa993ad69966ffeb2f65d0318fcae39c6f8e20cf68ef7b3a5cb97769e5
COPY --from=builder /workspace/weed-bin /usr/bin/weed
