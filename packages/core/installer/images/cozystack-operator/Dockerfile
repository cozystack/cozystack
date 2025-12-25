FROM golang:1.25-alpine as builder

ARG TARGETOS
ARG TARGETARCH

RUN apk add --no-cache make git

COPY . /src/
WORKDIR /src

RUN go mod download

# Build cozystack-operator
RUN CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} go build \
  -ldflags="-w -s" \
  -o /cozystack-operator \
  ./cmd/cozystack-operator

FROM alpine:3.22

COPY --from=builder /cozystack-operator /usr/bin/cozystack-operator

ENTRYPOINT ["/usr/bin/cozystack-operator"]
