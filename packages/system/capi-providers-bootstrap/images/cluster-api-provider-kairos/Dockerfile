# Builds the kairos-io/cluster-api-provider-kairos manager from an upstream
# release with patches/ applied. A patch placed there has an open upstream pull
# request, and is removed in the first Cozystack release that picks up an
# upstream release containing it.
FROM --platform=$BUILDPLATFORM golang:1.27@sha256:e432b43af23a9328d56a7c499be0476810aa344acbcf65fc7c455d4ff5a40602 AS builder
ARG GOPROXY

# Upstream release. Bump together with KAIROS_COMPONENT_VERSION in the package
# Makefile, and SOURCE_SHA256 with it: GitHub publishes no checksum for a tag
# archive.
ARG VERSION=v0.2.3
ARG SOURCE_SHA256=71051ea4803dbf54ef238dd386760c2362498c9c6bcd2b0fee3b121d86820ffa
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
