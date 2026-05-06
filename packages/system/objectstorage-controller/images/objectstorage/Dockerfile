# syntax=docker/dockerfile:1.2

FROM alpine AS source
ARG COMMIT_REF=v0.2.2
RUN apk add --no-cache curl tar git
WORKDIR /src

RUN curl -sSL https://github.com/kubernetes-sigs/container-object-storage-interface/archive/${COMMIT_REF}.tar.gz \
        | tar -xz --strip-components=1

COPY patches /patches
RUN git apply /patches/*.diff

FROM --platform=$BUILDPLATFORM docker.io/golang:1.24 AS builder
ARG TARGETOS
ARG TARGETARCH

WORKDIR /go/src/cosi

COPY --from=source /src/go.mod /src/go.sum /src ./
RUN go mod download

RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build -o /build/controller ./controller/cmd \
 && CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build -o /build/sidecar ./sidecar/cmd

FROM gcr.io/distroless/static:nonroot AS controller
COPY --from=builder /build/controller /controller
USER 65532:65532
ENTRYPOINT ["/controller"]

FROM gcr.io/distroless/static:nonroot AS sidecar
COPY --from=builder /build/sidecar /sidecar
USER 65532:65532
ENTRYPOINT ["/sidecar"]
