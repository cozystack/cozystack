package telemetry

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"sort"
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
)

// Collector handles telemetry data collection and sending
type Collector struct {
	client          client.Client
	discoveryClient discovery.DiscoveryInterface
	config          *Config
	ticker          *time.Ticker
	stopCh          chan struct{}
}

// NewCollector creates a new telemetry collector
func NewCollector(client client.Client, config *Config, kubeConfig *rest.Config) (*Collector, error) {
	discoveryClient, err := discovery.NewDiscoveryClientForConfig(kubeConfig)
	if err != nil {
		return nil, fmt.Errorf("failed to create discovery client: %w", err)
	}
	return &Collector{
		client:          client,
		discoveryClient: discoveryClient,
		config:          config,
	}, nil
}

// Start implements manager.Runnable
func (c *Collector) Start(ctx context.Context) error {
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
func (c *Collector) NeedLeaderElection() bool {
	// Only run telemetry collector on the leader
	return true
}

// Stop halts telemetry collection
func (c *Collector) Stop() {
	close(c.stopCh)
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
func (c *Collector) collect(ctx context.Context) {
	logger := log.FromContext(ctx).V(1)

	// Get cluster ID from kube-system namespace
	var kubeSystemNS corev1.Namespace
	if err := c.client.Get(ctx, types.NamespacedName{Name: "kube-system"}, &kubeSystemNS); err != nil {
		logger.Info(fmt.Sprintf("Failed to get kube-system namespace: %v", err))
		return
	}

	clusterID := string(kubeSystemNS.UID)

	// Get all CozystackBundles
	var bundleList cozyv1alpha1.CozystackBundleList
	bundleNameStr := ""
	bundleEnable := ""
	bundleDisable := ""
	oidcEnabled := "false"

	if err := c.client.List(ctx, &bundleList); err != nil {
		logger.Info(fmt.Sprintf("Failed to list CozystackBundles: %v", err))
		// Continue with empty bundle data instead of returning
	} else {
		// Collect bundle names (sorted alphabetically)
		bundleNames := make([]string, 0, len(bundleList.Items))
		for _, bundle := range bundleList.Items {
			bundleNames = append(bundleNames, bundle.Name)
		}
		sort.Strings(bundleNames)
		bundleNameStr = strings.Join(bundleNames, ",")

		// Collect all packages from all bundles
		var allEnabledPackages []string
		var allDisabledPackages []string

		for _, bundle := range bundleList.Items {
			for _, pkg := range bundle.Spec.Packages {
				if pkg.Disabled {
					allDisabledPackages = append(allDisabledPackages, pkg.Name)
				} else {
					allEnabledPackages = append(allEnabledPackages, pkg.Name)
					// Check if keycloak package is enabled
					if pkg.Name == "keycloak" {
						oidcEnabled = "true"
					}
				}
			}
		}

		// Sort package lists alphabetically
		sort.Strings(allEnabledPackages)
		sort.Strings(allDisabledPackages)

		bundleEnable = strings.Join(allEnabledPackages, ",")
		bundleDisable = strings.Join(allDisabledPackages, ",")
	}

	// Get Kubernetes version from nodes
	var nodeList corev1.NodeList
	if err := c.client.List(ctx, &nodeList); err != nil {
		logger.Info(fmt.Sprintf("Failed to list nodes: %v", err))
		return
	}

	// Create metrics buffer
	var metrics strings.Builder

	// Add Cozystack info metric
	if len(nodeList.Items) > 0 {
		k8sVersion := "unknown"
		if version, err := c.discoveryClient.ServerVersion(); err == nil && version != nil {
			k8sVersion = version.String()
		}
		metrics.WriteString(fmt.Sprintf(
			"cozy_cluster_info{cozystack_version=\"%s\",kubernetes_version=\"%s\",oidc_enabled=\"%s\",bundle_name=\"%s\",bundle_enable=\"%s\",bundle_disable=\"%s\"} 1\n",
			c.config.CozystackVersion,
			k8sVersion,
			oidcEnabled,
			bundleNameStr,
			bundleEnable,
			bundleDisable,
		))
	}

	// Collect node metrics
	if len(nodeList.Items) > 0 {
		nodeOSCount := make(map[string]int)
		kernelVersion := "unknown"
		for _, node := range nodeList.Items {
			key := fmt.Sprintf("%s (%s)", node.Status.NodeInfo.OperatingSystem, node.Status.NodeInfo.OSImage)
			nodeOSCount[key] = nodeOSCount[key] + 1
			if kernelVersion == "unknown" && node.Status.NodeInfo.KernelVersion != "" {
				kernelVersion = node.Status.NodeInfo.KernelVersion
			}
		}

		for osKey, count := range nodeOSCount {
			metrics.WriteString(fmt.Sprintf(
				"cozy_nodes_count{os=\"%s\",kernel=\"%s\"} %d\n",
				osKey,
				kernelVersion,
				count,
			))
		}
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

	// Count tenant namespaces
	var nsList corev1.NamespaceList
	if err := c.client.List(ctx, &nsList); err != nil {
		logger.Info(fmt.Sprintf("Failed to list Namespaces: %v", err))
	} else {
		tenantCount := 0
		for _, ns := range nsList.Items {
			if strings.HasPrefix(ns.Name, "tenant-") {
				tenantCount++
			}
		}
		metrics.WriteString(fmt.Sprintf("cozy_tenants_count %d\n", tenantCount))
	}

	// Collect PV metrics grouped by driver and size
	var pvList corev1.PersistentVolumeList
	if err := c.client.List(ctx, &pvList); err != nil {
		logger.Info(fmt.Sprintf("Failed to list PVs: %v", err))
	} else {
		// Map to store counts by size and driver
		pvMetrics := make(map[string]map[string]int)

		for _, pv := range pvList.Items {
			if capacity, ok := pv.Spec.Capacity[corev1.ResourceStorage]; ok {
				sizeGroup := getSizeGroup(capacity)

				// Get the CSI driver name
				driver := "unknown"
				if pv.Spec.CSI != nil {
					driver = pv.Spec.CSI.Driver
				} else if pv.Spec.HostPath != nil {
					driver = "hostpath"
				} else if pv.Spec.NFS != nil {
					driver = "nfs"
				}

				// Initialize nested map if needed
				if _, exists := pvMetrics[sizeGroup]; !exists {
					pvMetrics[sizeGroup] = make(map[string]int)
				}

				// Increment count for this size/driver combination
				pvMetrics[sizeGroup][driver]++
			}
		}

		// Write metrics
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

	// Collect workload metrics
	var monitorList cozyv1alpha1.WorkloadMonitorList
	if err := c.client.List(ctx, &monitorList); err != nil {
		logger.Info(fmt.Sprintf("Failed to list WorkloadMonitors: %v", err))
		// Continue without workload metrics instead of returning
	} else {
		for _, monitor := range monitorList.Items {
			metrics.WriteString(fmt.Sprintf(
				"cozy_workloads_count{uid=\"%s\",kind=\"%s\",type=\"%s\",version=\"%s\"} %d\n",
				monitor.UID,
				monitor.Spec.Kind,
				monitor.Spec.Type,
				monitor.Spec.Version,
				monitor.Status.ObservedReplicas,
			))
		}
	}

	// Send metrics
	if err := c.sendMetrics(clusterID, metrics.String()); err != nil {
		logger.Info(fmt.Sprintf("Failed to send metrics: %v", err))
	}
}

// sendMetrics sends collected metrics to the configured endpoint
func (c *Collector) sendMetrics(clusterID, metrics string) error {
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
