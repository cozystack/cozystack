FROM alpine:3.24@sha256:28bd5fe8b56d1bd048e5babf5b10710ebe0bae67db86916198a6eec434943f8b AS plugins

RUN apk add --no-cache curl

# Grafana plugins mirrored from the official catalog
# (https://grafana.com/grafana/plugins) at build time and served next to the
# dashboards, so Grafana pods install them from this in-cluster server instead
# of downloading from the internet at startup — required for air-gapped and
# egress-restricted installs. Keep ids and versions in sync with
# GF_INSTALL_PLUGINS in packages/system/monitoring/templates/grafana/grafana.yaml.
RUN mkdir -p /plugins \
 && for p in \
      victoriametrics-logs-datasource:0.29.0 \
      natel-discrete-panel:0.1.1 \
      grafana-worldmap-panel:1.0.6 \
      marcusolsson-dynamictext-panel:6.2.0 \
    ; do \
      id="${p%%:*}"; ver="${p##*:}"; \
      curl -fsSL -o "/plugins/${id}-${ver}.zip" \
        "https://grafana.com/api/plugins/${id}/versions/${ver}/download" \
        || exit 1; \
    done

FROM alpine:3.24@sha256:28bd5fe8b56d1bd048e5babf5b10710ebe0bae67db86916198a6eec434943f8b

RUN apk add --no-cache darkhttpd

COPY dashboards /var/www/dashboards
COPY --from=plugins /plugins /var/www/dashboards/plugins

WORKDIR /var/www

EXPOSE 8080

CMD ["darkhttpd", "/var/www/dashboards", "--port", "8080", "--addr", "0.0.0.0"]
