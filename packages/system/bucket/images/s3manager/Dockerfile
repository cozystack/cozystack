# Source: https://github.com/cloudlena/s3manager/blob/main/Dockerfile

FROM docker.io/library/golang:1.26@sha256:6c2a5538f964f1c82f97ad14988bf05de100d922d159d0e398b54c7b0ca0c6c9 AS builder

ARG TARGETOS
ARG TARGETARCH

WORKDIR /usr/src/app
RUN wget -O- https://github.com/cloudlena/s3manager/archive/9a7c8e446b422f8973b8c461990f39fdafee9c27.tar.gz | tar -xzf- --strip 1
ADD cozystack.patch /
RUN git apply /cozystack.patch
RUN go mod tidy
RUN GOOS=$TARGETOS GOARCH=$TARGETARCH CGO_ENABLED=0 go build -ldflags="-s -w" -a -installsuffix cgo -o bin/s3manager

FROM docker.io/library/alpine:3.24@sha256:294b683cb724975bec92580e1e685676bd4b50bda910ddb8c51d4cabeaec77e6
WORKDIR /usr/src/app
RUN addgroup -S s3manager && adduser -S s3manager -G s3manager
RUN apk add --no-cache \
  ca-certificates \
  dumb-init
COPY --from=builder --chown=s3manager:s3manager /usr/src/app/bin/s3manager ./
USER s3manager
EXPOSE 8080
ENTRYPOINT [ "/usr/bin/dumb-init", "--" ]
CMD [ "/usr/src/app/s3manager" ]
