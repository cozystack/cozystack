FROM golang:1.26-alpine@sha256:8ac98ca534ac3f51e1f420a1dd2c15e74c75cfa0f23f3ad27eb5d7236c349a0c AS builder

ARG TARGETOS
ARG TARGETARCH
ARG VERSION=dev

RUN apk add --no-cache make git

COPY . /src/
WORKDIR /src

RUN go mod download

# Build cozystack-operator
RUN CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} go build \
  -ldflags="-w -s -X github.com/cozystack/cozystack/pkg/version.Version=${VERSION}" \
  -o /cozystack-operator \
  ./cmd/cozystack-operator

FROM alpine:3.24@sha256:294b683cb724975bec92580e1e685676bd4b50bda910ddb8c51d4cabeaec77e6

COPY --from=builder /cozystack-operator /usr/bin/cozystack-operator

ENTRYPOINT ["/usr/bin/cozystack-operator"]
