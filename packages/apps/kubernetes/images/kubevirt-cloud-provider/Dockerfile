# Source: https://github.com/kubevirt/cloud-provider-kubevirt/blob/main/build/images/kubevirt-cloud-controller-manager/Dockerfile
FROM golang:1.26@sha256:3aff6657219a4d9c14e27fb1d8976c49c29fddb70ba835014f477e1c70636647 AS builder

ARG TARGETOS
ARG TARGETARCH
ENV GOOS=$TARGETOS
ENV GOARCH=$TARGETARCH

RUN git clone https://github.com/kubevirt/cloud-provider-kubevirt /go/src/kubevirt.io/cloud-provider-kubevirt \
 && cd /go/src/kubevirt.io/cloud-provider-kubevirt \
 && git checkout a0acf33

WORKDIR /go/src/kubevirt.io/cloud-provider-kubevirt

# see: https://github.com/kubevirt/cloud-provider-kubevirt/pull/335
ADD patches /patches
RUN git apply /patches/*.diff
RUN go get 'k8s.io/endpointslice/util@v0.28' 'k8s.io/apiserver@v0.28'
RUN go mod tidy
RUN go mod vendor

RUN CGO_ENABLED=0 go build -mod=vendor -ldflags="-s -w" -o bin/kubevirt-cloud-controller-manager ./cmd/kubevirt-cloud-controller-manager

FROM scratch
COPY --from=builder /go/src/kubevirt.io/cloud-provider-kubevirt/bin/kubevirt-cloud-controller-manager /bin/kubevirt-cloud-controller-manager
ENTRYPOINT [ "/bin/kubevirt-cloud-controller-manager" ]
