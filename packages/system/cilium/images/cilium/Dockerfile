ARG VERSION=v1.19.5

FROM quay.io/cilium/cilium:${VERSION} AS upstream

# The upstream runtime image ships no `patch`; apply the BPF patch in a
# throwaway alpine stage and copy the result back over the upstream image.
# The agent compiles BPF at startup from /var/lib/cilium/bpf, so replacing
# the source file is sufficient.
FROM docker.io/library/alpine:3.22 AS patcher
RUN apk add --no-cache patch
WORKDIR /work
COPY --from=upstream /var/lib/cilium/bpf/bpf_host.c ./bpf_host.c
COPY patches/pass-ipv6-to-stack-when-ipv6-datapath-disabled.diff /patches/
# --fuzz=0: any upstream drift in the patched context must fail the build, loudly.
RUN patch --batch --forward --fuzz=0 -p1 < /patches/pass-ipv6-to-stack-when-ipv6-datapath-disabled.diff || { \
      echo 'ERROR: the IPv6 passthrough patch no longer applies to bpf/bpf_host.c.' >&2; \
      echo 'Upstream cilium changed the host-firewall protocol switches (version bump?).' >&2; \
      echo 'Regenerate packages/system/cilium/images/cilium/patches/*.diff against the new tag.' >&2; \
      exit 1; }
RUN test "$(grep -c 'cozystack-ipv6-passthrough' bpf_host.c)" = 3 || { \
      echo 'ERROR: expected exactly 3 cozystack-ipv6-passthrough markers in bpf_host.c' >&2; \
      exit 1; }

FROM upstream
COPY --from=patcher /work/bpf_host.c /var/lib/cilium/bpf/bpf_host.c
