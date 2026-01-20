package telemetry

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	helmv2 "github.com/fluxcd/helm-controller/api/v2"

	cozyv1alpha1 "github.com/cozystack/cozystack/api/v1alpha1"
)

const (
	// ApplicationKindLabel is the label used to identify application kind on HelmReleases
	ApplicationKindLabel = "apps.cozystack.io/application.kind"
)

// Collector handles telemetry data collection for cozystack-controller
type Collector struct {
	client client.Client
	config *Config
	ticker *time.Ticker
	stopCh chan struct{}
}

// NewCollector creates a new telemetry collector for cozystack-controller
func NewCollector(c client.Client, config *Config, _ *rest.Config) (*Collector, error) {
	return &Collector{
		client: c,
		config: config,
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
	return true
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

	// Get all ApplicationDefinitions to know which kinds exist
	var appDefList cozyv1alpha1.ApplicationDefinitionList
	if err := c.client.List(ctx, &appDefList); err != nil {
		logger.Info(fmt.Sprintf("Failed to list ApplicationDefinitions: %v", err))
		return
	}

	// Build a map of all known application kinds (initialized with 0)
	appKindCounts := make(map[string]int)
	for _, appDef := range appDefList.Items {
		kind := appDef.Spec.Application.Kind
		if kind != "" {
			appKindCounts[kind] = 0
		}
	}

	// Get all HelmReleases with apps.cozystack.io/application.kind label in one request
	var hrList helmv2.HelmReleaseList
	if err := c.client.List(ctx, &hrList, client.HasLabels{ApplicationKindLabel}); err != nil {
		logger.Info(fmt.Sprintf("Failed to list HelmReleases: %v", err))
		return
	}

	// Count HelmReleases by application kind
	for _, hr := range hrList.Items {
		kind := hr.Labels[ApplicationKindLabel]
		if kind != "" {
			appKindCounts[kind]++
		}
	}

	// Create metrics buffer
	var metrics strings.Builder

	// Write application count metrics
	for kind, count := range appKindCounts {
		metrics.WriteString(fmt.Sprintf(
			"cozy_application_count{kind=\"%s\"} %d\n",
			kind,
			count,
		))
	}

	// Send metrics only if there's something to send
	if metrics.Len() > 0 {
		if err := c.sendMetrics(clusterID, metrics.String()); err != nil {
			logger.Info(fmt.Sprintf("Failed to send metrics: %v", err))
		}
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
