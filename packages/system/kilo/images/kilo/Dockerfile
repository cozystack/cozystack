# Build the kg binary
FROM --platform=$BUILDPLATFORM golang:1.27@sha256:e0174e51e81218523251d85d248a90d24c3d5e81543b4f07a5d66229397db190 AS builder

ARG VERSION=944dff5745d747c06555a964c2137b9a986ff6ec
ARG TARGETOS
ARG TARGETARCH

WORKDIR /workspace

RUN curl -sSL https://github.com/squat/kilo/archive/${VERSION}.tar.gz | tar -xzvf- --strip=1

COPY patches /patches
RUN git apply /patches/*.diff

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

RUN GOOS=$TARGETOS GOARCH=$TARGETARCH CGO_ENABLED=0 go build \
    -trimpath \
    -ldflags "-X github.com/squat/kilo/pkg/version.Version=${VERSION}" \
    -a -o kg ./cmd/kg

RUN GOOS=$TARGETOS GOARCH=$TARGETARCH CGO_ENABLED=0 go build \
    -trimpath \
    -ldflags "-X github.com/squat/kilo/pkg/version.Version=${VERSION}" \
    -a -o kgctl ./cmd/kgctl

# Download CNI plugins
FROM alpine:3.24@sha256:294b683cb724975bec92580e1e685676bd4b50bda910ddb8c51d4cabeaec77e6 AS cni
ARG TARGETARCH
ARG CNI_PLUGINS_VERSION=v1.9.1
RUN apk add --no-cache curl && \
    curl -Lo cni.tar.gz https://github.com/containernetworking/plugins/releases/download/$CNI_PLUGINS_VERSION/cni-plugins-linux-$TARGETARCH-$CNI_PLUGINS_VERSION.tgz && \
    tar -xf cni.tar.gz

FROM alpine:3.24@sha256:294b683cb724975bec92580e1e685676bd4b50bda910ddb8c51d4cabeaec77e6
RUN apk add --no-cache ipset iptables ip6tables graphviz font-noto
COPY --from=cni bridge host-local loopback portmap /opt/cni/bin/
ADD https://raw.githubusercontent.com/kubernetes-sigs/iptables-wrappers/e139a115350974aac8a82ec4b815d2845f86997e/iptables-wrapper-installer.sh /
RUN chmod 700 /iptables-wrapper-installer.sh && /iptables-wrapper-installer.sh --no-sanity-check
COPY --from=builder /workspace/kg /opt/bin/kg
COPY --from=builder /workspace/kgctl /opt/bin/kgctl

ENTRYPOINT ["/opt/bin/kg"]
