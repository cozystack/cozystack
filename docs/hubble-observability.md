# Enabling Hubble for Network Observability

Hubble is a network and security observability platform built on top of Cilium. It provides deep visibility into the communication and behavior of services in your Kubernetes cluster.

## Prerequisites

- Cozystack platform running with Cilium as the CNI
- Monitoring hub enabled for Grafana access

## Configuration

Hubble is disabled by default in Cozystack. To enable it, update the Cilium configuration.

### Enable Hubble

Edit the Cilium values in your platform configuration to enable Hubble:

```yaml
cilium:
  hubble:
    enabled: true
    relay:
      enabled: true
    ui:
      enabled: true
    metrics:
      enabled:
        - dns
        - drop
        - tcp
        - flow
        - port-distribution
        - icmp
        - httpV2:exemplars=true;labelsContext=source_ip,source_namespace,source_workload,destination_ip,destination_namespace,destination_workload,traffic_direction
```

### Components

When Hubble is enabled, the following components become available:

- **Hubble Relay**: Aggregates flow data from all Cilium agents
- **Hubble UI**: Web-based interface for exploring network flows
- **Hubble Metrics**: Prometheus metrics for network observability

## Grafana Dashboards

Once Hubble is enabled and the monitoring hub is deployed, the following dashboards become available in Grafana under the `hubble` folder:

| Dashboard | Description |
|-----------|-------------|
| **Overview** | General Hubble metrics including processing statistics |
| **DNS Namespace** | DNS query and response metrics by namespace |
| **L7 HTTP Metrics** | HTTP layer 7 metrics by workload |
| **Network Overview** | Network flow overview by namespace |

### Accessing Dashboards

1. Navigate to Grafana via the monitoring hub
2. Browse to the `hubble` folder in the dashboard browser
3. Select a dashboard to view network observability data

## Metrics Available

Hubble exposes various metrics that can be queried in Grafana:

- `hubble_flows_processed_total`: Total number of flows processed
- `hubble_dns_queries_total`: DNS queries by type
- `hubble_dns_responses_total`: DNS responses by status
- `hubble_drop_total`: Dropped packets by reason
- `hubble_tcp_flags_total`: TCP connections by flag
- `hubble_http_requests_total`: HTTP requests by method and status

## Troubleshooting

### Verify Hubble Status

Check if Hubble is running:

```bash
kubectl get pods -n cozy-cilium -l k8s-app=hubble-relay
kubectl get pods -n cozy-cilium -l k8s-app=hubble-ui
```

### Check Metrics Endpoint

Verify Hubble metrics are being scraped:

```bash
kubectl port-forward -n cozy-cilium svc/hubble-metrics 9965:9965
curl http://localhost:9965/metrics
```

### Verify ServiceMonitor

Ensure the ServiceMonitor is created for Prometheus scraping:

```bash
kubectl get servicemonitor -n cozy-cilium
```
