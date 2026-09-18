# Source: https://github.com/cloudlena/s3manager/blob/main/Dockerfile

FROM docker.io/library/golang:1.26@sha256:079e59808d2d252516e27e3f3a9c003740dee7f75e55aa71528766d52bcfc16a AS builder

ARG TARGETOS
ARG TARGETARCH

WORKDIR /usr/src/app
RUN wget -O- https://github.com/cloudlena/s3manager/archive/9a7c8e446b422f8973b8c461990f39fdafee9c27.tar.gz | tar -xzf- --strip 1
ADD cozystack.patch /
RUN git apply /cozystack.patch
RUN go mod tidy

# go build never compiles a _test.go, so nothing else runs the tests of the
# packages the patch touches. Each package is tested from its own directory:
# that finds the module it belongs to, and an empty list runs nothing.
RUN for dir in $(git apply --numstat /cozystack.patch | cut -f3 | grep '\.go$' | xargs -n1 dirname | sort -u); do \
      (cd "$dir" && go test .) || exit 1; \
    done

RUN GOOS=$TARGETOS GOARCH=$TARGETARCH CGO_ENABLED=0 go build -ldflags="-s -w" -a -installsuffix cgo -o bin/s3manager

FROM docker.io/library/alpine:3.24@sha256:28bd5fe8b56d1bd048e5babf5b10710ebe0bae67db86916198a6eec434943f8b
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
