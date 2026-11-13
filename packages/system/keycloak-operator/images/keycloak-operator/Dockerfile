# syntax=docker/dockerfile:1.2@sha256:e2a8561e419ab1ba6b2fe6cbdf49fd92b95912df1cf7d313c3e2230a333fdbcc

FROM alpine@sha256:294b683cb724975bec92580e1e685676bd4b50bda910ddb8c51d4cabeaec77e6 AS source
ARG COMMIT_REF=facbc36
RUN apk add --no-cache curl tar git
WORKDIR /src

RUN curl -sSL https://github.com/epam/edp-keycloak-operator/archive/${COMMIT_REF}.tar.gz \
        | tar -xz --strip-components=1

COPY patches /patches
RUN git apply /patches/*.diff

FROM --platform=$BUILDPLATFORM docker.io/golang:1.27@sha256:e432b43af23a9328d56a7c499be0476810aa344acbcf65fc7c455d4ff5a40602 AS builder
ARG GOPROXY
ARG TARGETOS
ARG TARGETARCH

WORKDIR /go/src/keycloak-operator

COPY --from=source /src/go.mod /src/go.sum ./
RUN for attempt in 1 2 3; do go mod download && break; [ "$attempt" = 3 ] && exit 1; sleep $((attempt * 10)); done

COPY --from=source /src .

COPY --from=source /patches /patches

# go build never compiles a _test.go, so nothing else runs the tests of the
# packages the patches touch. Each package is tested from its own directory:
# that finds the module it belongs to, and an empty list runs nothing.
# The list is read into a variable first: a for loop drops the exit status
# of a command substitution, so a patch git cannot read would leave the list
# empty and the step green.
RUN files=$(git apply --numstat /patches/*.diff) || exit 1; \
    for dir in $(printf '%s\n' "$files" | cut -f3 | grep '\.go$' | xargs -r -n1 dirname | sort -u); do \
      (cd "$dir" && go test .) || exit 1; \
    done

RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build -o /build/manager ./cmd

FROM gcr.io/distroless/static:nonroot@sha256:e2e927ec666bae08560abb3c55d0659eceabb657f56b6782ab500a9fc7f555e3
COPY --from=builder /build/manager /manager
USER 65532:65532
ENTRYPOINT ["/manager"]
