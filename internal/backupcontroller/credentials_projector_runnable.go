package backupcontroller

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/go-logr/logr"
	"github.com/prometheus/client_golang/prometheus"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	"sigs.k8s.io/controller-runtime/pkg/metrics"
)

// credentialsProjectionFailures counts projection errors per target
// namespace and reason. Exposes terminal misconfigurations (malformed
// source Secret, unowned target) as a Kubernetes-native signal rather
// than a log line so operators see them in the same Grafana board the
// rest of the controller's metrics land in. Reset to zero on success
// would mask repeated transient failures, so this is a monotonic counter
// — alerting is on rate(...) or absent successes.
var credentialsProjectionFailures = prometheus.NewCounterVec(
	prometheus.CounterOpts{
		Name: "cozystack_backup_credentials_projection_failures_total",
		Help: "Number of failed BackupCredentials projections, by target namespace and reason.",
	},
	[]string{"namespace", "reason"},
)

// credentialsProjectionSuccesses counts successful projection ticks per
// target namespace. Combined with the failures counter, operators can
// alert on absent successes (BSL going stale) without log scraping.
var credentialsProjectionSuccesses = prometheus.NewCounterVec(
	prometheus.CounterOpts{
		Name: "cozystack_backup_credentials_projection_successes_total",
		Help: "Number of successful BackupCredentials projections, by target namespace.",
	},
	[]string{"namespace"},
)

func init() {
	metrics.Registry.MustRegister(credentialsProjectionFailures, credentialsProjectionSuccesses)
}

// SystemCredentialsProjector is a controller-runtime Runnable that
// projects the platform-managed backup credentials Secret into a list of
// system namespaces (e.g. cozy-velero, where Velero's BSL references a
// Secret-backed credential file).
//
// Tenant namespaces are handled lazily by the BackupJob/RestoreJob
// reconcilers — they project on demand right before each Job runs. System
// namespaces, by contrast, are referenced by long-lived static
// configuration (BackupStorageLocation, operator deployments) that must
// have credentials available at all times, so the runnable refreshes them
// on a periodic tick instead.
type SystemCredentialsProjector struct {
	Client     client.Client
	Config     BackupCredentialsConfig
	Namespaces []string
	Period     time.Duration
}

var (
	_ manager.Runnable               = (*SystemCredentialsProjector)(nil)
	_ manager.LeaderElectionRunnable = (*SystemCredentialsProjector)(nil)
)

// NeedLeaderElection makes the projector leader-elected — only one
// controller-manager replica ticks against the apiserver per cycle.
// Without this, every replica (the deployment runs 2) would race to
// project the same Secret every minute, doubling apiserver load and
// inflating the success counter with duplicate increments.
func (p *SystemCredentialsProjector) NeedLeaderElection() bool { return true }

// NewSystemCredentialsProjector parses a comma-separated namespace list
// and returns a configured runnable. An empty namespaces list disables
// the runnable — Start returns immediately. Defaults to a 1-minute tick
// if Period is zero.
func NewSystemCredentialsProjector(c client.Client, cfg BackupCredentialsConfig, namespacesCSV string, period time.Duration) *SystemCredentialsProjector {
	var ns []string
	for _, s := range strings.Split(namespacesCSV, ",") {
		s = strings.TrimSpace(s)
		if s != "" {
			ns = append(ns, s)
		}
	}
	if period == 0 {
		period = time.Minute
	}
	return &SystemCredentialsProjector{
		Client:     c,
		Config:     cfg,
		Namespaces: ns,
		Period:     period,
	}
}

func (p *SystemCredentialsProjector) Start(ctx context.Context) error {
	logger := log.FromContext(ctx).WithName("system-credentials-projector")
	if len(p.Namespaces) == 0 || !p.Config.IsEnabled() {
		logger.V(1).Info("system credentials projector disabled", "namespaces", p.Namespaces, "configured", p.Config.IsEnabled())
		return nil
	}
	tick := time.NewTicker(p.Period)
	defer tick.Stop()
	p.projectAll(ctx, logger)
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-tick.C:
			p.projectAll(ctx, logger)
		}
	}
}

func (p *SystemCredentialsProjector) projectAll(ctx context.Context, logger logr.Logger) {
	for _, ns := range p.Namespaces {
		if err := ProjectBackupCredentials(ctx, p.Client, p.Config, ns); err != nil {
			reason := classifyReason(err)
			credentialsProjectionFailures.WithLabelValues(ns, reason).Inc()
			// Always log at Info, not Error — the Prometheus counter is
			// the actionable signal (rate(failures_total{reason="..."}))
			// so a malformed source Secret does not spam Error logs every
			// minute. Operators alert on the counter; the log line is
			// kept for diagnostic context only.
			logger.Info("system credentials projection failed", "namespace", ns, "reason", reason, "error", err.Error())
			continue
		}
		credentialsProjectionSuccesses.WithLabelValues(ns).Inc()
		logger.V(1).Info("system credentials projection succeeded", "namespace", ns)
	}
}

// classifyReason returns the ProjectionError reason if the underlying
// error is one of ours, otherwise "Unknown". Used as a metric label, so
// the cardinality is bounded by the small Reason* set.
func classifyReason(err error) string {
	var perr *ProjectionError
	if errors.As(err, &perr) {
		return perr.Reason
	}
	return "Unknown"
}
