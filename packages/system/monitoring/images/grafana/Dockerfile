FROM grafana/grafana:11.6.15@sha256:f739273dfed04760428fcfbfccd1752b3b7fc87ef0a3d5637e2e87cce151e1a2

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
