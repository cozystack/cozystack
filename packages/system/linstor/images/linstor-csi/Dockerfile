FROM --platform=$BUILDPLATFORM golang:1.27@sha256:3680233e3204827fbdc66088528ae6d4b3d034f51d03a99d454f6de034888244 AS builder

ARG VERSION=v1.11.2
ARG LINSTOR_WAIT_UNTIL_VERSION=v0.3.1
ARG TARGETARCH
ARG TARGETOS

WORKDIR /src

RUN curl -sSL https://github.com/piraeusdatastore/linstor-csi/archive/refs/tags/${VERSION}.tar.gz | tar -xzvf- --strip=1

COPY patches /patches
RUN git apply /patches/*.diff

RUN go mod download

# Upstream pins grpc v1.81.0, which uses http2.TrailerPrefix; on Go 1.27
# golang.org/x/net builds http2 on top of net/http and no longer defines it.
# v1.83.2 also carries the fix for GO-2026-6443, a server panic reachable
# through the CSI gRPC endpoint.
RUN go get google.golang.org/grpc@v1.83.2 && \
    go mod tidy

RUN GOOS=$TARGETOS GOARCH=$TARGETARCH CGO_ENABLED=0 \
    go build \
    -a \
    -ldflags "-X github.com/piraeusdatastore/linstor-csi/pkg/driver.Version=$VERSION -extldflags -static" \
    -o /linstor-csi \
    ./cmd/linstor-csi/linstor-csi.go

RUN curl -fsSL https://github.com/LINBIT/linstor-wait-until/releases/download/$LINSTOR_WAIT_UNTIL_VERSION/linstor-wait-until-$LINSTOR_WAIT_UNTIL_VERSION-$TARGETOS-$TARGETARCH.tar.gz | tar xvzC /

FROM debian:trixie-slim@sha256:a99cfc517144bc59b1978475ec53b46ecabec7e43635402ee5b77cc54cd1b20a

RUN apt-get update && apt-get install -y --no-install-recommends \
      xfsprogs e2fsprogs nfs-common \
      && apt-get clean && rm -rf /var/lib/apt/lists/* \
      && ln -sf /proc/mounts /etc/mtab

COPY --from=builder /linstor-csi /
COPY --from=builder /linstor-wait-until /linstor-wait-until

ENTRYPOINT ["/linstor-csi"]
