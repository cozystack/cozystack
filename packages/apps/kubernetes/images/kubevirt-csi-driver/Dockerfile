ARG builder_image=docker.io/library/golang:1.26@sha256:45a5f7a810238aabcbad211d70b9ae082022d96f7c7259e94041ad1b933575ac
FROM ${builder_image} AS builder

ARG TARGETOS
ARG TARGETARCH

WORKDIR /src

COPY go.mod go.sum ./
RUN go mod download

COPY *.go ./
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build \
    -ldflags "-X kubevirt.io/csi-driver/pkg/service.VendorVersion=0.2.0" \
    -o kubevirt-csi-driver .

FROM docker.io/library/almalinux:9@sha256:d2515c769e7b73f95c4fde38c0a505336ff38f14990c0b7253b77060a049a743

RUN dnf install -y e2fsprogs xfsprogs nfs-utils && dnf clean all

COPY --from=builder /src/kubevirt-csi-driver .

ENTRYPOINT ["./kubevirt-csi-driver"]
