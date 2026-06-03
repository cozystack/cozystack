package fluxshardoperator

import (
	"fmt"
	"strconv"
	"strings"

	corev1 "k8s.io/api/core/v1"
)

const (
	// ShardKeyLabel is the Flux sharding label helm-controller shards select on.
	// One label => exactly one owning shard, no overlap and no gap by construction.
	ShardKeyLabel = "sharding.fluxcd.io/key"

	// LegacyShardKey is the single-bucket value every tenant HelmRelease carried
	// before sharding (reconciled by the hand-rolled flux-tenants Deployment).
	// HelmReleases are still born with this value (ApplicationDefinition
	// release.labels and chart templates stamp it); the webhook rewrites it to
	// the tenant's shard at admission and the placement controller relabels any
	// stragglers.
	LegacyShardKey = "tenants"

	// TenantShardLabel records the tenant->shard assignment on the tenant
	// namespace so the webhook can resolve it cheaply at admission time.
	// HelmRelease labels remain the source of truth for index rebuilds.
	TenantShardLabel = "internal.cozystack.io/flux-shard"

	// ApplicationKindLabel identifies the Application kind a HelmRelease was
	// rendered from; parent tenant HelmReleases carry kind "Tenant".
	ApplicationKindLabel = "apps.cozystack.io/application.kind"

	// TenantKind is the Application kind of parent tenant HelmReleases.
	TenantKind = "Tenant"

	// ManagedByLabel marks Deployments provisioned by this operator.
	ManagedByLabel = "app.kubernetes.io/managed-by"

	// ManagedByValue is the value of ManagedByLabel on operator-owned objects.
	ManagedByValue = "flux-shard-operator"

	// FieldOwner is the server-side apply field manager of this operator.
	FieldOwner = "flux-shard-operator"

	// FluxDeploymentName is the all-in-one flux Deployment the shard
	// helm-controllers are cloned from.
	FluxDeploymentName = "flux"

	// LegacyTenantsDeploymentName is the hand-rolled tenant shard retired by
	// this operator once no HelmRelease carries the legacy shard key.
	LegacyTenantsDeploymentName = "flux-tenants"

	// HelmControllerContainerName is the container cloned from the flux
	// Deployment.
	HelmControllerContainerName = "helm-controller"

	shardPrefix           = "shard"
	shardDeploymentPrefix = "helm-controller-"
)

// ShardCountAuto is the --shard-count value that enables automatic sizing
// from the tenant HelmRelease count.
const ShardCountAuto = "auto"

// Config carries the operator-wide settings shared by the placement
// controller, the shard provisioner and the admission webhook.
type Config struct {
	// FluxNamespace is the namespace the flux-aio Deployment and the shard
	// Deployments live in.
	FluxNamespace string
	// ShardCount is the explicit number of helm-controller shards to provision
	// and distribute tenants over. Ignored when AutoShardCount is set.
	ShardCount int
	// AutoShardCount sizes the shard count automatically from the tenant
	// HelmRelease count (see Config.EffectiveShardCount).
	AutoShardCount bool
	// ShardConcurrent is the --concurrent value for each shard.
	ShardConcurrent int
	// RebalanceThreshold is the (maxLoad-minLoad)/avgLoad ratio above which the
	// placement controller starts moving tenants between shards.
	RebalanceThreshold float64
	// PinnedTenants maps a tenant namespace (e.g. "tenant-bigone") to a shard
	// name (e.g. "shard3"); pinned tenants are excluded from rebalancing.
	PinnedTenants map[string]string
	// ShardResources overrides the helm-controller container resources cloned
	// from flux-aio. Empty fields inherit the cloned values.
	ShardResources corev1.ResourceRequirements
}

// ShardName returns the canonical shard key value for index i (shard0..).
func ShardName(i int) string {
	return shardPrefix + strconv.Itoa(i)
}

// ShardDeploymentName returns the Deployment name for shard index i.
func ShardDeploymentName(i int) string {
	return shardDeploymentPrefix + ShardName(i)
}

// ParseShardIndex parses a canonical shard key value ("shard<i>") back into
// its index. Non-canonical values (including the legacy "tenants" bucket)
// return ok=false.
func ParseShardIndex(s string) (int, bool) {
	rest, found := strings.CutPrefix(s, shardPrefix)
	if !found || rest == "" {
		return 0, false
	}
	i, err := strconv.Atoi(rest)
	if err != nil || i < 0 || ShardName(i) != s {
		return 0, false
	}
	return i, true
}

// ParseShardDeploymentIndex parses a shard Deployment name back into its
// shard index.
func ParseShardDeploymentIndex(name string) (int, bool) {
	rest, found := strings.CutPrefix(name, shardDeploymentPrefix)
	if !found {
		return 0, false
	}
	return ParseShardIndex(rest)
}

// ParseShardCount parses the --shard-count flag value: "auto" enables
// automatic sizing, otherwise a positive integer is an explicit shard count.
func ParseShardCount(s string) (count int, auto bool, err error) {
	if strings.EqualFold(strings.TrimSpace(s), ShardCountAuto) {
		return 0, true, nil
	}
	n, convErr := strconv.Atoi(strings.TrimSpace(s))
	if convErr != nil || n < 1 {
		return 0, false, fmt.Errorf("invalid shard count %q: must be %q or a positive integer", s, ShardCountAuto)
	}
	return n, false, nil
}

// ParsePinnedTenants parses the --pinned-tenants flag value
// ("tenant-a=shard1,tenant-b=shard0") into a map.
func ParsePinnedTenants(s string) (map[string]string, error) {
	pinned := map[string]string{}
	if s == "" {
		return pinned, nil
	}
	for _, pair := range strings.Split(s, ",") {
		k, v, found := strings.Cut(strings.TrimSpace(pair), "=")
		if !found || k == "" {
			return nil, fmt.Errorf("invalid pinned tenant entry %q, expected <tenant-namespace>=<shard>", pair)
		}
		if _, ok := ParseShardIndex(v); !ok {
			return nil, fmt.Errorf("invalid shard %q for pinned tenant %q", v, k)
		}
		pinned[k] = v
	}
	return pinned, nil
}
