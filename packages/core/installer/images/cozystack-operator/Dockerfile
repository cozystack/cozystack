FROM --platform=$BUILDPLATFORM golang:1.27-alpine@sha256:f92b6ef800e499660581efdabdf25d9d817a9d124eaf900924f0504e7e27e12d AS builder
ARG GOPROXY

ARG TARGETOS
ARG TARGETARCH
ARG VERSION=dev

RUN apk add --no-cache make git

COPY . /src/
WORKDIR /src

RUN for attempt in 1 2 3; do go mod download && break; [ "$attempt" = 3 ] && exit 1; sleep $((attempt * 10)); done

# Build cozystack-operator
RUN CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} go build \
  -ldflags="-w -s -X github.com/cozystack/cozystack/pkg/version.Version=${VERSION}" \
  -o /cozystack-operator \
  ./cmd/cozystack-operator

FROM alpine:3.24@sha256:294b683cb724975bec92580e1e685676bd4b50bda910ddb8c51d4cabeaec77e6

COPY --from=builder /cozystack-operator /usr/bin/cozystack-operator

ENTRYPOINT ["/usr/bin/cozystack-operator"]
