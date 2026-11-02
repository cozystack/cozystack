# Builds the clastix/cluster-api-control-plane-provider-kamaji manager
# binary from a tagged upstream release with patches/ applied.
#
# Upstream serves only v1alpha2 since v0.20.0 (CAPI v1beta2 contract) and
# keeps the v1alpha1 conversion webhook for its commercial channel; the
# community PR was closed with an invitation to carry it in our own build:
# https://github.com/clastix/cluster-api-control-plane-provider-kamaji/pull/339#issuecomment-6357975126
# patches/0001-v1alpha1-conversion-webhook.diff is that PR, made lossless
# (fields v1alpha1 lacks survive in the cluster.x-k8s.io/conversion-data
# annotation) and with our v0.19.0 additionalServicePorts backport kept in
# the v1alpha1 schema, so every KamajiControlPlane stored by
# v0.19.0-cozystack.0 and the v1alpha1 manifests the kubernetes chart renders
# keep working. Drop the patch once nothing reads or writes v1alpha1 and the
# CRD status.storedVersions no longer lists it.
FROM --platform=$BUILDPLATFORM golang:1.27@sha256:3680233e3204827fbdc66088528ae6d4b3d034f51d03a99d454f6de034888244 AS builder
ARG GOPROXY

# Upstream release tag. Bump together with patches/ when refreshing.
ARG VERSION=v0.21.0
ARG TARGETOS
ARG TARGETARCH

WORKDIR /workspace

RUN curl -sSL "https://github.com/clastix/cluster-api-control-plane-provider-kamaji/archive/refs/tags/${VERSION}.tar.gz" \
    | tar -xzf - --strip=1

COPY patches /patches
RUN git apply --recount /patches/*.diff

# go build never compiles a _test.go, so nothing else runs the tests of the
# packages the patches touch. Each package is tested from its own directory:
# that finds the module it belongs to, and an empty list runs nothing.
# The list is read into a variable first: a for loop drops the exit status
# of a command substitution, so a patch git cannot read would leave the list
# empty and the step green.
RUN files=$(git apply --numstat --recount /patches/*.diff) || exit 1; \
    for dir in $(printf '%s\n' "$files" | cut -f3 | grep '\.go$' | xargs -r -n1 dirname | sort -u); do \
      (cd "$dir" && go test .) || exit 1; \
    done

RUN CGO_ENABLED=0 GOOS=${TARGETOS:-linux} GOARCH=${TARGETARCH} \
    go build -a -o manager main.go

FROM gcr.io/distroless/static:nonroot@sha256:e2e927ec666bae08560abb3c55d0659eceabb657f56b6782ab500a9fc7f555e3
WORKDIR /
COPY --from=builder /workspace/manager /manager
USER 65532:65532

ENTRYPOINT ["/manager"]
