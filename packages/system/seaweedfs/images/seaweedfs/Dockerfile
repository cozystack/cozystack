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
# https://github.com/cozystack/cozystack/issues/3782. Upstream fixed it in
# seaweedfs/seaweedfs#11674 after 4.48; drop the patch on the first vendored
# release that carries it.
#
# patches/volume-layout-crowded-lock.diff makes SetVolumeCrowded take the write
# lock of the volume layout. Upstream 4.45 mutates the crowded map under the
# read lock, while Assign reads it under the same read lock through
# GetWritableVolumeCount, so under a steady stream of writes the master dies on
# a fatal "concurrent map read and map write", which recover cannot catch, and
# S3 answers 500 until it is back. Backport of seaweedfs/seaweedfs#11216,
# released in 4.46; drop the patch on the first vendored release that carries
# it.
#
# `make update` re-pins VERSION from the vendored chart, SOURCE_SHA256 from the
# source archive of that tag, UPSTREAM_DIGEST from the upstream image of that
# tag and the builder from upstream's docker/Dockerfile.go_build, and checks
# that the patches still apply.
ARG VERSION=4.45
ARG SOURCE_SHA256=800a91693e2a4e974ef0e2a157b6a04eff9f4df507303b1cff31b0601081fc30
ARG UPSTREAM_DIGEST=sha256:fc9f76fa993ad69966ffeb2f65d0318fcae39c6f8e20cf68ef7b3a5cb97769e5

# The Go toolchain of upstream's builder (docker/Dockerfile.go_build), which
# compiles the upstream weed.
FROM --platform=$BUILDPLATFORM golang:1.26-alpine@sha256:3082400e369fa24d5fc60bca20edab3f6d604e0c5a690ec66b295eff4dd87ade AS builder
ARG GOPROXY
ARG VERSION
ARG SOURCE_SHA256

RUN apk add --no-cache git curl

WORKDIR /go/src/github.com/seaweedfs/seaweedfs

RUN curl -fsSL -o /tmp/source.tar.gz https://github.com/seaweedfs/seaweedfs/archive/refs/tags/${VERSION}.tar.gz && \
    echo "${SOURCE_SHA256}  /tmp/source.tar.gz" | sha256sum -c - && \
    tar -xzf /tmp/source.tar.gz --strip-components=1 && rm /tmp/source.tar.gz

COPY patches /patches
RUN git apply /patches/*.diff

ENV CGO_ENABLED=0

# go build never compiles a _test.go, so nothing else runs the tests the
# patches carry. The list is read into a variable first: a for loop drops the
# exit status of a command substitution, so a patch git cannot read would leave
# the list empty and the step green.
RUN files=$(git apply --numstat /patches/*.diff) || exit 1; \
    for dir in $(printf '%s\n' "$files" | cut -f3 | grep '\.go$' | xargs -r -n1 dirname | sort -u); do \
      go test "./$dir" || exit 1; \
    done

# Declared after the tests so they run once on the build platform rather than
# once per target platform.
ARG TARGETOS
ARG TARGETARCH

# The source archive carries no git history for upstream's `git rev-parse`, so
# the commit field of `weed version` names this build instead.
RUN cd weed && GOOS=$TARGETOS GOARCH=$TARGETARCH go build \
    -ldflags "-X github.com/seaweedfs/seaweedfs/weed/util/version.COMMIT=cozystack-patched" \
    -o /out/weed .

FROM chrislusf/seaweedfs:${VERSION}@${UPSTREAM_DIGEST}
COPY --from=builder /out/weed /usr/bin/weed
