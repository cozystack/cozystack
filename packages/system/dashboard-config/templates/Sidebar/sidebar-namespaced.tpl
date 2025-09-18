{{- define "incloud-web-resources.sidebar.menu.items.namespaced" -}}
- key: main
  label: Main
  children:
  - key: marketplace
    label: Marketplace
    link: /openapi-ui/{clusterName}/{namespace}/factory/marketplace
  - key: infos
    label: Tenant Info
    link: /openapi-ui/{clusterName}/{namespace}/factory/info-details/info

- key: iaas
  label: IaaS
  children:
  - key: virtualmachines
    label: Virtual Machines
    link: /openapi-ui/{clusterName}/{namespace}/api-table/apps.cozystack.io/v1alpha1/virtualmachines
  - key: kuberneteses
    label: Kubernetes Clusters
    link: /openapi-ui/{clusterName}/{namespace}/api-table/apps.cozystack.io/v1alpha1/kuberneteses
  - key: vminstances
    label: VM Instances
    link: /openapi-ui/{clusterName}/{namespace}/api-table/apps.cozystack.io/v1alpha1/vminstances
  - key: vmdisks
    label: VM Disks
    link: /openapi-ui/{clusterName}/{namespace}/api-table/apps.cozystack.io/v1alpha1/vmdisks
  - key: buckets
    label: Buckets
    link: /openapi-ui/{clusterName}/{namespace}/api-table/apps.cozystack.io/v1alpha1/buckets

- key: paas
  label: PaaS
  children:
  - key: clickhouses
    label: ClickHouse
    link: /openapi-ui/{clusterName}/{namespace}/api-table/apps.cozystack.io/v1alpha1/clickhouses
  - key: natses
    label: NATS
    link: /openapi-ui/{clusterName}/{namespace}/api-table/apps.cozystack.io/v1alpha1/natses
  - key: mysqls
    label: MySQL
    link: /openapi-ui/{clusterName}/{namespace}/api-table/apps.cozystack.io/v1alpha1/mysqls
  - key: redises
    label: Redis
    link: /openapi-ui/{clusterName}/{namespace}/api-table/apps.cozystack.io/v1alpha1/redises
  - key: rabbitmqs
    label: RabbitMQ
    link: /openapi-ui/{clusterName}/{namespace}/api-table/apps.cozystack.io/v1alpha1/rabbitmqs
  - key: postgreses
    label: Postgres
    link: /openapi-ui/{clusterName}/{namespace}/api-table/apps.cozystack.io/v1alpha1/postgreses
  - key: ferretdb
    label: FerretDB
    link: /openapi-ui/{clusterName}/{namespace}/api-table/apps.cozystack.io/v1alpha1/ferretdb
  - key: kafkas
    label: Kafka
    link: /openapi-ui/{clusterName}/{namespace}/api-table/apps.cozystack.io/v1alpha1/kafkas

- key: naas
  label: NaaS
  children:
  - key: tcpbalancers
    label: TCP Balancers
    link: /openapi-ui/{clusterName}/{namespace}/api-table/apps.cozystack.io/v1alpha1/tcpbalancers
  - key: httpcaches
    label: HTTP Caching
    link: /openapi-ui/{clusterName}/{namespace}/api-table/apps.cozystack.io/v1alpha1/httpcaches
  - key: vpns
    label: VPNs
    link: /openapi-ui/{clusterName}/{namespace}/api-table/apps.cozystack.io/v1alpha1/vpns

- key: administration
  label: Administration
  children:
    - key: tenants
      label: Tenants
      link: /openapi-ui/{clusterName}/{namespace}/api-table/apps.cozystack.io/v1alpha1/tenants
    - key: monitorings
      label: Monitoring
      link: /openapi-ui/{clusterName}/{namespace}/api-table/apps.cozystack.io/v1alpha1/monitorings
    - key: etcds
      label: Etcd
      link: /openapi-ui/{clusterName}/{namespace}/api-table/apps.cozystack.io/v1alpha1/etcds
    - key: ingresses
      label: Ingress
      link: /openapi-ui/{clusterName}/{namespace}/api-table/apps.cozystack.io/v1alpha1/ingresses
    - key: seaweedfses
      label: SeaweedFS
      link: /openapi-ui/{clusterName}/{namespace}/api-table/apps.cozystack.io/v1alpha1/seaweedfses
    - key: bootboxes
      label: BootBox
      link: /openapi-ui/{clusterName}/{namespace}/api-table/apps.cozystack.io/v1alpha1/bootboxes
{{- end }}
