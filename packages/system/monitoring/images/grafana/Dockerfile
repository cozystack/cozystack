FROM grafana/grafana:11.4.0

USER root

RUN mkdir -p /var/lib/grafana-plugins \
 && chown -R grafana:0 /var/lib/grafana-plugins

USER grafana

ARG VLOGS_VERSION=v0.14.1
RUN curl -L https://github.com/VictoriaMetrics/victorialogs-datasource/releases/download/${VLOGS_VERSION}/victoriametrics-logs-datasource-${VLOGS_VERSION}.tar.gz | \
      tar -xzf - -C /var/lib/grafana-plugins

RUN grafana-cli --pluginsDir /var/lib/grafana-plugins plugins install natel-discrete-panel
RUN grafana-cli --pluginsDir /var/lib/grafana-plugins plugins install grafana-worldmap-panel
