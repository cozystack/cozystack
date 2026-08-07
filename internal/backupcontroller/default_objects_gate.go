package backupcontroller

import (
	"context"
	"errors"
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
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
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

// defaultObjectsMissing reports how many of the objects the platform default
// backups depend on are absent from the cluster: the bucket credentials
// Secret the projector reads, the Strategy CRs the BackupClass routes to,
// and the Velero BSL. It is the signal an operator alerts on: a non-zero
// value that does not return to zero means the platform default backups are
// not usable, whatever the HelmRelease's Ready condition says.
//
// It is only written when a check reached a conclusion. An API error mid-check
// leaves the previous value in place rather than flapping the alert, so the
// gauge alone cannot distinguish "healthy" from "not evaluated" — that is what
// defaultObjectsCheckErrors is for, and the runbook pairs the two.
var defaultObjectsMissing = prometheus.NewGaugeVec(
	prometheus.GaugeOpts{
		Name: "cozystack_backup_default_objects_missing",
		Help: "Number of objects the platform default backups depend on (bucket credentials Secret, BackupClass strategy CRs, Velero BSL) that do not exist in the cluster.",
	},
	[]string{"backupclass"},
)

// defaultObjectsForceReconciles counts the forced Helm upgrades the gate
// issued to materialise those objects. A counter that keeps climbing means
// the forced render is not producing the objects (e.g. a CRD is missing),
// which is a different failure than the install-time race this gate closes.
// A suspended release is NOT counted here: it is skipped before the patch,
// so a paused release cannot masquerade as a render that keeps failing.
var defaultObjectsForceReconciles = prometheus.NewCounterVec(
	prometheus.CounterOpts{
		Name: "cozystack_backup_default_objects_force_reconciles_total",
		Help: "Number of forced HelmRelease reconciles issued to create missing platform backup objects.",
	},
	[]string{"namespace", "name"},
)

// defaultObjectsCheckErrors counts the checks that could not reach a
// conclusion (API error reading the source Secret, the BackupClass, or one
// of the routed objects). While this climbs, defaultObjectsMissing is stale:
// alerting on the gauge alone would silently keep reporting the last known
// state, including a 0 that is no longer true.
var defaultObjectsCheckErrors = prometheus.NewCounterVec(
	prometheus.CounterOpts{
		Name: "cozystack_backup_default_objects_check_errors_total",
		Help: "Number of default backup object checks that failed before reaching a conclusion, leaving cozystack_backup_default_objects_missing stale.",
	},
	[]string{"backupclass"},
)

func init() {
	metrics.Registry.MustRegister(defaultObjectsMissing, defaultObjectsForceReconciles, defaultObjectsCheckErrors)
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
	// CredentialsHelmRelease is the platform bucket's <bucket>-system
	// release, which renders the user-credentials Secret named by
	// Config.SourceSecretName behind a `lookup` of the COSI Secret — the
	// same permanent-skip trap as the Strategy CRs, one release earlier.
	// Forcing it is what unblocks everything downstream, since the gate
	// itself cannot resolve the bucket name without that Secret.
	//
	// Empty when the platform bucket is not provisioned by Cozystack
	// (backupStorage.provisionBucket=false, external S3): the Secret is
	// then admin-managed and no release renders it, so there is nothing
	// to force.
	CredentialsHelmRelease types.NamespacedName
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
	now func() time.Time
	// lastForce and lastCredentialsForce throttle the two releases
	// independently: the credentials release is forced during the window in
	// which the objects release cannot even be evaluated, so sharing one
	// timestamp would make the first force delay the second by
	// MinForceInterval for no reason.
	lastForce            time.Time
	lastCredentialsForce time.Time
}

// errHelmReleaseSuspended reports that the target release has
// spec.suspend: true. helm-controller ignores reconcile.fluxcd.io/forceAt on
// a suspended release, so patching it would be a no-op repeated every
// MinForceInterval for as long as the suspension lasts.
var errHelmReleaseSuspended = errors.New("HelmRelease is suspended")

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
	// force() logs the suspended-release skip from deep in the call stack;
	// without this it would land on a bare logger with no name.
	ctx = log.IntoContext(ctx, logger)

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

// checkTimeout bounds a single check. It stays below Period so a slow check
// cannot overlap the next tick, and is capped so a long Period does not mean a
// correspondingly long stall.
func (g *DefaultObjectsGate) checkTimeout() time.Duration {
	const maxTimeout = 30 * time.Second
	if g.Period <= 0 {
		return maxTimeout
	}
	if half := g.Period / 2; half < maxTimeout {
		return half
	}
	return maxTimeout
}

func (g *DefaultObjectsGate) checkAndLog(ctx context.Context, logger logr.Logger) {
	// Bound every tick. Check makes several sequential API calls (Secret get,
	// BackupClass get, one Get per routed strategy, HelmRelease patch) and the
	// manager context is only cancelled at shutdown, so an API server that
	// stalls on any one of them would block here indefinitely. The loop that
	// drives this is sequential, so that single stuck call would stop all
	// later checks for the life of the pod: the recovery this gate exists to
	// provide would go silent, with a stale gauge and no further log line, and
	// only a restart would bring it back.
	checkCtx, cancel := context.WithTimeout(ctx, g.checkTimeout())
	defer cancel()
	missing, forced, err := g.Check(checkCtx)
	switch {
	case err != nil:
		// Info, not Error: every branch here is either transient (the
		// bucket is still being provisioned) or already exposed as a
		// metric, and this runs every minute for the life of the cluster.
		logger.Info("default backup objects check failed", "error", err.Error())
	case len(missing) > 0 && forced:
		// The release named here is whichever one renders what is missing:
		// the credentials Secret comes from the bucket's <bucket>-system
		// release, everything else from this chart's own.
		logger.Info("forced a Helm upgrade to create missing default backup objects",
			"helmRelease", g.forcedReleaseFor(missing).String(), "missing", missing)
	case len(missing) > 0:
		logger.Info("default backup objects still missing, no force issued on this tick",
			"helmRelease", g.forcedReleaseFor(missing).String(), "missing", missing,
			"minForceInterval", g.MinForceInterval.String())
	default:
		logger.V(1).Info("all default backup objects present")
	}
}

// forcedReleaseFor reports which release renders the given missing set, for
// logging only. reconcileCredentials returns the credentials Secret as the
// sole missing object, and it is never mixed with the routed objects —
// resolving the bucket name is a precondition for checking those at all.
func (g *DefaultObjectsGate) forcedReleaseFor(missing []string) types.NamespacedName {
	credsKey := fmt.Sprintf("Secret/%s", g.Config.SourceSecretName)
	if len(missing) == 1 && missing[0] == credsKey && g.CredentialsHelmRelease.Name != "" {
		return g.CredentialsHelmRelease
	}
	return g.HelmRelease
}

// Check resolves the platform bucket name, verifies that every object the
// BackupClass routes to exists, and forces one Helm upgrade when any is
// missing. It returns the missing objects (formatted for logs) and whether
// a forced upgrade was issued on this call.
//
// While the source Secret is absent or carries no bucket name, the gate
// cannot evaluate the routed objects — their templates gate on the same
// unresolved bucket — so it instead forces the release that renders that
// Secret (see reconcileCredentials). The bucket name is read from the
// projector's source Secret rather than from the BucketClaim, so the gate
// needs no objectstorage RBAC and works for the external-S3 path too.
func (g *DefaultObjectsGate) Check(ctx context.Context) ([]string, bool, error) {
	src := &corev1.Secret{}
	if err := g.Client.Get(ctx, types.NamespacedName{Namespace: g.Config.SourceNamespace, Name: g.Config.SourceSecretName}, src); err != nil {
		if apierrors.IsNotFound(err) {
			// The Secret the projector reads does not exist. On a fresh
			// install that is the bootstrap window; months later it is the
			// permanent skip this gate exists to repair, and the two are
			// indistinguishable from here — so treat both the same way and
			// let the throttle bound the cost.
			return g.reconcileCredentials(ctx)
		}
		defaultObjectsCheckErrors.WithLabelValues(g.BackupClassName).Inc()
		return nil, false, fmt.Errorf("get source credentials Secret %s/%s: %w", g.Config.SourceNamespace, g.Config.SourceSecretName, err)
	}
	creds, err := parseSourceSecret(src)
	if err != nil {
		defaultObjectsCheckErrors.WithLabelValues(g.BackupClassName).Inc()
		return nil, false, err
	}
	if creds.bucket == "" {
		// The Secret exists but carries no bucket name: a partially rendered
		// or hand-written Secret. Same treatment — a re-render of the
		// producing release is the only thing that can complete it.
		return g.reconcileCredentials(ctx)
	}

	backupClass := &backupsv1alpha1.BackupClass{}
	if err := g.Client.Get(ctx, client.ObjectKey{Name: g.BackupClassName}, backupClass); err != nil {
		defaultObjectsCheckErrors.WithLabelValues(g.BackupClassName).Inc()
		return nil, false, fmt.Errorf("get BackupClass %s: %w", g.BackupClassName, err)
	}

	missing, err := g.missingObjects(ctx, backupClass)
	if err != nil {
		defaultObjectsCheckErrors.WithLabelValues(g.BackupClassName).Inc()
		return nil, false, err
	}
	defaultObjectsMissing.WithLabelValues(g.BackupClassName).Set(float64(len(missing)))
	if len(missing) == 0 {
		return nil, false, nil
	}

	return g.force(ctx, g.HelmRelease, &g.lastForce, missing)
}

// reconcileCredentials handles the one object the gate cannot check the same
// way as the others: the bucket user-credentials Secret it reads to resolve
// the bucket name in the first place.
//
// That Secret is rendered by the platform bucket's <bucket>-system release
// behind a `lookup` of the COSI Secret, so it is subject to the identical
// permanent-skip trap — and when it is missing, nothing downstream can
// resolve: the projector has no source, every Strategy CR and the Velero BSL
// stay gated off, and migration 50 cannot find its snapshot target.
//
// The chart cannot repair this itself. Making the render `fail` instead of
// skip would abort the whole <bucket>-system release — the other users'
// Secrets and the bucket UI Deployment/Service/Ingress with it — and one
// user whose BucketAccess never provisions would park the release in Failed,
// blocking every later upgrade of that bucket with no per-bucket way out. So
// the repair belongs here, where it is per-object and costs a throttled
// annotation patch: exactly the mechanism the gate already applies to the
// Strategy CRs, one release earlier in the chain.
//
// Forcing that release cannot deadlock its own precondition: the BucketClaim
// and the BucketAccess whose COSI Secret the lookup reads are rendered
// unconditionally by the PARENT release (packages/apps/bucket/templates/
// bucketclaim.yaml), not by the one being forced.
func (g *DefaultObjectsGate) reconcileCredentials(ctx context.Context) ([]string, bool, error) {
	missing := []string{fmt.Sprintf("Secret/%s", g.Config.SourceSecretName)}
	defaultObjectsMissing.WithLabelValues(g.BackupClassName).Set(float64(len(missing)))
	if g.CredentialsHelmRelease.Name == "" || g.CredentialsHelmRelease.Namespace == "" {
		// External S3: the Secret is admin-managed and no release renders
		// it. Report it missing, force nothing.
		return missing, false, nil
	}
	return g.force(ctx, g.CredentialsHelmRelease, &g.lastCredentialsForce, missing)
}

// force issues at most one throttled forced Helm upgrade of target and
// reports whether it went through. last is the caller's throttle timestamp,
// advanced on every outcome that must not be retried immediately.
func (g *DefaultObjectsGate) force(ctx context.Context, target types.NamespacedName, last *time.Time, missing []string) ([]string, bool, error) {
	now := g.now()
	if !last.IsZero() && now.Sub(*last) < g.MinForceInterval {
		return missing, false, nil
	}
	if err := g.forceHelmRelease(ctx, target, now); err != nil {
		switch {
		case apierrors.IsNotFound(err):
			// Not a Flux-managed install (a plain `helm install` for local
			// development). There is nothing to force; stay quiet rather
			// than logging an error every tick.
			*last = now
			return missing, false, nil
		case errors.Is(err, errHelmReleaseSuspended):
			// Deliberate operator state (cozyhr suspend, maintenance). The
			// annotations would be ignored, so skip the patch entirely and
			// leave the force counter alone — a climbing counter must keep
			// meaning "the forced render is not producing the objects".
			log.FromContext(ctx).Info("skipped forcing a suspended HelmRelease; missing default backup objects will not be repaired until it is resumed",
				"helmRelease", target.String(), "missing", missing)
			*last = now
			return missing, false, nil
		}
		return missing, false, fmt.Errorf("force HelmRelease %s: %w", target.String(), err)
	}
	*last = now
	defaultObjectsForceReconciles.WithLabelValues(target.Namespace, target.Name).Inc()
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
		// When the Velero BSL is disabled (velero.bslEnabled=false, surfaced
		// here as an empty VeleroNamespace) the chart gates the Velero
		// Strategy CRs off the SAME flag, so they are never rendered. The
		// BackupClass still routes to them unconditionally, so counting them
		// as missing would force a Helm upgrade every MinForceInterval
		// forever against a render that can never produce them.
		if g.VeleroNamespace == "" && group == strategyAPIGroup && kind == "Velero" {
			continue
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
		// Resolve through the RESTMapper, like the strategy loop above, so a
		// genuinely absent Velero API is a NoMatch we skip rather than a
		// NotFound we count. The dynamic client's Get bypasses the mapper and
		// returns a plain 404 for an unserved group, which IsNotFound would
		// catch first, making the "CRDs absent" branch dead code and looping
		// the gate forever if Velero were removed while bslEnabled=true.
		mapping, err := g.RESTMapping(schema.GroupKind{Group: backupStorageLocationGVR.Group, Kind: "BackupStorageLocation"})
		switch {
		case err == nil:
			_, getErr := g.Resource(mapping.Resource).Namespace(g.VeleroNamespace).Get(ctx, defaultBackupStorageLocationName, metav1.GetOptions{})
			switch {
			case getErr == nil:
			case apierrors.IsNotFound(getErr):
				missing = append(missing, fmt.Sprintf("BackupStorageLocation/%s", defaultBackupStorageLocationName))
			default:
				return nil, fmt.Errorf("get BackupStorageLocation %s: %w", defaultBackupStorageLocationName, getErr)
			}
		case meta.IsNoMatchError(err):
			// Velero API not served (e.g. Velero uninstalled after bootstrap):
			// no Helm re-render can create the BSL, so it is not "missing".
		default:
			return nil, fmt.Errorf("map BackupStorageLocation: %w", err)
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
//
// A point Get precedes the patch so a suspended release is skipped rather
// than re-stamped forever: helm-controller ignores both annotations while
// spec.suspend is true, and `cozyhr suspend` sets exactly that. Without the
// check the gate would patch every MinForceInterval for the whole suspension
// and inflate the force counter, which the runbook reads as a render that
// keeps failing — the wrong diagnosis. The Get needs no RBAC beyond the
// `get` on helmreleases the chart already grants.
func (g *DefaultObjectsGate) forceHelmRelease(ctx context.Context, target types.NamespacedName, now time.Time) error {
	hr, err := g.Resource(helmReleaseGVR).Namespace(target.Namespace).Get(ctx, target.Name, metav1.GetOptions{})
	if err != nil {
		return err
	}
	// A malformed spec.suspend (wrong type) is reported by NestedBool as an
	// error; treat it as not suspended and let the patch proceed, rather
	// than silently disabling the repair on a field we could not parse.
	if suspended, found, err := unstructured.NestedBool(hr.Object, "spec", "suspend"); err == nil && found && suspended {
		return errHelmReleaseSuspended
	}

	stamp := now.UTC().Format(time.RFC3339Nano)
	patch := fmt.Sprintf(
		`{"metadata":{"annotations":{"reconcile.fluxcd.io/forceAt":%q,"reconcile.fluxcd.io/requestedAt":%q}}}`,
		stamp, stamp,
	)
	_, err = g.Resource(helmReleaseGVR).Namespace(target.Namespace).
		Patch(ctx, target.Name, types.MergePatchType, []byte(patch), metav1.PatchOptions{})
	return err
}
