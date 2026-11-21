FROM --platform=$BUILDPLATFORM golang:1.27@sha256:e432b43af23a9328d56a7c499be0476810aa344acbcf65fc7c455d4ff5a40602 AS builder
ARG GOPROXY

ARG VERSION=v3.3.5

ARG TARGETOS
ARG TARGETARCH

WORKDIR /workspace

RUN curl -sSL https://github.com/freshworks-oss/redis-operator/archive/refs/tags/${VERSION}.tar.gz | tar -xzvf- --strip=1

# The carried TLS patch is source only: its CRD half ships as the chart
# patch, so the tarball's CRD and chart copies stay at v3.3.5 and the tests
# that read them would fail instead of skipping.
RUN rm -rf manifests charts example .github

COPY patches /patches
RUN git apply /patches/*.diff

# go build never compiles a _test.go, so nothing else runs the tests of the
# packages the patches touch. Each package is tested from its own directory:
# that finds the module it belongs to, and an empty list runs nothing.
# The list is read into a variable first: a for loop drops the exit status
# of a command substitution, so a patch git cannot read would leave the list
# empty and the step green.
# Upstream's integration tests sit behind the integration build tag and need a
# cluster, so go test compiles none of them; go vet with the tag type-checks
# them in whichever of these directories they live.
RUN files=$(git apply --numstat /patches/*.diff) || exit 1; \
    for dir in $(printf '%s\n' "$files" | cut -f3 | grep '\.go$' | xargs -r -n1 dirname | sort -u); do \
      (cd "$dir" && go test . && go vet -tags integration .) || exit 1; \
    done

RUN GOOS=$TARGETOS GOARCH=$TARGETARCH VERSION=$VERSION ./scripts/build.sh

FROM alpine:3.24@sha256:294b683cb724975bec92580e1e685676bd4b50bda910ddb8c51d4cabeaec77e6
RUN apk --no-cache add \
  ca-certificates
COPY --from=builder /workspace/bin/redis-operator /usr/local/bin
RUN addgroup -g 1000 rf && \
  adduser -D -u 1000 -G rf rf && \
  chown rf:rf /usr/local/bin/redis-operator
USER rf

ENTRYPOINT ["/usr/local/bin/redis-operator"]

