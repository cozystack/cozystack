FROM golang:1.25-alpine as builder

ARG TARGETOS
ARG TARGETARCH

RUN apk add --no-cache make git
RUN apk add helm --repository=https://dl-cdn.alpinelinux.org/alpine/edge/community

COPY . /src/
WORKDIR /src

RUN go mod download

# Build cozystack-operator
RUN CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} go build \
  -ldflags="-w -s" \
  -o /cozystack-operator \
  ./cmd/cozystack-operator

FROM alpine:3.22

RUN wget -O- https://github.com/cozystack/cozypkg/raw/refs/heads/main/hack/install.sh | sh -s -- -v 1.4.0

RUN apk add --no-cache make kubectl helm coreutils git jq ca-certificates

COPY --from=builder /src/scripts /cozystack/scripts
COPY --from=builder /cozystack-operator /usr/bin/cozystack-operator

WORKDIR /cozystack
ENTRYPOINT ["/usr/bin/cozystack-operator"]
