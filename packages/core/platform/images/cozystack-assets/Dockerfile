FROM golang:1.24-alpine AS builder

ARG TARGETOS
ARG TARGETARCH

RUN apk add --no-cache make git
RUN apk add helm --repository=https://dl-cdn.alpinelinux.org/alpine/edge/community

COPY . /src/
WORKDIR /src

RUN go mod download

RUN go build -o /cozystack-assets-server -ldflags '-extldflags "-static" -w -s' ./cmd/cozystack-assets-server

RUN make repos

FROM alpine:3.22

COPY --from=builder /src/_out/repos /cozystack/assets/repos
COPY --from=builder /cozystack-assets-server /usr/bin/cozystack-assets-server
COPY --from=builder /src/dashboards /cozystack/assets/dashboards

WORKDIR /cozystack
ENTRYPOINT ["/usr/bin/cozystack-assets-server"]
