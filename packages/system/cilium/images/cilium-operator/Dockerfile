ARG VERSION=v1.20.2
ARG SOURCE_SHA256=2d9dd9697bf59dc592053734ac0dffe63892fc3e1a4d1fd28e1f44a0c5a49916
ARG UPSTREAM_DIGEST=sha256:64d8798350e8569b8e7622563fed6e44dce2625f311e4651b774816516c744fc

# The Go toolchain of upstream's builder image (images/builder/Dockerfile),
# which compiles the upstream operator.
FROM --platform=$BUILDPLATFORM docker.io/library/golang:1.26.8@sha256:3c3e25a4da13fd0478eed2df1eb35a0e667094a7124d3993a6a1d30f71c17e79 AS builder
ARG GOPROXY
ARG VERSION
ARG SOURCE_SHA256

WORKDIR /go/src/github.com/cilium/cilium

RUN curl -fsSL -o /tmp/source.tar.gz https://github.com/cilium/cilium/archive/refs/tags/${VERSION}.tar.gz && \
    echo "${SOURCE_SHA256}  /tmp/source.tar.gz" | sha256sum -c - && \
    tar -xzf /tmp/source.tar.gz --strip-components=1 && rm /tmp/source.tar.gz

COPY patches /patches
RUN git apply /patches/*.diff

ENV GOFLAGS=-mod=vendor CGO_ENABLED=0

# go build never compiles a _test.go, so nothing else runs the tests of the
# packages the patches touch. Each package is tested from its own directory:
# that finds the module it belongs to, and an empty list runs nothing.
# The list is read into a variable first: a for loop drops the exit status
# of a command substitution, so a patch git cannot read would leave the list
# empty and the step green.
RUN files=$(git apply --numstat /patches/*.diff) || exit 1; \
    for dir in $(printf '%s\n' "$files" | cut -f3 | grep '\.go$' | xargs -r -n1 dirname | sort -u); do \
      (cd "$dir" && go test .) || exit 1; \
    done

# Declared after the tests so they run once on the build platform rather than
# once per target platform.
ARG TARGETOS
ARG TARGETARCH

# Same tags and version variable as upstream's cilium-operator-generic target.
# The version keeps upstream's "<version> <revision> <date>" shape, which
# pkg/version.FromString needs to fill the operator's version metric; the
# revision field marks this build apart from the upstream one. The date is the
# commit time GitHub stamps on every file of the tag archive, so the same
# inputs give the same binary.
RUN GOOS=$TARGETOS GOARCH=$TARGETARCH go build \
    -tags osusergo,ipam_provider_operator \
    -ldflags "-s -w -X 'github.com/cilium/cilium/pkg/version.ciliumVersion=${VERSION#v} cozystack-patched $(date -u -d @$(stat -c %Y VERSION) +%Y-%m-%dT%H:%M:%SZ)'" \
    -o /out/cilium-operator-generic ./operator

FROM quay.io/cilium/operator-generic:${VERSION}@${UPSTREAM_DIGEST}
COPY --from=builder /out/cilium-operator-generic /usr/bin/cilium-operator-generic
