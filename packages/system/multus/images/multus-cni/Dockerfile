FROM --platform=$BUILDPLATFORM golang:1.26@sha256:079e59808d2d252516e27e3f3a9c003740dee7f75e55aa71528766d52bcfc16a AS build

ARG VERSION=v4.3.0
ARG TARGETPLATFORM

WORKDIR /usr/src/multus-cni

RUN curl -sSL https://github.com/k8snetworkplumbingwg/multus-cni/archive/refs/tags/${VERSION}.tar.gz | \
    tar -xzf - --strip=1

COPY patches /patches
RUN git init && git config user.email "build@cozystack" && git config user.name "build" && \
    git add -A && git commit -m "init" && git tag ${VERSION} && \
    git apply /patches/*.diff

RUN ./hack/build-go.sh

FROM debian:stable-slim@sha256:ee12ffb55625b99d62837a72f037d9b2f18fd0c787a89c2b9a4f09666c48776c
LABEL org.opencontainers.image.source=https://github.com/cozystack/cozystack
COPY --from=build /usr/src/multus-cni/bin /usr/src/multus-cni/bin
COPY --from=build /usr/src/multus-cni/LICENSE /usr/src/multus-cni/LICENSE
COPY --from=build /usr/src/multus-cni/bin/cert-approver /
WORKDIR /

ENTRYPOINT ["/usr/src/multus-cni/bin/multus-daemon"]
