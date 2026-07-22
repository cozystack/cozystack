FROM --platform=$BUILDPLATFORM golang:1.26@sha256:3aff6657219a4d9c14e27fb1d8976c49c29fddb70ba835014f477e1c70636647 AS build

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

FROM debian:stable-slim@sha256:328d16499860ae6cb9b345e2e4cebca08c2a36e4f7278482c7bd1f39d71e5bfd
LABEL org.opencontainers.image.source=https://github.com/cozystack/cozystack
COPY --from=build /usr/src/multus-cni/bin /usr/src/multus-cni/bin
COPY --from=build /usr/src/multus-cni/LICENSE /usr/src/multus-cni/LICENSE
COPY --from=build /usr/src/multus-cni/bin/cert-approver /
WORKDIR /

ENTRYPOINT ["/usr/src/multus-cni/bin/multus-daemon"]
