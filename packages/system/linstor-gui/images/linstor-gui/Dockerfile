# Upstream: https://github.com/LINBIT/linstor-gui (GPL-3.0)
# Serves the pre-built LINSTOR GUI tarball published by LINBIT from a hardened
# nginx-unprivileged image. nginx.conf is supplied by the chart via ConfigMap,
# so the upstream docker-entrypoint.sh / nginx.conf.template is not used.
FROM nginxinc/nginx-unprivileged:1.29-alpine

ARG LINSTOR_GUI_VERSION
USER root
RUN apk add --no-cache curl tar && \
    curl -fsSL "https://pkg.linbit.com/downloads/linstor/linstor-gui-${LINSTOR_GUI_VERSION}.tar.gz" -o /tmp/linstor-gui.tar.gz && \
    mkdir -p /usr/share/nginx/html && \
    tar -xzf /tmp/linstor-gui.tar.gz -C /usr/share/nginx/html --strip-components=2 "linstor-gui-${LINSTOR_GUI_VERSION}/dist" && \
    rm -f /tmp/linstor-gui.tar.gz && \
    apk del curl tar && \
    chown -R 101:101 /usr/share/nginx/html

USER 101
EXPOSE 3373
CMD ["nginx", "-g", "daemon off;"]
