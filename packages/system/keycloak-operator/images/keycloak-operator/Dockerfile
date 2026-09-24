# syntax=docker/dockerfile:1.2@sha256:e2a8561e419ab1ba6b2fe6cbdf49fd92b95912df1cf7d313c3e2230a333fdbcc

FROM alpine@sha256:294b683cb724975bec92580e1e685676bd4b50bda910ddb8c51d4cabeaec77e6 AS source
ARG COMMIT_REF=facbc36
RUN apk add --no-cache curl tar git
WORKDIR /src

RUN curl -sSL https://github.com/epam/edp-keycloak-operator/archive/${COMMIT_REF}.tar.gz \
        | tar -xz --strip-components=1

COPY patches /patches
RUN git apply /patches/*.diff

FROM --platform=$BUILDPLATFORM docker.io/golang:1.26@sha256:6c2a5538f964f1c82f97ad14988bf05de100d922d159d0e398b54c7b0ca0c6c9 AS builder
ARG TARGETOS
ARG TARGETARCH

WORKDIR /go/src/keycloak-operator

COPY --from=source /src/go.mod /src/go.sum ./
RUN go mod download

COPY --from=source /src .
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build -o /build/manager ./cmd

FROM gcr.io/distroless/static:nonroot@sha256:e2e927ec666bae08560abb3c55d0659eceabb657f56b6782ab500a9fc7f555e3
COPY --from=builder /build/manager /manager
USER 65532:65532
ENTRYPOINT ["/manager"]
