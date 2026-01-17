package telemetry

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/discovery"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	cozyv1alpha1 "github.com/cozystack/cozystack/api/v1alpha1"
	"github.com/cozystack/cozystack/pkg/version"
)

// OperatorCollector handles telemetry data collection for cozystack-operator
type OperatorCollector struct {
	client          client.Client
	discoveryClient discovery.DiscoveryInterface
	config          *Config
	ticker          *time.Ticker
	stopCh          chan struct{}
}

// NewOperatorCollector creates a new telemetry collector for cozystack-operator
func NewOperatorCollector(c client.Client, config *Config, kubeConfig *rest.Config) (*OperatorCollector, error) {
	discoveryClient, err := discovery.NewDiscoveryClientForConfig(kubeConfig)
	if err != nil {
		return nil, fmt.Errorf("failed to create discovery client: %w", err)
	}
	return &OperatorCollector{
		client:          c,
		discoveryClient: discoveryClient,
		config:          config,
	}, nil
}

// Start implements manager.Runnable
func (c *OperatorCollector) Start(ctx context.Context) error {
	if c.config.Disabled {
		return nil
	}

	c.ticker = time.NewTicker(c.config.Interval)
	c.stopCh = make(chan struct{})

	// Initial collection
	c.collect(ctx)

	for {
		select {
		case <-ctx.Done():
			c.ticker.Stop()
			close(c.stopCh)
			return nil
		case <-c.ticker.C:
			c.collect(ctx)
		}
	}
}

// NeedLeaderElection implements manager.LeaderElectionRunnable
func (c *OperatorCollector) NeedLeaderElection() bool {
	return true
}

// getSizeGroup returns the exponential size group for PVC
func getSizeGroup(size resource.Quantity) string {
	gb := size.Value() / (1024 * 1024 * 1024)
	switch {
	case gb <= 1:
		return "1Gi"
	case gb <= 5:
		return "5Gi"
	case gb <= 10:
		return "10Gi"
	case gb <= 25:
		return "25Gi"
	case gb <= 50:
		return "50Gi"
	case gb <= 100:
		return "100Gi"
	case gb <= 250:
		return "250Gi"
	case gb <= 500:
		return "500Gi"
	case gb <= 1024:
		return "1Ti"
	case gb <= 2048:
		return "2Ti"
	case gb <= 5120:
		return "5Ti"
	default:
		return "10Ti"
	}
}

