FROM grafana/grafana:11.6.16@sha256:d67af92050b8d93b393dc741864752a69c9da1ffa39c1bb9af49ad5d9e47d2c3

USER root

RUN mkdir -p /var/lib/grafana-plugins \
 && chown -R grafana:0 /var/lib/grafana-plugins

USER grafana

ARG VLOGS_VERSION=v0.28.0
RUN curl -L https://github.com/VictoriaMetrics/victorialogs-datasource/releases/download/${VLOGS_VERSION}/victoriametrics-logs-datasource-${VLOGS_VERSION}.tar.gz | \
      tar -xzf - -C /var/lib/grafana-plugins

RUN grafana-cli --pluginsDir /var/lib/grafana-plugins plugins install natel-discrete-panel
RUN grafana-cli --pluginsDir /var/lib/grafana-plugins plugins install grafana-worldmap-panel
RUN grafana-cli --pluginsDir /var/lib/grafana-plugins plugins install marcusolsson-dynamictext-panel
