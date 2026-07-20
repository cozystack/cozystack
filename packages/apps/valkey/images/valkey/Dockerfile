# syntax=docker/dockerfile:1
#
# Thin Redis-compatibility layer over the official upstream Valkey image.
#
# Cozystack runs Valkey through the Spotahome redis-operator. The operator
# launches the container with the classic `redis-server` command and — more
# importantly — hardcodes `redis-cli` in its liveness/readiness/shutdown probe
# scripts, which are NOT overridable via the RedisFailover CRD. The upstream
# valkey/valkey image ships only `valkey-*` binaries, so this layer adds the
# `redis-*` compatibility symlinks. That is exactly what the distro
# `valkey-compat` package and Docker's Hardened Valkey image provide.
#
# No Valkey code is compiled here: we only symlink the official upstream
# binaries, keeping the BSD-3-Clause upstream image as the single source.
ARG VALKEY_VERSION
FROM valkey/valkey:${VALKEY_VERSION}

USER root
RUN set -eux; \
    cd /usr/local/bin; \
    for b in server cli sentinel benchmark check-aof check-rdb; do \
        ln -svfT "valkey-$b" "redis-$b"; \
    done; \
    redis-server --version; \
    redis-cli --version
USER valkey