// collect gathers and sends telemetry data
func (c *OperatorCollector) collect(ctx context.Context) {
	logger := log.FromContext(ctx).V(1)

	// Get cluster ID from kube-system namespace
	var kubeSystemNS corev1.Namespace
	if err := c.client.Get(ctx, types.NamespacedName{Name: "kube-system"}, &kubeSystemNS); err != nil {
		logger.Info(fmt.Sprintf("Failed to get kube-system namespace: %v", err))
		return
	}

	clusterID := string(kubeSystemNS.UID)

	// Get Kubernetes version
	k8sVersion, err := c.discoveryClient.ServerVersion()
	if err != nil {
		logger.Info(fmt.Sprintf("Failed to get Kubernetes version: %v", err))
		return
	}

	// Get nodes
	var nodeList corev1.NodeList
	if err := c.client.List(ctx, &nodeList); err != nil {
		logger.Info(fmt.Sprintf("Failed to list nodes: %v", err))
		return
	}

	// Create metrics buffer
	var metrics strings.Builder

	// Add cluster info metric
	metrics.WriteString(fmt.Sprintf(
		"cozy_cluster_info{cozystack_version=\"%s\",kubernetes_version=\"%s\"} 1\n",
		version.Version,
		k8sVersion.GitVersion,
	))

	// Collect node metrics grouped by OS and kernel
	nodeOSCount := make(map[string]map[string]int) // os -> kernel -> count
	for _, node := range nodeList.Items {
		osKey := fmt.Sprintf("%s (%s)", node.Status.NodeInfo.OperatingSystem, node.Status.NodeInfo.OSImage)
		kernelKey := node.Status.NodeInfo.KernelVersion

		if _, exists := nodeOSCount[osKey]; !exists {
			nodeOSCount[osKey] = make(map[string]int)
		}
		nodeOSCount[osKey][kernelKey]++
	}

	for osKey, kernels := range nodeOSCount {
		for kernel, count := range kernels {
			metrics.WriteString(fmt.Sprintf(
				"cozy_nodes_count{os=\"%s\",kernel=\"%s\"} %d\n",
				osKey,
				kernel,
				count,
			))
		}
	}

	// Collect cluster capacity metrics (cpu, memory, gpu)
	capacityTotals := make(map[string]int64)
	for _, node := range nodeList.Items {
		for resourceName, quantity := range node.Status.Capacity {
			name := string(resourceName)
			if name == "cpu" || name == "memory" || strings.HasPrefix(name, "nvidia.com/") {
				capacityTotals[name] += quantity.Value()
			}
		}
	}

	for resourceName, total := range capacityTotals {
		metrics.WriteString(fmt.Sprintf(
			"cozy_cluster_capacity{resource=\"%s\"} %d\n",
			resourceName,
			total,
		))
	}

	// Collect LoadBalancer services metrics
	var serviceList corev1.ServiceList
	if err := c.client.List(ctx, &serviceList); err != nil {
		logger.Info(fmt.Sprintf("Failed to list Services: %v", err))
	} else {
		lbCount := 0
		for _, svc := range serviceList.Items {
			if svc.Spec.Type == corev1.ServiceTypeLoadBalancer {
				lbCount++
			}
		}
		metrics.WriteString(fmt.Sprintf("cozy_loadbalancers_count %d\n", lbCount))
	}

	// Collect PV metrics grouped by driver and size
	var pvList corev1.PersistentVolumeList
	if err := c.client.List(ctx, &pvList); err != nil {
		logger.Info(fmt.Sprintf("Failed to list PVs: %v", err))
	} else {
		pvMetrics := make(map[string]map[string]int) // size -> driver -> count

		for _, pv := range pvList.Items {
			if capacity, ok := pv.Spec.Capacity[corev1.ResourceStorage]; ok {
				sizeGroup := getSizeGroup(capacity)

				driver := "unknown"
				if pv.Spec.CSI != nil {
					driver = pv.Spec.CSI.Driver
				} else if pv.Spec.HostPath != nil {
					driver = "hostpath"
				} else if pv.Spec.NFS != nil {
					driver = "nfs"
				}

				if _, exists := pvMetrics[sizeGroup]; !exists {
					pvMetrics[sizeGroup] = make(map[string]int)
				}
				pvMetrics[sizeGroup][driver]++
			}
		}

		for size, drivers := range pvMetrics {
			for driver, count := range drivers {
				metrics.WriteString(fmt.Sprintf(
					"cozy_pvs_count{driver=\"%s\",size=\"%s\"} %d\n",
					driver,
					size,
					count,
				))
			}
		}
	}

	// Collect installed packages
	var packageList cozyv1alpha1.PackageList
	if err := c.client.List(ctx, &packageList); err != nil {
		logger.Info(fmt.Sprintf("Failed to list Packages: %v", err))
	} else {
		for _, pkg := range packageList.Items {
			variant := pkg.Spec.Variant
			if variant == "" {
				variant = "default"
			}
			metrics.WriteString(fmt.Sprintf(
				"cozy_package_info{name=\"%s\",variant=\"%s\"} 1\n",
				pkg.Name,
				variant,
			))
		}
	}

	// Send metrics
	if err := c.sendMetrics(clusterID, metrics.String()); err != nil {
		logger.Info(fmt.Sprintf("Failed to send metrics: %v", err))
	}
}

// sendMetrics sends collected metrics to the configured endpoint
func (c *OperatorCollector) sendMetrics(clusterID, metrics string) error {
	req, err := http.NewRequest("POST", c.config.Endpoint, bytes.NewBufferString(metrics))
	if err != nil {
		return fmt.Errorf("failed to create request: %w", err)
	}

	req.Header.Set("Content-Type", "text/plain")
	req.Header.Set("X-Cluster-ID", clusterID)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("failed to send request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("unexpected status code: %d", resp.StatusCode)
	}

	return nil
}
