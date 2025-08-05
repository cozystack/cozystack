FROM golang:1.24-alpine as builder

ARG TARGETOS
ARG TARGETARCH

COPY main.go go.mod go.sum /src/
WORKDIR /src

RUN go build -o /token-proxy -ldflags '-extldflags "-static" -w -s' main.go

FROM scratch

COPY --from=builder /token-proxy /token-proxy

ENTRYPOINT ["/token-proxy"]
