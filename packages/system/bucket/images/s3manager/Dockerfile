# Source: https://github.com/cloudlena/s3manager/blob/main/Dockerfile

FROM --platform=$BUILDPLATFORM docker.io/library/golang:1.27@sha256:e0174e51e81218523251d85d248a90d24c3d5e81543b4f07a5d66229397db190 AS builder

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
# The list is read into a variable first: a for loop drops the exit status
# of a command substitution, so a patch git cannot read would leave the list
# empty and the step green.
RUN files=$(git apply --numstat /cozystack.patch) || exit 1; \
    for dir in $(printf '%s\n' "$files" | cut -f3 | grep '\.go$' | xargs -r -n1 dirname | sort -u); do \
      (cd "$dir" && go test .) || exit 1; \
    done

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
