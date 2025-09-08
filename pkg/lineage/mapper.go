package lineage

import (
	"fmt"
	"strings"

	helmv2 "github.com/fluxcd/helm-controller/api/v2"
)

type AppMapper interface {
	Map(*helmv2.HelmRelease) (apiVersion, kind, prefix string, err error)
}

type stubMapper struct{}

var stubMapperMap = map[string]string{
	"cozystack-extra/bootbox":        "apps.cozystack.io/v1alpha1/BootBox/",
	"cozystack-apps/bucket":          "apps.cozystack.io/v1alpha1/Bucket/bucket-",
	"cozystack-apps/clickhouse":      "apps.cozystack.io/v1alpha1/ClickHouse/clickhouse-",
	"cozystack-extra/etcd":           "apps.cozystack.io/v1alpha1/Etcd/",
	"cozystack-apps/ferretdb":        "apps.cozystack.io/v1alpha1/FerretDB/ferretdb-",
	"cozystack-apps/http-cache":      "apps.cozystack.io/v1alpha1/HTTPCache/http-cache-",
	"cozystack-extra/info":           "apps.cozystack.io/v1alpha1/Info/",
	"cozystack-extra/ingress":        "apps.cozystack.io/v1alpha1/Ingress/",
	"cozystack-apps/kafka":           "apps.cozystack.io/v1alpha1/Kafka/kafka-",
	"cozystack-apps/kubernetes":      "apps.cozystack.io/v1alpha1/Kubernetes/kubernetes-",
	"cozystack-extra/monitoring":     "apps.cozystack.io/v1alpha1/Monitoring/",
	"cozystack-apps/mysql":           "apps.cozystack.io/v1alpha1/MySQL/mysql-",
	"cozystack-apps/nats":            "apps.cozystack.io/v1alpha1/NATS/nats-",
	"cozystack-apps/postgres":        "apps.cozystack.io/v1alpha1/Postgres/postgres-",
	"cozystack-apps/rabbitmq":        "apps.cozystack.io/v1alpha1/RabbitMQ/rabbitmq-",
	"cozystack-apps/redis":           "apps.cozystack.io/v1alpha1/Redis/redis-",
	"cozystack-extra/seaweedfs":      "apps.cozystack.io/v1alpha1/SeaweedFS/",
	"cozystack-apps/tcp-balancer":    "apps.cozystack.io/v1alpha1/TCPBalancer/tcp-balancer-",
	"cozystack-apps/tenant":          "apps.cozystack.io/v1alpha1/Tenant/tenant-",
	"cozystack-apps/virtual-machine": "apps.cozystack.io/v1alpha1/VirtualMachine/virtual-machine-",
	"cozystack-apps/vm-disk":         "apps.cozystack.io/v1alpha1/VMDisk/vm-disk-",
	"cozystack-apps/vm-instance":     "apps.cozystack.io/v1alpha1/VMInstance/vm-instance-",
	"cozystack-apps/vpn":             "apps.cozystack.io/v1alpha1/VPN/vpn-",
}

func (s *stubMapper) Map(hr *helmv2.HelmRelease) (string, string, string, error) {
	val, ok := stubMapperMap[hr.Spec.Chart.Spec.SourceRef.Name+"/"+hr.Spec.Chart.Spec.Chart]
	if !ok {
		return "", "", "", fmt.Errorf("cannot map helm release %s/%s to dynamic app", hr.Namespace, hr.Name)
	}
	split := strings.Split(val, "/")
	return strings.Join(split[:2], "/"), split[2], split[3], nil
}
