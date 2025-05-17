#https://github.com/deckhouse/deckhouse/blob/main/modules/340-monitoring-kubernetes-control-plane/monitoring/grafana-dashboards/kubernetes-cluster/control-plane-status.json
base=https://github.com/deckhouse/deckhouse/raw/main/
dir="dashboards"
mkdir -p "$dir"



add_ds_prometheus(){
  jq '.templating.list |= [{"current":{"selected":false,"text":"default","value":"default"},"description":null,"error":null,"hide":0,"includeAll":false,"label":"datasource","multi":false,"name":"ds_prometheus","options":[],"query":"prometheus","refresh":1,"regex":"","skipUrlSync":false,"type":"datasource"}] + .'
}

indent() {
  sed "s/^/$(head -c "$1" < /dev/zero | tr '\0' ' ')/"
}

fix_d8() {
  sed \
    -e 's|$__interval_sx3|$__rate_interval|g' \
    -e 's|$__interval_sx4|$__rate_interval|g' \
    -e 's|P0D6E4079E36703EB|$ds_prometheus|g'
}

swap_pvc_overview() {
 jq '(.panels[] | select(.title=="PVC Detailed") | .panels[] | select(.title=="Overview")) as $a | del(.panels[] | select(.title=="PVC Detailed").panels[] | select(.title=="Overview")) | ( (.panels[] | select(.title=="PVC Detailed"))) as $b | del( .panels[] | select(.title=="PVC Detailed")) | (.panels[.panels|length]=($a|.gridPos.y=$b.gridPos.y)) | (.panels[.panels|length]=($b|.gridPos.y=$a.gridPos.y))'
}

deprectaed_remove_faq() {
  jq 'del(.panels[] | select(.title == "How to find who sends requests to deprecated APIs"))| (.panels[]|select(.type == "text")).gridPos.x = 12 | (.panels[]|select(.type == "text")).gridPos.w = 12'
}

while read url others; do
  name="$(basename "$url" .json | tr _ -)"
  case $url in
  modules/042-kube-dns/*)
    ddir=control-plane
    ;;
  modules/402-ingress-nginx/*)
    ddir=ingress
    ;;
  modules/340-monitoring-kubernetes/*)
    ddir=main
    ;;
  modules/340-monitoring-kubernetes-control-plane/*)
    ddir=control-plane
    ;;
  esac
  file="$dir/$ddir/$name.json"
  echo "$file"
  mkdir -p "$dir/$ddir"
  curl -sSL "$base$url" -o "$file.tmp"

  case "$name" in
    "deprecated-resources")
      filters="add_ds_prometheus | deprectaed_remove_faq | fix_d8"
      ;;
    *)
      filters="fix_d8"
      ;;
  esac

  cat "$file.tmp" | eval "$filters" > "$file"
done <<\EOT
modules/042-kube-dns/monitoring/grafana-dashboards/kubernetes-cluster/dns/dns-coredns.json
modules/402-ingress-nginx/monitoring/grafana-dashboards/kubernetes-cluster/controllers.json
modules/402-ingress-nginx/monitoring/grafana-dashboards/kubernetes-cluster/controller-detail.json
modules/402-ingress-nginx/monitoring/grafana-dashboards/ingress-nginx/namespace/namespace_detail.json
modules/402-ingress-nginx/monitoring/grafana-dashboards/ingress-nginx/namespace/namespaces.json
modules/402-ingress-nginx/monitoring/grafana-dashboards/ingress-nginx/vhost/vhost_detail.json
modules/402-ingress-nginx/monitoring/grafana-dashboards/ingress-nginx/vhost/vhosts.json
modules/340-monitoring-kubernetes-control-plane/monitoring/grafana-dashboards/kubernetes-cluster/control-plane-status.json
modules/340-monitoring-kubernetes-control-plane/monitoring/grafana-dashboards/kubernetes-cluster/kube-etcd.json                #TODO
modules/340-monitoring-kubernetes-control-plane/monitoring/grafana-dashboards/kubernetes-cluster/deprecated-resources.json
modules/340-monitoring-kubernetes/monitoring/grafana-dashboards//kubernetes-cluster/nodes/ntp.json                              #TODO
modules/340-monitoring-kubernetes/monitoring/grafana-dashboards//kubernetes-cluster/nodes/nodes.json
modules/340-monitoring-kubernetes/monitoring/grafana-dashboards//kubernetes-cluster/nodes/node.json
modules/340-monitoring-kubernetes/monitoring/grafana-dashboards//main/controller.json
modules/340-monitoring-kubernetes/monitoring/grafana-dashboards//main/pod.json
modules/340-monitoring-kubernetes/monitoring/grafana-dashboards//main/namespace/namespaces.json
modules/340-monitoring-kubernetes/monitoring/grafana-dashboards//main/namespace/namespace.json
modules/340-monitoring-kubernetes/monitoring/grafana-dashboards//main/capacity-planning/capacity-planning.json
modules/340-monitoring-kubernetes/monitoring/grafana-dashboards//flux/flux-control-plane.json
modules/340-monitoring-kubernetes/monitoring/grafana-dashboards//flux/flux-stats.json
modules/340-monitoring-kubernetes/monitoring/grafana-dashboards//kafka/strimzi-kafka.json
modules/340-monitoring-kubernetes/monitoring/grafana-dashboards//goldpinger/goldpinger.json
EOT



while read url others; do
  name="$(basename "$url" .json | tr _ -)"
  case $url in
  *VictoriaMetrics*)
    ddir=victoria-metrics
    ;;
  */dotdc/*)
    ddir=dotdc
    ;;
  esac
  file="$dir/$ddir/$name.json"

  mkdir -p "$dir/$ddir"
  echo "$file"
  curl -sSL "$url" -o "$file"
done <<\EOT
	https://raw.githubusercontent.com/VictoriaMetrics/VictoriaMetrics/master/dashboards/victoriametrics.json
	https://raw.githubusercontent.com/VictoriaMetrics/VictoriaMetrics/master/dashboards/vmagent.json
	https://raw.githubusercontent.com/VictoriaMetrics/VictoriaMetrics/master/dashboards/victoriametrics-cluster.json
	https://raw.githubusercontent.com/VictoriaMetrics/VictoriaMetrics/master/dashboards/vmalert.json
	https://raw.githubusercontent.com/VictoriaMetrics/VictoriaMetrics/master/dashboards/operator.json
	https://raw.githubusercontent.com/VictoriaMetrics/VictoriaMetrics/master/dashboards/backupmanager.json
	https://raw.githubusercontent.com/dotdc/grafana-dashboards-kubernetes/master/dashboards/k8s-system-coredns.json
	https://raw.githubusercontent.com/dotdc/grafana-dashboards-kubernetes/master/dashboards/k8s-views-global.json
	https://raw.githubusercontent.com/dotdc/grafana-dashboards-kubernetes/master/dashboards/k8s-views-namespaces.json
	https://raw.githubusercontent.com/dotdc/grafana-dashboards-kubernetes/master/dashboards/k8s-views-pods.json
EOT
