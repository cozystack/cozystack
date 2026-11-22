ARG VERSION=v1.20.2@sha256:2939231d0d3e3ebddcd80fffa168b7ddcc78fdf0dc864d1c8c126ff523c54f01

FROM quay.io/cilium/cilium:${VERSION} AS upstream

# The upstream runtime image ships no `patch`; apply the BPF patches in a
# throwaway alpine stage and copy the results back over the upstream image.
# The agent compiles BPF at startup from /var/lib/cilium/bpf, so replacing
# the source files is sufficient.
FROM docker.io/library/alpine:3.24@sha256:294b683cb724975bec92580e1e685676bd4b50bda910ddb8c51d4cabeaec77e6 AS patcher
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
COPY --from=upstream /var/lib/cilium/bpf/lib/nodeport_egress.h ./lib/nodeport_egress.h
COPY patches/skip-snat-conflict-for-local-destinations.diff /patches/
RUN patch --batch --forward --fuzz=0 -p1 < /patches/skip-snat-conflict-for-local-destinations.diff || { \
      echo 'ERROR: the cilium/cilium#49232 backport no longer applies to bpf/lib/nodeport_egress.h.' >&2; \
      echo 'If the new tag already carries cilium/cilium#49232, drop the patch; otherwise regenerate it.' >&2; \
      exit 1; }
RUN test "$(grep -c -e 'See comment in nodeport_has_nat_conflict_ipv4' -e 'Host traffic to a local endpoint never leaves the node' lib/nodeport_egress.h)" = 2 || { \
      echo 'ERROR: expected both cilium/cilium#49232 hunks in nodeport_egress.h' >&2; \
      exit 1; }

FROM upstream
COPY --from=patcher /work/bpf_host.c /var/lib/cilium/bpf/bpf_host.c
COPY --from=patcher /work/lib/nodeport_egress.h /var/lib/cilium/bpf/lib/nodeport_egress.h
