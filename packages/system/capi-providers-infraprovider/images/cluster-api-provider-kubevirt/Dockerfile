FROM --platform=$BUILDPLATFORM golang:1.27.1@sha256:162be5298a40ed317005c8339c6de4d10d3eef336d66dc8e9259b03ab9d3a6d2 AS builder
ARG GOPROXY

ARG VERSION=v0.1.10
ARG COMMIT=6dbc11c4d0cbfbdcd618566538c10b0d4a07516b
ARG TARGETOS
ARG TARGETARCH

WORKDIR /workspace

RUN curl -fsSL "https://github.com/kubernetes-sigs/cluster-api-provider-kubevirt/archive/${COMMIT}.tar.gz" -o /tmp/source.tar.gz \
    && tar -xzf /tmp/source.tar.gz --strip-components=1 \
    && rm /tmp/source.tar.gz

COPY patches /patches
RUN git apply --check /patches/*.diff && git apply /patches/*.diff

RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    go test ./controllers ./pkg/...

RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 GOOS=${TARGETOS:-linux} GOARCH=${TARGETARCH} \
    go build -trimpath -o /manager .

FROM quay.io/capk/capk-manager:v0.1.10@sha256:87c2a80413a0f64f65326d379f4905d3f3f2eaf9dab8a6a67a7f7973ca85aef6
COPY --from=builder --chmod=0755 /manager /manager
USER 65532
ENTRYPOINT ["/manager"]
