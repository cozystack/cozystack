package backupcontroller

import (
	"context"
	"fmt"
	"net/http"
	"sort"
	"time"

	"github.com/go-logr/logr"
	"github.com/prometheus/client_golang/prometheus"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/rest"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/apiutil"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	"sigs.k8s.io/controller-runtime/pkg/metrics"

	backupsv1alpha1 "github.com/cozystack/cozystack/api/backups/v1alpha1"
)

// defaultObjectsMissing reports how many of the objects the platform
// BackupClass depends on are absent from the cluster. It is the signal an
// operator alerts on: a non-zero value that does not return to zero means
// the platform default backups are not usable, whatever the HelmRelease's
// Ready condition says.
var defaultObjectsMissing = prometheus.NewGaugeVec(
	prometheus.GaugeOpts{
		Name: "cozystack_backup_default_objects_missing",
		Help: "Number of objects referenced by the platform BackupClass that do not exist in the cluster.",
	},
	[]string{"backupclass"},
)

// defaultObjectsForceReconciles counts the forced Helm upgrades the gate
// issued to materialise those objects. A counter that keeps climbing means
// the forced render is not producing the objects (e.g. a CRD is missing),
// which is a different failure than the install-time race this gate closes.
var defaultObjectsForceReconciles = prometheus.NewCounterVec(
	prometheus.CounterOpts{
		Name: "cozystack_backup_default_objects_force_reconciles_total",
		Help: "Number of forced HelmRelease reconciles issued to create missing platform backup objects.",
	},
	[]string{"namespace", "name"},
)

func init() {
	metrics.Registry.MustRegister(defaultObjectsMissing, defaultObjectsForceReconciles)
}

const (
	// strategyAPIGroup is the API group of the strategy.backups.cozystack.io
	// CRs a BackupClass routes to. BackupClassStrategy.StrategyRef is a
	// TypedLocalObjectReference and carries no version, so the group+kind
	// are mapped to a resource through the RESTMapper.
	strategyAPIGroup = "strategy.backups.cozystack.io"

	// defaultBackupStorageLocationName is the Velero BSL the cozy-default
	// Velero strategies reference via storageLocation. It is rendered by the
	// same lookup-gated template as the Strategy CRs, so it belongs to the
	// same existence check.
	defaultBackupStorageLocationName = "cozy-default"
)

var backupStorageLocationGVR = schema.GroupVersionResource{
	Group:    "velero.io",
	Version:  "v1",
	Resource: "backupstoragelocations",
}

// DefaultObjectsGate closes a race the chart's templates cannot close by
// themselves.
//
// The default Strategy CRs and the Velero BackupStorageLocation are
// Helm-templated behind a `lookup` of the BucketClaim the SAME chart
// creates, because the S3 bucket name is assigned by the COSI driver
// (`bucket-<claim-UID>`) and is therefore unknowable at render time. On a
// fresh install the lookup is empty and those templates render nothing.
// helm-controller does not re-render a release that succeeded and whose
// chart and values did not change (drift detection is off on
// operator-generated HelmReleases), so the skip is PERMANENT: the objects
// are never created, and the only recovery is a forced Helm upgrade
// (reconcile.fluxcd.io/forceAt + requestedAt — a plain reconcile request
// does not re-render).
//
// This runnable performs that recovery automatically. Once the bucket name
// is resolvable — read from the same platform credentials Secret the
// projector already consumes, so no new dependency and no `lookup` — it
// checks that every object the platform BackupClass routes to actually
// exists, and forces one Helm upgrade when any is missing. A forced
// upgrade re-runs the lookups, so the gated templates materialise.
//
// It deliberately does not create the objects itself: their bodies are
// values-driven (endpoint, region, forcePathStyle, the Altinity strategy's
// whole PodTemplateSpec) and Helm remains their single owner. The gate only
// makes sure the render that produces them actually happens.
type DefaultObjectsGate struct {
	// Client reads the BackupClass (cached) and the source credentials
	// Secret (uncached — the manager disables the Secret cache).
	Client client.Client
	// Interface is a dynamic client used for the per-object existence
	// checks and the HelmRelease annotation patch. Going through the
	// dynamic client keeps these reads off the controller-runtime cache,
	// so they need only `get` RBAC and start no cluster-wide informers.
	dynamic.Interface
	meta.RESTMapper

	// Config supplies the source credentials Secret coordinates. Reused
	// verbatim from the credentials projector.
	Config BackupCredentialsConfig
	// BackupClassName is the platform BackupClass whose strategyRefs
	// enumerate the objects that must exist. Empty disables the gate.
	BackupClassName string
	// HelmRelease is the release that renders those objects and is forced
	// when they are missing. Empty name disables the gate.
	HelmRelease types.NamespacedName
	// VeleroNamespace is where the cozy-default BackupStorageLocation
	// lives. Empty skips the BSL check (velero.bslEnabled=false).
	VeleroNamespace string

	// Period is the check interval. Defaults to 1 minute.
	Period time.Duration
	// MinForceInterval throttles forced upgrades so a condition the forced
	// render cannot fix (missing CRD, wrong bucket) does not turn into a
	// hot loop against helm-controller. Defaults to 5 minutes.
	MinForceInterval time.Duration

	// now is a test seam for the throttle clock.
	now       func() time.Time
	lastForce time.Time
}

