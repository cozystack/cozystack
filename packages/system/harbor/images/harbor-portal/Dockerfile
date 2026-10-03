# The portal half of the Harbor rebuild in images/harbor, kept in its own file
# because its build output is static JS with no architecture. The Makefile
# passes VERSION and COMMIT from images/harbor/Dockerfile so both files build
# one pinned release. Follows upstream's make/photon/portal/Dockerfile{,.base}.
#
# Temporary, like images/harbor: deleted together with it once the vendored
# chart moves to an upstream release that publishes arm64 (expected 2.16).
ARG VERSION
ARG COMMIT

FROM --platform=$BUILDPLATFORM node:16.18.0@sha256:3547c98a330a08fb9dbb8b04ac4430f6d21133cb79f21f6cf8283184f523ff83 AS build
ARG VERSION
ARG COMMIT
RUN git clone --branch ${VERSION} --depth 1 https://github.com/goharbor/harbor /harbor \
    && test "$(git -C /harbor rev-parse HEAD)" = "${COMMIT}"
# A VERSION bump must not build the portal with the old node.
RUN grep -qxF "NODEBUILDIMAGE=node:$(node --version | sed 's/^v//')" /harbor/Makefile
WORKDIR /harbor/src/portal
# The postinstall hook generates the API client from ./swagger.yaml.
RUN cp /harbor/api/v2.0/swagger.yaml swagger.yaml \
    && npm install --unsafe-perm \
    && npm run generate-build-timestamp \
    && node --max_old_space_size=2048 node_modules/@angular/cli/bin/ng build --configuration production
RUN npm install js-yaml@4.1.0 \
    && node -e "const yaml = require('js-yaml'); const fs = require('fs'); fs.writeFileSync('swagger.json', JSON.stringify(yaml.load(fs.readFileSync('swagger.yaml', 'utf8'))));" \
    && cp /harbor/LICENSE dist/
RUN cd app-swagger-ui && npm install --unsafe-perm && npm run build

FROM goharbor/photon:5.0@sha256:512acf2732c2a6a3506b4913e0b5f1bda38023cc4e982c2510a38b469fbc65ee
RUN tdnf install -y nginx shadow >> /dev/null \
    && tdnf clean all \
    && ln -sf /dev/stdout /var/log/nginx/access.log \
    && ln -sf /dev/stderr /var/log/nginx/error.log \
    && groupmod -g 10000 nginx && usermod -g 10000 -u 10000 -d /home/nginx -s /bin/bash nginx \
    && chown -R nginx:nginx /etc/nginx
COPY --from=build /harbor/src/portal/dist /usr/share/nginx/html
COPY --from=build /harbor/src/portal/swagger.json /usr/share/nginx/html
COPY --from=build /harbor/src/portal/app-swagger-ui/dist /usr/share/nginx/html
VOLUME /var/cache/nginx /var/log/nginx /run
STOPSIGNAL SIGQUIT
USER nginx
CMD ["nginx", "-g", "daemon off;"]
