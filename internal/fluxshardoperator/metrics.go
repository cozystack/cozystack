package fluxshardoperator

import (
	"github.com/prometheus/client_golang/prometheus"
	"sigs.k8s.io/controller-runtime/pkg/metrics"
)

var (
	shardLoadGauge = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Name: "cozy_flux_shard_load",
		Help: "Number of tenant HelmReleases assigned to each helm-controller shard.",
	}, []string{"shard"})

	tenantsGauge = prometheus.NewGauge(prometheus.GaugeOpts{
		Name: "cozy_flux_shard_tenants",
		Help: "Number of tenants known to the placement controller.",
	})

	helmReleasesGauge = prometheus.NewGauge(prometheus.GaugeOpts{
		Name: "cozy_flux_shard_helmreleases",
		Help: "Number of tenant HelmReleases known to the placement controller.",
	})

	pendingMovesGauge = prometheus.NewGauge(prometheus.GaugeOpts{
		Name: "cozy_flux_shard_pending_moves",
		Help: "Number of tenants whose assignment differs from the desired placement (backfill or rebalance in progress).",
	})

	recommendedShardsGauge = prometheus.NewGauge(prometheus.GaugeOpts{
		Name: "cozy_flux_shard_recommended_count",
		Help: "Autosizing recommendation for the shard count (v1 surfaces it only, enforcement is manual via values).",
	})

	movesCounter = prometheus.NewCounter(prometheus.CounterOpts{
		Name: "cozy_flux_shard_moves_total",
		Help: "Total number of tenant shard reassignments performed.",
	})
)

func init() {
	metrics.Registry.MustRegister(
		shardLoadGauge,
		tenantsGauge,
		helmReleasesGauge,
		pendingMovesGauge,
		recommendedShardsGauge,
		movesCounter,
	)
}