var (
	_ manager.Runnable               = (*DefaultObjectsGate)(nil)
	_ manager.LeaderElectionRunnable = (*DefaultObjectsGate)(nil)
)

// NeedLeaderElection keeps a single replica forcing the release. Two
// replicas racing the same annotation patch would double the Helm upgrades
// for no benefit.
func (g *DefaultObjectsGate) NeedLeaderElection() bool { return true }

// SetupWithManager wires the dynamic client and the RESTMapper from the
// manager's rest.Config, mirroring RestoreJobReconciler, and registers the
// gate as a manager Runnable.
func (g *DefaultObjectsGate) SetupWithManager(mgr ctrl.Manager) error {
	cfg := mgr.GetConfig()
	var err error
	if g.Interface, err = dynamic.NewForConfig(cfg); err != nil {
		return err
	}
	var h *http.Client
	if h, err = rest.HTTPClientFor(cfg); err != nil {
		return err
	}
	if g.RESTMapper, err = apiutil.NewDynamicRESTMapper(cfg, h); err != nil {
		return err
	}
	return mgr.Add(g)
}

func (g *DefaultObjectsGate) Start(ctx context.Context) error {
	logger := log.FromContext(ctx).WithName("default-objects-gate")
	if g.BackupClassName == "" || g.HelmRelease.Name == "" || g.HelmRelease.Namespace == "" || !g.Config.IsEnabled() {
		logger.V(1).Info("default backup objects gate disabled",
			"backupClass", g.BackupClassName,
			"helmRelease", g.HelmRelease.String(),
			"credentialsConfigured", g.Config.IsEnabled())
		return nil
	}
	if g.Period == 0 {
		g.Period = time.Minute
	}
	if g.MinForceInterval == 0 {
		g.MinForceInterval = 5 * time.Minute
	}
	if g.now == nil {
		g.now = time.Now
	}

	tick := time.NewTicker(g.Period)
	defer tick.Stop()
	g.checkAndLog(ctx, logger)
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-tick.C:
			g.checkAndLog(ctx, logger)
		}
	}
}

func (g *DefaultObjectsGate) checkAndLog(ctx context.Context, logger logr.Logger) {
	missing, forced, err := g.Check(ctx)
	switch {
	case err != nil:
		// Info, not Error: every branch here is either transient (the
		// bucket is still being provisioned) or already exposed as a
		// metric, and this runs every minute for the life of the cluster.
		logger.Info("default backup objects check failed", "error", err.Error())
	case len(missing) > 0 && forced:
		logger.Info("forced a Helm upgrade to create missing default backup objects",
			"helmRelease", g.HelmRelease.String(), "missing", missing)
	case len(missing) > 0:
		logger.Info("default backup objects still missing, force throttled",
			"helmRelease", g.HelmRelease.String(), "missing", missing,
			"minForceInterval", g.MinForceInterval.String())
	default:
		logger.V(1).Info("all default backup objects present")
	}
}

// Check resolves the platform bucket name, verifies that every object the
// BackupClass routes to exists, and forces one Helm upgrade when any is
// missing. It returns the missing objects (formatted for logs) and whether
// a forced upgrade was issued on this call.
//
// It is a no-op while the bucket name is unresolvable: forcing then would
// re-render the same empty lookup. The bucket name is read from the
// projector's source Secret rather than from the BucketClaim, so the gate
// needs no objectstorage RBAC and works for the external-S3 path too.
func (g *DefaultObjectsGate) Check(ctx context.Context) ([]string, bool, error) {
	src := &corev1.Secret{}
	if err := g.Client.Get(ctx, types.NamespacedName{Namespace: g.Config.SourceNamespace, Name: g.Config.SourceSecretName}, src); err != nil {
		return nil, false, fmt.Errorf("get source credentials Secret %s/%s: %w", g.Config.SourceNamespace, g.Config.SourceSecretName, err)
	}
	creds, err := parseSourceSecret(src)
	if err != nil {
		return nil, false, err
	}
	if creds.bucket == "" {
		// The bucket is not provisioned yet (or an admin-managed Secret
		// omits the name). Nothing a re-render could resolve.
		return nil, false, nil
	}

	backupClass := &backupsv1alpha1.BackupClass{}
	if err := g.Client.Get(ctx, client.ObjectKey{Name: g.BackupClassName}, backupClass); err != nil {
		return nil, false, fmt.Errorf("get BackupClass %s: %w", g.BackupClassName, err)
	}

	missing, err := g.missingObjects(ctx, backupClass)
	if err != nil {
		return nil, false, err
	}
	defaultObjectsMissing.WithLabelValues(g.BackupClassName).Set(float64(len(missing)))
	if len(missing) == 0 {
		return nil, false, nil
	}

	now := g.now()
	if !g.lastForce.IsZero() && now.Sub(g.lastForce) < g.MinForceInterval {
		return missing, false, nil
	}
	if err := g.forceHelmRelease(ctx, now); err != nil {
		if apierrors.IsNotFound(err) {
			// Not a Flux-managed install (a plain `helm install` for local
			// development). There is nothing to force; stay quiet rather
			// than logging an error every tick.
			g.lastForce = now
			return missing, false, nil
		}
		return missing, false, fmt.Errorf("force HelmRelease %s: %w", g.HelmRelease.String(), err)
	}
	g.lastForce = now
	defaultObjectsForceReconciles.WithLabelValues(g.HelmRelease.Namespace, g.HelmRelease.Name).Inc()
	return missing, true, nil
}

