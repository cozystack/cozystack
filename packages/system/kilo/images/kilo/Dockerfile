# Build the manager binary
ARG FROM=alpine
FROM $FROM AS cni
ARG GOARCH=amd64
ARG CNI_PLUGINS_VERSION=v1.1.1
RUN apk add --no-cache curl && \
    curl -Lo cni.tar.gz https://github.com/containernetworking/plugins/releases/download/$CNI_PLUGINS_VERSION/cni-plugins-linux-$GOARCH-$CNI_PLUGINS_VERSION.tgz && \
    tar -xf cni.tar.gz
    
FROM golang:1.19.0 as builder

ARG VERSION=0.6.0
ARG TARGETOS
ARG TARGETARCH

WORKDIR /workspace

RUN curl -sSL https://github.com/squat/kilo/archive/refs/tags/${VERSION}.tar.gz | tar -xzvf- --strip=1

COPY patches /patches
RUN git apply /patches/*.diff


RUN set -eux; \
    GOOS=$TARGETOS GOARCH=$TARGETARCH CGO_ENABLED=0 \
      go build -mod=vendor \
      -ldflags "-X github.com/squat/kilo/pkg/version.Version=$VERSION" \
      -o /out/kg \
      ./cmd/kg/... ; \
    GOOS=$TARGETOS GOARCH=$TARGETARCH CGO_ENABLED=0 \
      go build -mod=vendor \
      -ldflags "-X github.com/squat/kilo/pkg/version.Version=$VERSION" \
      -o /out/kgctl \
      ./cmd/kgctl/...

FROM alpine:3.20
ARG GOARCH
ARG ALPINE_VERSION=v3.20
LABEL maintainer="squat <lserven@gmail.com>"
RUN echo -e "https://alpine.global.ssl.fastly.net/alpine/$ALPINE_VERSION/main\nhttps://alpine.global.ssl.fastly.net/alpine/$ALPINE_VERSION/community" > /etc/apk/repositories && \
    apk add --no-cache ipset iptables ip6tables graphviz font-noto
COPY --from=cni bridge host-local loopback portmap /opt/cni/bin/
ADD https://raw.githubusercontent.com/kubernetes-sigs/iptables-wrappers/e139a115350974aac8a82ec4b815d2845f86997e/iptables-wrapper-installer.sh /
RUN chmod 700 /iptables-wrapper-installer.sh && /iptables-wrapper-installer.sh --no-sanity-check
COPY --from=builder /out/kg /opt/bin/
COPY --from=builder /out/kgctl /opt/bin/
ENTRYPOINT ["/opt/bin/kg"]