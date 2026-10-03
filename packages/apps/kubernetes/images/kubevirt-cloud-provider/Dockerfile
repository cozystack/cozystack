# Source: https://github.com/kubevirt/cloud-provider-kubevirt/blob/main/build/images/kubevirt-cloud-controller-manager/Dockerfile
FROM --platform=$BUILDPLATFORM golang:1.27@sha256:e0174e51e81218523251d85d248a90d24c3d5e81543b4f07a5d66229397db190 AS builder

ARG TARGETOS
ARG TARGETARCH
ENV GOOS=$TARGETOS
ENV GOARCH=$TARGETARCH

RUN git clone https://github.com/kubevirt/cloud-provider-kubevirt /go/src/kubevirt.io/cloud-provider-kubevirt \
 && cd /go/src/kubevirt.io/cloud-provider-kubevirt \
 && git checkout a0acf33

WORKDIR /go/src/kubevirt.io/cloud-provider-kubevirt

# see: https://github.com/kubevirt/cloud-provider-kubevirt/pull/335
ADD patches /patches
RUN git apply /patches/*.diff
RUN go get 'k8s.io/endpointslice/util@v0.28' 'k8s.io/apiserver@v0.28'
RUN go mod tidy
RUN go mod vendor

# go build never compiles a _test.go, so nothing else runs the tests of the
# packages the patches touch. Each package is tested from its own directory:
# that finds the module it belongs to, and an empty list runs nothing.
# The list is read into a variable first: a for loop drops the exit status
# of a command substitution, so a patch git cannot read would leave the list
# empty and the step green.
# The stage sets GOOS and GOARCH to the target for the whole stage, so the
# tests drop them: a test binary built for another architecture cannot run
# on the build host without emulation.
RUN files=$(git apply --numstat /patches/*.diff) || exit 1; \
    for dir in $(printf '%s\n' "$files" | cut -f3 | grep '\.go$' | xargs -r -n1 dirname | sort -u); do \
      (cd "$dir" && env -u GOOS -u GOARCH go test -mod=vendor .) || exit 1; \
    done

RUN CGO_ENABLED=0 go build -mod=vendor -ldflags="-s -w" -o bin/kubevirt-cloud-controller-manager ./cmd/kubevirt-cloud-controller-manager

FROM scratch
COPY --from=builder /go/src/kubevirt.io/cloud-provider-kubevirt/bin/kubevirt-cloud-controller-manager /bin/kubevirt-cloud-controller-manager
ENTRYPOINT [ "/bin/kubevirt-cloud-controller-manager" ]
