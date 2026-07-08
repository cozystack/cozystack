# Build the kg binary
FROM --platform=$BUILDPLATFORM golang:1.26 AS builder

ARG VERSION=944dff5745d747c06555a964c2137b9a986ff6ec
ARG TARGETOS
ARG TARGETARCH

WORKDIR /workspace

RUN curl -sSL https://github.com/squat/kilo/archive/${VERSION}.tar.gz | tar -xzvf- --strip=1

COPY patches /patches
RUN git apply /patches/*.diff

RUN GOOS=$TARGETOS GOARCH=$TARGETARCH CGO_ENABLED=0 go build \
    -trimpath \
    -ldflags "-X github.com/squat/kilo/pkg/version.Version=${VERSION}" \
    -a -o kg ./cmd/kg

RUN GOOS=$TARGETOS GOARCH=$TARGETARCH CGO_ENABLED=0 go build \
    -trimpath \
    -ldflags "-X github.com/squat/kilo/pkg/version.Version=${VERSION}" \
    -a -o kgctl ./cmd/kgctl

# Download CNI plugins
FROM alpine:3.24 AS cni
ARG TARGETARCH
ARG CNI_PLUGINS_VERSION=v1.9.1
RUN apk add --no-cache curl && \
    curl -Lo cni.tar.gz https://github.com/containernetworking/plugins/releases/download/$CNI_PLUGINS_VERSION/cni-plugins-linux-$TARGETARCH-$CNI_PLUGINS_VERSION.tgz && \
    tar -xf cni.tar.gz

FROM alpine:3.24
RUN apk add --no-cache ipset iptables ip6tables graphviz font-noto
COPY --from=cni bridge host-local loopback portmap /opt/cni/bin/
ADD https://raw.githubusercontent.com/kubernetes-sigs/iptables-wrappers/e139a115350974aac8a82ec4b815d2845f86997e/iptables-wrapper-installer.sh /
RUN chmod 700 /iptables-wrapper-installer.sh && /iptables-wrapper-installer.sh --no-sanity-check
COPY --from=builder /workspace/kg /opt/bin/kg
COPY --from=builder /workspace/kgctl /opt/bin/kgctl

ENTRYPOINT ["/opt/bin/kg"]
