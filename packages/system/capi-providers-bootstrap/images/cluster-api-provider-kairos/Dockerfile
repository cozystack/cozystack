# Builds the kairos-io/cluster-api-provider-kairos manager from an upstream
# release with patches/ applied. A patch placed there has an open upstream pull
# request, and is removed in the first Cozystack release that picks up an
# upstream release containing it.
FROM --platform=$BUILDPLATFORM golang:1.27@sha256:3680233e3204827fbdc66088528ae6d4b3d034f51d03a99d454f6de034888244 AS builder
ARG GOPROXY

# Upstream release. Bump together with KAIROS_COMPONENT_VERSION in the package
# Makefile, and SOURCE_SHA256 with it: GitHub publishes no checksum for a tag
# archive.
ARG VERSION=v0.2.1
ARG SOURCE_SHA256=a86b6e66ce48c3792750123667516d62ca258a40635f38810c36a64635131879
ARG TARGETOS
ARG TARGETARCH

WORKDIR /workspace

RUN curl -fsSL -o /tmp/source.tar.gz "https://github.com/kairos-io/cluster-api-provider-kairos/archive/refs/tags/${VERSION}.tar.gz" && \
    echo "${SOURCE_SHA256}  /tmp/source.tar.gz" | sha256sum -c - && \
    tar -xzf /tmp/source.tar.gz --strip=1 && rm /tmp/source.tar.gz

COPY patches /patches
RUN set -- /patches/*.diff; [ -e "$1" ] || exit 0; git apply --recount "$@"

# Same reasoning as the Kamaji control-plane provider image: go build never
# compiles a _test.go, so the tests of the packages the patches touch run here.
RUN set -- /patches/*.diff; [ -e "$1" ] || exit 0; \
    files=$(git apply --numstat --recount "$@") || exit 1; \
    for dir in $(printf '%s\n' "$files" | cut -f3 | grep '\.go$' | xargs -r -n1 dirname | sort -u); do \
      (cd "$dir" && go test .) || exit 1; \
    done

RUN CGO_ENABLED=0 GOOS=${TARGETOS:-linux} GOARCH=${TARGETARCH} \
    go build -a -o manager main.go

FROM gcr.io/distroless/static:nonroot@sha256:e2e927ec666bae08560abb3c55d0659eceabb657f56b6782ab500a9fc7f555e3
WORKDIR /
COPY --from=builder /workspace/manager /manager
USER 65532:65532

ENTRYPOINT ["/manager"]
