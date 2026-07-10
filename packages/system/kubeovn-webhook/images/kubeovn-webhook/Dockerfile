FROM golang:1.26@sha256:079e59808d2d252516e27e3f3a9c003740dee7f75e55aa71528766d52bcfc16a AS builder

ARG TARGETOS
ARG TARGETARCH

WORKDIR /app
COPY go.mod go.sum ./
RUN GOOS=$TARGETOS GOARCH=$TARGETARCH go mod download

COPY . .
RUN GOOS=$TARGETOS GOARCH=$TARGETARCH CGO_ENABLED=0 go build -o webhook .

FROM alpine:3.24@sha256:28bd5fe8b56d1bd048e5babf5b10710ebe0bae67db86916198a6eec434943f8b
WORKDIR /app

COPY --from=builder /app/webhook /app/webhook

EXPOSE 8443
USER 65534
ENTRYPOINT ["/app/webhook", "--tls-cert-file=/etc/webhook/certs/tls.crt", "--tls-key-file=/etc/webhook/certs/tls.key"]