// missingObjects returns a sorted, de-duplicated list of the
// "<kind>/<name>" objects the BackupClass references that do not exist,
// plus the Velero BSL when a Velero namespace is configured.
//
// An unmappable group/kind (the CRD is not installed) is NOT reported as
// missing: no Helm re-render can create an object whose CRD is absent, and
// counting it would keep the gate forcing upgrades forever.
func (g *DefaultObjectsGate) missingObjects(ctx context.Context, backupClass *backupsv1alpha1.BackupClass) ([]string, error) {
	seen := map[string]struct{}{}
	var missing []string

	for _, strategy := range backupClass.Spec.Strategies {
		name := strategy.StrategyRef.Name
		kind := strategy.StrategyRef.Kind
		if name == "" || kind == "" {
			continue
		}
		group := strategyAPIGroup
		if strategy.StrategyRef.APIGroup != nil && *strategy.StrategyRef.APIGroup != "" {
			group = *strategy.StrategyRef.APIGroup
		}
		key := fmt.Sprintf("%s/%s", kind, name)
		if _, dup := seen[key]; dup {
			continue
		}
		seen[key] = struct{}{}

		mapping, err := g.RESTMapping(schema.GroupKind{Group: group, Kind: kind})
		if err != nil {
			if meta.IsNoMatchError(err) {
				continue
			}
			return nil, fmt.Errorf("map %s/%s: %w", group, kind, err)
		}
		// Strategy CRs are cluster-scoped; Namespace("") is correct for
		// both scopes on the dynamic client.
		_, err = g.Resource(mapping.Resource).Get(ctx, name, metav1.GetOptions{})
		switch {
		case err == nil:
		case apierrors.IsNotFound(err):
			missing = append(missing, key)
		default:
			return nil, fmt.Errorf("get %s %s: %w", kind, name, err)
		}
	}

	if g.VeleroNamespace != "" {
		_, err := g.Resource(backupStorageLocationGVR).Namespace(g.VeleroNamespace).Get(ctx, defaultBackupStorageLocationName, metav1.GetOptions{})
		switch {
		case err == nil:
		case apierrors.IsNotFound(err):
			missing = append(missing, fmt.Sprintf("BackupStorageLocation/%s", defaultBackupStorageLocationName))
		case meta.IsNoMatchError(err):
			// Velero CRDs absent: nothing to materialise.
		default:
			return nil, fmt.Errorf("get BackupStorageLocation %s: %w", defaultBackupStorageLocationName, err)
		}
	}

	sort.Strings(missing)
	return missing, nil
}

// forceHelmRelease stamps BOTH reconcile.fluxcd.io/forceAt and
// reconcile.fluxcd.io/requestedAt. requestedAt alone only asks
// helm-controller to reconcile, which is a no-op for a release whose chart
// and values are unchanged — it is forceAt that makes it run a real Helm
// upgrade, and forceAt is only honoured together with requestedAt.
func (g *DefaultObjectsGate) forceHelmRelease(ctx context.Context, now time.Time) error {
	stamp := now.UTC().Format(time.RFC3339Nano)
	patch := fmt.Sprintf(
		`{"metadata":{"annotations":{"reconcile.fluxcd.io/forceAt":%q,"reconcile.fluxcd.io/requestedAt":%q}}}`,
		stamp, stamp,
	)
	_, err := g.Resource(helmReleaseGVR).Namespace(g.HelmRelease.Namespace).
		Patch(ctx, g.HelmRelease.Name, types.MergePatchType, []byte(patch), metav1.PatchOptions{})
	return err
}
