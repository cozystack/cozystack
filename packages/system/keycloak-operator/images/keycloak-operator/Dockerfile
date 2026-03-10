# syntax=docker/dockerfile:1.2

FROM alpine AS source
ARG COMMIT_REF=facbc36
RUN apk add --no-cache curl tar git
WORKDIR /src

RUN curl -sSL https://github.com/epam/edp-keycloak-operator/archive/${COMMIT_REF}.tar.gz \
        | tar -xz --strip-components=1

COPY patches /patches
RUN git apply /patches/*.diff

FROM --platform=$BUILDPLATFORM docker.io/golang:1.24 AS builder
ARG TARGETOS
ARG TARGETARCH

WORKDIR /go/src/keycloak-operator

COPY --from=source /src/go.mod /src/go.sum ./
RUN go mod download

COPY --from=source /src .
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build -o /build/manager ./cmd

FROM gcr.io/distroless/static:nonroot
COPY --from=builder /build/manager /manager
USER 65532:65532
ENTRYPOINT ["/manager"]
