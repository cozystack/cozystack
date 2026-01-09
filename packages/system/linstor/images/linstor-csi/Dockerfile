FROM golang:1.25 AS builder

ARG VERSION=v1.10.5
ARG LINSTOR_WAIT_UNTIL_VERSION=v0.3.1
ARG TARGETARCH
ARG TARGETOS

WORKDIR /src

RUN curl -sSL https://github.com/piraeusdatastore/linstor-csi/archive/refs/tags/${VERSION}.tar.gz | tar -xzvf- --strip=1

COPY patches /patches
RUN git apply /patches/*.diff

RUN go mod download

RUN GOOS=$TARGETOS GOARCH=$TARGETARCH CGO_ENABLED=0 \
    go build \
    -a \
    -ldflags "-X github.com/piraeusdatastore/linstor-csi/pkg/driver.Version=$VERSION -extldflags -static" \
    -o /linstor-csi \
    ./cmd/linstor-csi/linstor-csi.go

RUN curl -fsSL https://github.com/LINBIT/linstor-wait-until/releases/download/$LINSTOR_WAIT_UNTIL_VERSION/linstor-wait-until-$LINSTOR_WAIT_UNTIL_VERSION-$TARGETOS-$TARGETARCH.tar.gz | tar xvzC /

FROM debian:trixie-slim

RUN apt-get update && apt-get install -y --no-install-recommends \
      xfsprogs e2fsprogs nfs-common \
      && apt-get clean && rm -rf /var/lib/apt/lists/* \
      && ln -sf /proc/mounts /etc/mtab

COPY --from=builder /linstor-csi /
COPY --from=builder /linstor-wait-until /linstor-wait-until

ENTRYPOINT ["/linstor-csi"]
