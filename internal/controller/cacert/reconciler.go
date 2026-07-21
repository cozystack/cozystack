/*
Copyright 2026 The Cozystack Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

// Package cacert extracts the trust anchor of a managed application into a
// canonical, key-free Secret the tenant can read.
//
// Every engine in the catalog issues a per-release CA, but the object that
// holds ca.crt almost always holds a private key next to it: the cert-manager
// CA Secret carries tls.key, CloudNativePG's <release>-ca carries ca.key.
// Handing a tenant the trust anchor by granting read on one of those objects
// hands over key material too. The consume contract is therefore one canonical
// object per release — an Opaque Secret named "<release>.tenant-ca" holding
// ONLY ca.crt — and this controller produces it, so a tenant learns exactly one
// name.
//
// # The declaration is a namespaced sentinel
//
// A chart declares that one of its Secrets should be published to the tenant by
// rendering a TenantProjection (internal.cozystack.io/v1alpha1) named after the
// release. The sentinel is the reconcile unit, and its spec names the source
// Secret and the key to lift. No tenant RBAC role grants any verb on
// internal.cozystack.io, so a tenant cannot forge a sentinel to publish a CA of
// their choosing; a ValidatingAdmissionPolicy pins writes to helm-controller as
// defence in depth.
//
// The sentinel replaces two earlier discovery legs — a publish-ca-cert label on
// the source Secret, and a spec.caCert declaration on the cluster-scoped
// ApplicationDefinition — with a single object the chart renders and Flux owns.
// The label leg emulated object semantics (a Secret standing in for a
// declaration) for something that is a declaration; the declared leg lived on a
// cluster-scoped object for a per-namespace fact. Both collapse into the
// namespaced sentinel.
//
// # The canonical name is "<release>.tenant-ca", and the DOT is the whole point
//
// It is deliberately not "<release>-ca-cert": that name is already taken, and
// means opposite things depending on the engine — the redis fork publishes a
// key-free CA there, while Percona Server for MongoDB publishes a key-BEARING
// Secret under exactly the same name. A target an engine already claims is a
// name the controller could only reach by overwriting live key material.
//
// It is also not "<release>-tenant-ca", and that near-miss is the more
// instructive one. No operator claims that suffix — which is true, and is the
// wrong question. "No operator claims it" is a statement about SUFFIXES, and the
// collision is about NAMES. Release names are built as <prefix><app name>, so a
// suffix free of engines is not a name free of releases: postgres app "foo"
// would project to postgres-foo-tenant-ca,
// which is exactly what CloudNativePG's own "<cluster>-ca" resolves to for a
// sibling app named "foo-tenant" — a key-BEARING Secret, in the same namespace,
// at the same name. Whichever writer arrives first, the other is broken:
// CloudNativePG rejects a CA Secret with no ca.key in it, and this controller
// refuses to overwrite a Secret it did not create. That is the same class of bug
// as the -ca-cert one, one level up: cross-RELEASE instead of cross-ENGINE.
//
// The dot ends the class rather than dodging another instance of it. Application
// names are validated as DNS-1035 LABELS (pkg/apis/apps/validation), whose
// grammar is [a-z]([-a-z0-9]*[a-z0-9])? — a dot cannot occur in one. Release
// prefixes are dot-free, and every engine CA suffix ("-ca", "-ca-cert",
// "-cluster-ca-cert", "-clients-ca-cert", "-ssl", "-tls", ...) is dot-free too.
// So <prefix><app><suffix> is dot-free BY CONSTRUCTION, and a name containing a
// dot cannot be produced by any engine, for any application name, ever. Secret
// names are DNS-1123 SUBDOMAINS, where a dot is legal — the two grammars differ
// by exactly the character that makes this safe.
//
// # Load-bearing behaviours
//
//  1. Fail closed on key material. projectionData is the only way bytes reach a
//     projection, and it re-asserts on EVERY write that the value carries a PEM
//     certificate and no PEM private-key header. The check mirrors the
//     chart-side guard in cozy-lib.tls.caCertSecret
//     (packages/library/cozy-lib/templates/_tls.tpl), so both ends of the
//     contract reject the same inputs.
//  2. Sanitize at write time. Exactly one whitelisted key is emitted, under the
//     canonical name, and its value is rebuilt from the certificate blocks the
//     guard parsed rather than copied from the source. The source Data map is
//     never copied wholesale — that is the precise bug this controller exists to
//     prevent.
//  3. Tolerate asynchronous sources. An operator creates its CA Secret long
//     after the chart renders, and may populate it later still. "Not there yet"
//     is the normal startup state: the sentinel's status reports
//     Ready=False/SourceNotFound so it is visible to `kubectl get`, the source
//     Secret is watched so the projection appears the instant it lands, and a
//     slow resync heals a projection deleted out of band.
//  4. Own the projection, never adopt a stranger. The projection is
//     owner-referenced to the sentinel, so deleting the application prunes the
//     sentinel (Flux) and garbage-collects the projection with it. A Secret of
//     the same name that the controller did not create is left untouched, and
//     the collision is surfaced as a Warning Event.
//
// # Why the owner is the sentinel, not the HelmRelease
//
// The lineage admission webhook decides whether the tenant may read the
// projection, and it derives that by walking owner references up to a
// HelmRelease (pkg/lineage/lineage.go). The projection references the sentinel;
// the sentinel carries NO owner reference and instead the helm.toolkit.fluxcd.io/name
// label Flux stamps on every rendered object, which the same walk resolves to
// the HelmRelease as a fallback. So the chain
// projection → sentinel → HelmRelease → application resolves through unmodified
// lineage code, and the projection gets the internal.cozystack.io/tenantresource
// label that lets the tenant read it through the tenantsecrets API.
//
// Referencing the sentinel rather than the HelmRelease directly is also what
// ties the projection's lifetime to the declaration. A retired declaration is
// withdrawn by one of two mechanisms. When the chart stops rendering the sentinel
// entirely, Flux prunes it and Kubernetes garbage-collects the projection through
// the owner reference. When the sentinel outlives its CACert entry — it keeps
// existing but stops declaring the anchor, so the owner still exists and GC never
// fires — withdrawProjection deletes the projection explicitly, and only one this
// sentinel owns; a foreign Secret, or another sentinel's projection at the name,
// is left untouched.
//
// # What this controller does NOT decide
//
// It does not grant the tenant read access, and cannot. That verdict belongs to
// the lineage admission webhook, which derives it from the ApplicationDefinition's
// spec.secrets selectors and stamps internal.cozystack.io/tenantresource — "true"
// or "false" — overwriting whatever the writer put there.
//
// The webhook sees an object ONCE. It is registered with objectSelector
// internal.cozystack.io/managed-by-cozystack DoesNotExist and stamps that very
// marker alongside its verdict, so once it has admitted an object neither the
// old nor the new version matches the selector again and every later UPDATE is
// skipped. The verdict is therefore frozen at what the selectors said when the
// projection was CREATED — which is the wrong answer the moment a definition's
// selectors change. Un-freezing it is deliberate work, and it is why the
// reconciler tracks the selectors at all; see SelectorsDigestAnnotation.
//
// This controller's side of the contract is to stamp
// internal.cozystack.io/tenant-ca on the projection, the single engine-agnostic
// label a definition's spec.secrets can select on, and to make sure the webhook
// is asked again whenever the definition changes (see SelectorsDigestAnnotation).
//
// Until a definition actually carries that selector, projections are produced
// and kept correct but are marked tenantresource=false, and no tenant can read
// one. Converging the per-engine definitions is deliberately separate work.
package cacert

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"maps"
	"reflect"
	"regexp"
	"sort"
	"strings"
	"time"

	helmv2 "github.com/fluxcd/helm-controller/api/v2"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	crsource "sigs.k8s.io/controller-runtime/pkg/source"

	internalv1alpha1 "github.com/cozystack/cozystack/api/internalapi/v1alpha1"
	cozyv1alpha1 "github.com/cozystack/cozystack/api/v1alpha1"
	appsv1alpha1 "github.com/cozystack/cozystack/pkg/apis/apps/v1alpha1"
)

const (
	// TenantCALabel is stamped on every projection. It is the single,
	// engine-agnostic selector an ApplicationDefinition needs in spec.secrets
	// (matchLabels) to promote the trust anchor to a TenantSecret — no
	// per-release name templating.
	TenantCALabel = "internal.cozystack.io/tenant-ca"

	// ManagedLabel marks a projection as controller-created. The reconciler
	// only ever writes over a Secret bearing this label, so a Secret that
	// merely shares the name — an engine's own key-bearing CA, for one — is
	// never clobbered.
	ManagedLabel = "internal.cozystack.io/ca-cert-copy"

	// SourceRefAnnotation records the "<namespace>/<name>" of the source on the
	// projection, for traceability: `kubectl describe` shows which Secret fed
	// the trust anchor. It is part of the drift check so a source change
	// rewrites it, but nothing reads it to make a decision.
	SourceRefAnnotation = "internal.cozystack.io/ca-cert-source"

	// SelectorsDigestAnnotation records a digest of the ApplicationDefinition's
	// spec.secrets at the projection's last write. It exists to force a
	// re-admission when those selectors change.
	//
	// The reconciler does not decide whether a tenant may READ the projection.
	// The lineage admission webhook does, from exactly those selectors, and it
	// stamps its verdict — "true" or "false" — into the
	// internal.cozystack.io/tenantresource label, overwriting whatever was there.
	// A projection created before its definition selects
	// internal.cozystack.io/tenant-ca is therefore marked "false".
	//
	// Nothing would ever fix that. When the definition later gains the selector
	// the sentinel IS reconciled (definitions are watched), but the data, labels,
	// source and owner have all stayed the same, so the drift check would skip
	// the write — and with no write there is no admission, so the verdict is
	// never revisited and the tenant stays locked out of a Secret that is now
	// theirs. Making the selectors part of the drift check is half of closing
	// that: they change, so exactly one write happens.
	//
	// The write is only half because a write is not an admission. The webhook is
	// registered with objectSelector managed-by-cozystack DoesNotExist and stamps
	// that marker in the same pass as the verdict, so every UPDATE after the
	// first admission is skipped no matter how many writes are forced. The write
	// must therefore also DROP the marker, which is what hands the projection
	// back to the webhook; upsertProjection does that on a digest change, and the
	// reasoning is there.
	//
	// Revocation is the direction that makes this load-bearing rather than tidy.
	// A definition that stops selecting the anchor must take the tenant's access
	// away; with the verdict frozen it stays "true" and the tenant keeps reading
	// a trust anchor the platform withdrew.
	//
	// It digests spec.secrets rather than recording the definition's
	// resourceVersion, which would be the cruder signal: resourceVersion moves on
	// ANY edit to the definition, so every unrelated field change would rewrite
	// every projection of that kind for no gain.
	SelectorsDigestAnnotation = "internal.cozystack.io/ca-cert-selectors"

	// managedByCozystackLabel is stamped by the lineage admission webhook. The
	// reconciler never writes it; it only takes care not to strip it.
	managedByCozystackLabel = "internal.cozystack.io/managed-by-cozystack"

	// helmNameLabel is the Helm ownership label Flux stamps on every rendered
	// object. On the sentinel it names the release, from which the canonical
	// projection name is built, and it is the fallback the lineage webhook
	// resolves the application through.
	helmNameLabel = "helm.toolkit.fluxcd.io/name"

	// caCertKey is the canonical key of the trust anchor: the default key read
	// from a source, and the ONLY key ever written.
	caCertKey = "ca.crt"

	// certificatePEMType is the only PEM block type a trust anchor may carry.
	// It is the type crypto/x509 writes and the one CertPool.AppendCertsFromPEM
	// accepts, so it is what a tenant's verifier will look for.
	certificatePEMType = "CERTIFICATE"

	// projectionSuffix completes the canonical projection name,
	// "<release>.tenant-ca".
	//
	// The leading DOT is load-bearing and must not be "tidied" into a dash. Every
	// engine's own CA Secret ("-ca", "-ca-cert", "-cluster-ca-cert",
	// "-clients-ca-cert", "-ssl", "-tls", ...) is a potential SOURCE, and those
	// names are built from a release name that is itself <prefix><app name>. A
	// dash-separated suffix is therefore reachable by another release's engine —
	// "<foo>-tenant-ca" is CloudNativePG's CA for the app named "foo-tenant" — and
	// the collision destroys one of the two, in whichever order they arrive.
	//
	// A dot cannot: application names are DNS-1035 labels, which cannot contain
	// one, so no <prefix><app><engine suffix> can ever produce this name. Secret
	// names are DNS-1123 subdomains, which can. See the package doc.
	projectionSuffix = ".tenant-ca"

	// appsGroup is the API group of the aggregated application kinds, as it
	// appears in the lineage labels.
	appsGroup = "apps.cozystack.io"

	trueValue = "true"

	// resyncInterval heals a projection deleted out of band, or a name collision
	// the operator has since cleared. The source Secret is watched, so a rotation
	// does not wait for it; this is the slow backstop for state changes no watch
	// delivers.
	resyncInterval = 5 * time.Minute

	// conditionReady is the single status condition the sentinel carries.
	conditionReady = "Ready"

	// sourceSecretNameField indexes TenantProjections by the source Secret names
	// their entries reference, so a Secret event maps back to the sentinels that
	// consume it in O(1).
	sourceSecretNameField = ".spec.projections.sourceSecretName"
)

// Event reasons surfaced on the affected object.
const (
	reasonPrivateKeyRefused     = "CACertPrivateKeyRefused"
	reasonInvalidCACert         = "CACertInvalid"
	reasonCollision             = "CACertSecretCollision"
	reasonCanonicalNameOccupied = "CACertCanonicalNameOccupied"
	reasonCanonicalNameContract = "CACertCanonicalNameContract"
)

// Status condition reasons recorded on the sentinel's Ready condition. They are
// the whole point of the sentinel: a state the old design retried silently is
// now visible with `kubectl get tproj`.
const (
	reasonProjected             = "Projected"
	reasonSourceInvalid         = "SourceInvalid"
	reasonSourceNotFound        = "SourceNotFound"
	reasonSourceNotReady        = "SourceNotReady"
	reasonSourceRejected        = "SourceRejected"
	reasonCanonicalOccupied     = "CanonicalNameOccupied"
	reasonProjectionCollision   = "ProjectionCollision"
	reasonProjectionTerminating = "ProjectionTerminating"
	reasonNoRelease             = "NoRelease"
	reasonUnsupportedProjection = "UnsupportedProjectionType"
	reasonMultipleCACert        = "MultipleCACertProjections"
	reasonReleaseContested      = "ReleaseContested"
)

// privateKeyHeader matches a PEM private-key header of any flavour (PKCS#1,
// PKCS#8, EC, encrypted, OpenSSH, PGP). It is anchored to the header line and
// case-insensitive, so it neither false-positives on certificate body text
// that happens to contain the words "PRIVATE KEY" nor misses a lowercased
// hand-pasted header. Identical in intent to the chart-side guard in
// cozy-lib.tls.caCertSecret.
//
// It deliberately does NOT anchor on the closing "-----". Doing so reads as
// tighter and is strictly weaker: PGP's header is
// "-----BEGIN PGP PRIVATE KEY BLOCK-----", where BLOCK sits between "PRIVATE
// KEY" and the dashes, so a closing anchor lets the one flavour a human is most
// likely to paste by hand walk straight past the guard. Matching up to "PRIVATE
// KEY" and stopping catches every flavour, and gives up nothing: the prefix is
// already pinned to a PEM BEGIN line, which certificate body text cannot forge.
var privateKeyHeader = regexp.MustCompile(`(?i)-----BEGIN [A-Z0-9 ]*PRIVATE KEY`)

var (
	// errPrivateKey and errNotCertificate are the two fail-closed outcomes of
	// the write-path guard.
	errPrivateKey     = errors.New("value carries PEM private key material")
	errNotCertificate = errors.New("value is not a PEM certificate")

	// errProjectionTerminating marks a projection still being garbage-collected
	// — what deleting and recreating an application under the same name leaves
	// behind. Writing to a terminating object accomplishes nothing; the
	// reconciler waits for the collector and then creates a fresh projection.
	errProjectionTerminating = errors.New("CA projection is terminating")

	// errProjectionCollision marks a Secret at the canonical name that this
	// controller did not create. Overwriting it could destroy live key material,
	// so it is refused (and surfaced as an Event on the colliding object); the
	// sentinel's status reports it rather than the reconcile failing.
	errProjectionCollision = errors.New("canonical name occupied by a foreign Secret")
)

// Reconciler publishes the trust anchor declared by a TenantProjection sentinel
// as a canonical, key-free "<release>.tenant-ca" Secret in the sentinel's
// namespace.
type Reconciler struct {
	client.Client
	// Reader is the manager's uncached APIReader. The source Secret and the
	// projection are read through it, not the cache: the manager's Secret
	// informer is metadata-only (SetupWithManager), so it holds no key material
	// and cannot answer a data read, and the collision guard must not be fooled
	// by a cache miss on a Secret that squats on the projection's name.
	Reader client.Reader
	// Recorder surfaces every refusal — key material under the lifted key, a
	// malformed certificate, a name collision — as a Warning Event, so an
	// otherwise silent skip is visible with `kubectl get events -n <namespace>`.
	Recorder record.EventRecorder
}

// application identifies the application instance a release belongs to, as the
// cozystack API stamps it on every HelmRelease it creates.
type application struct {
	Group string
	Kind  string
	Name  string
}

// +kubebuilder:rbac:groups="",resources=secrets,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups="",resources=events,verbs=create;patch
// +kubebuilder:rbac:groups=helm.toolkit.fluxcd.io,resources=helmreleases,verbs=get;list;watch
// +kubebuilder:rbac:groups=cozystack.io,resources=applicationdefinitions,verbs=get;list;watch
// +kubebuilder:rbac:groups=internal.cozystack.io,resources=tenantprojections,verbs=get;list;watch
// +kubebuilder:rbac:groups=internal.cozystack.io,resources=tenantprojections/status,verbs=get;update;patch

// Reconcile publishes the trust anchor declared by one TenantProjection.
//
// Everything that is not a transient API error resolves to a status condition
// and, where a human must act, a Warning Event: an unpopulated source, a
// poisoned value and a name collision must never wedge the workqueue, and must
// never overwrite an object the controller does not own.
func (r *Reconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	tp := &internalv1alpha1.TenantProjection{}
	if err := r.Get(ctx, req.NamespacedName, tp); err != nil {
		if apierrors.IsNotFound(err) {
			// The sentinel is gone. The projection is owner-referenced to it, so
			// the garbage collector removes it — there is no prune logic here, by
			// design.
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, fmt.Errorf("get TenantProjection: %w", err)
	}
	if tp.DeletionTimestamp != nil {
		return ctrl.Result{}, nil
	}

	release := tp.Labels[helmNameLabel]
	if release == "" {
		// A sentinel Flux did not render carries no release, so no canonical
		// trust-anchor name can be derived. Report it and stop; a re-render adds
		// the label and wakes the For() watch.
		if err := r.setReady(ctx, tp, notReady(reasonNoRelease,
			fmt.Sprintf("sentinel carries no %s label; cannot derive the trust-anchor name", helmNameLabel))); err != nil {
			return ctrl.Result{}, err
		}
		return ctrl.Result{}, nil
	}

	// The selectors digest forces the lineage webhook to re-admit the projection
	// when the definition's spec.secrets — which decide tenant visibility —
	// change; see SelectorsDigestAnnotation. It is not needed to project the CA
	// itself, so a release whose HelmRelease or definition cannot be resolved is
	// still projected, with an empty digest that fills in once they appear.
	digest, err := r.selectorsDigestForRelease(ctx, tp.Namespace, release)
	if err != nil {
		return ctrl.Result{}, err
	}

	target := release + projectionSuffix

	// Every CACert projection resolves to the single, release-derived canonical
	// name, so more than one would overwrite the others on each pass — silently,
	// and with the last entry winning while every reconcile churned the write.
	// Refuse the whole sentinel rather than publish an arbitrary one of them. Only
	// CACert exists today, so this is the sole way two entries can collide; a
	// future projection type with its own target name would not.
	caCertEntries := 0
	for i := range tp.Spec.Projections {
		if tp.Spec.Projections[i].Type == internalv1alpha1.ProjectionTypeCACert {
			caCertEntries++
		}
	}
	if caCertEntries > 1 {
		// No requeue: a chart re-render fixing the declaration wakes the For()
		// watch, and nothing self-heals a sentinel that declares two of them.
		if err := r.setReady(ctx, tp, notReady(reasonMultipleCACert,
			fmt.Sprintf("%d CACert projections declared; all resolve to the single canonical name %q, so none is published — declare at most one", caCertEntries, target))); err != nil {
			return ctrl.Result{}, err
		}
		return ctrl.Result{}, nil
	}

	if caCertEntries == 0 {
		// No CACert entry declares the trust anchor any more. The CRD's MinItems=1
		// keeps a sentinel from emptying its projections list, so this is reached
		// when a FUTURE non-CACert entry replaces the CACert one — the list stays
		// non-empty while the anchor's declaration is gone. Withdraw a projection a
		// previous declaration published: garbage collection only fires when the
		// whole sentinel is pruned, so a sentinel that keeps existing with its CACert
		// entry removed would otherwise serve a stale anchor forever. Only a
		// projection this sentinel owns is deleted; a foreign Secret, or another
		// sentinel's projection at the name, is left untouched.
		if err := r.withdrawProjection(ctx, tp, target); err != nil {
			return ctrl.Result{}, err
		}
	} else {
		// Exactly one CACert entry. Refuse if another sentinel in this namespace
		// declares one for the same release: both resolve to the single canonical
		// name, so publishing a NEW anchor from either would silently pick an
		// arbitrary winner and let the two flap ownership of one projection, while
		// deleting either would garbage-collect an anchor the other still declares.
		// Neither writes a fresh projection until the declaration lives on exactly
		// one sentinel — a projection an earlier, uncontested reconcile already
		// published is left in place, not deleted, because withdrawing an anchor is
		// worse than leaving a stale one visible next to the contest condition. This
		// mirrors the more-than-one-CACert-entry refusal above, one level up.
		//
		// Requeue on the resync interval, unlike the other refusals. NoRelease and
		// MultipleCACert self-heal because the fix edits THIS sentinel, which the
		// For() watch delivers; a contest is cleared by deleting the SIBLING
		// sentinel, and nothing enqueues this one when a sibling changes. Without the
		// requeue the surviving sentinel would not publish until the informer's
		// global resync (hours), so the anchor could stay withheld long after the
		// operator removed the duplicate.
		other, err := r.anotherSentinelForRelease(ctx, tp, release)
		if err != nil {
			return ctrl.Result{}, err
		}
		if other != "" {
			if err := r.setReady(ctx, tp, notReady(reasonReleaseContested,
				fmt.Sprintf("another TenantProjection %q in this namespace declares a CACert projection for release %q; both resolve to the canonical name %q, so no new anchor is published — declare the trust anchor from exactly one sentinel", other, release, target))); err != nil {
				return ctrl.Result{}, err
			}
			return ctrl.Result{RequeueAfter: resyncInterval}, nil
		}
	}

	// The sentinel's Ready condition aggregates its projections: True only when
	// every declared projection is published, otherwise the first failure.
	ready := metav1.Condition{
		Status:  metav1.ConditionTrue,
		Reason:  reasonProjected,
		Message: "all declared projections are published",
	}
	for i := range tp.Spec.Projections {
		entry := &tp.Spec.Projections[i]
		if entry.Type != internalv1alpha1.ProjectionTypeCACert {
			// The CRD enum admits only CACert, so this is unreachable through the
			// API; it stays as the explicit answer if the enum is ever widened
			// before the code that handles the new type lands.
			if ready.Status == metav1.ConditionTrue {
				ready = notReady(reasonUnsupportedProjection,
					fmt.Sprintf("projection type %q is not supported", entry.Type))
			}
			continue
		}
		cond, err := r.reconcileCACert(ctx, tp, target, digest, entry)
		if err != nil {
			return ctrl.Result{}, err
		}
		if cond.Status != metav1.ConditionTrue && ready.Status == metav1.ConditionTrue {
			ready = cond
		}
	}

	if err := r.setReady(ctx, tp, ready); err != nil {
		return ctrl.Result{}, err
	}
	return ctrl.Result{RequeueAfter: resyncInterval}, nil
}

// reconcileCACert resolves one CACert entry's source and publishes it, returning
// the Ready sub-state the entry contributes. A returned error is transient (the
// caller requeues); every business outcome is a condition.
func (r *Reconciler) reconcileCACert(ctx context.Context, tp *internalv1alpha1.TenantProjection, target, digest string, entry *internalv1alpha1.TenantProjectionEntry) (metav1.Condition, error) {
	ns := tp.Namespace
	sourceName := entry.SourceSecretName
	key := strings.TrimSpace(entry.SourceKey)
	if key == "" {
		key = caCertKey
	}

	if sourceName == "" {
		// The CRD pins sourceSecretName to MinLength=1, so a live apiserver rejects
		// this at admission; an object admitted by an older CRD can still reach here.
		// Resolve it to a condition rather than a Get: an empty name makes the
		// apiserver return "resource name may not be empty", which is not NotFound,
		// so it would escape the not-found branch below and wedge the reconcile on
		// backoff with no status ever written — the silent retry the sentinel exists
		// to make visible.
		return notReady(reasonSourceInvalid,
			"the projection declares an empty sourceSecretName; no source Secret can be resolved"), nil
	}

	secret := &corev1.Secret{}
	if err := r.Reader.Get(ctx, types.NamespacedName{Namespace: ns, Name: sourceName}, secret); err != nil {
		if apierrors.IsNotFound(err) {
			// The declared source has not been created yet — the normal state
			// between an application appearing and its operator minting the CA.
			// The source is watched, so its creation wakes the reconciler; this
			// makes the wait visible rather than silent.
			return notReady(reasonSourceNotFound,
				fmt.Sprintf("source Secret %q does not exist in namespace %q", sourceName, ns)), nil
		}
		return metav1.Condition{}, fmt.Errorf("get CA source %s/%s: %w", ns, sourceName, err)
	}

	if sourceName == target {
		// The source itself sits at the canonical projection name — a Secret
		// cannot be projected over itself.
		return r.reconcileSourceAtCanonicalName(secret, target), nil
	}

	value, ok := secret.Data[key]
	if !ok || len(bytes.TrimSpace(value)) == 0 {
		// The source exists but the CA is not written yet — operators create the
		// Secret before they populate it. The watch delivers the write.
		return notReady(reasonSourceNotReady,
			fmt.Sprintf("source Secret %q does not carry key %q yet", sourceName, key)), nil
	}

	if err := r.upsertProjection(ctx, tp, target, digest, secret, value); err != nil {
		switch {
		case errors.Is(err, errPrivateKey):
			r.warn(secret, reasonPrivateKeyRefused,
				"key %q carries PEM private key material; refusing to publish it as a trust anchor", key)
			return notReady(reasonSourceRejected,
				fmt.Sprintf("key %q in source Secret %q carries private key material", key, sourceName)), nil
		case errors.Is(err, errNotCertificate):
			r.warn(secret, reasonInvalidCACert,
				"key %q does not carry a PEM certificate; refusing to publish it as a trust anchor", key)
			return notReady(reasonSourceRejected,
				fmt.Sprintf("key %q in source Secret %q does not carry a PEM certificate", key, sourceName)), nil
		case errors.Is(err, errProjectionTerminating):
			return notReady(reasonProjectionTerminating,
				fmt.Sprintf("the previous projection %q is still being garbage-collected", target)), nil
		case errors.Is(err, errProjectionCollision):
			return notReady(reasonProjectionCollision,
				fmt.Sprintf("a foreign Secret already occupies the projection name %q", target)), nil
		}
		return metav1.Condition{}, err
	}
	return metav1.Condition{
		Status:  metav1.ConditionTrue,
		Reason:  reasonProjected,
		Message: fmt.Sprintf("published the trust anchor to %q", target),
	}, nil
}

// withdrawProjection deletes the canonical trust-anchor Secret when the sentinel
// no longer declares one, but ONLY when the Secret is one this sentinel owns.
// Deleting the whole sentinel is handled by garbage collection through the owner
// reference; this covers the narrower case of a sentinel that keeps existing with
// its CACert declaration removed, which GC never sees. A foreign Secret at the
// name, or a projection another sentinel owns, is never deleted.
//
// Ownership is decided by isOurProjection — the SAME name-match adoption uses —
// not by an exact owner-reference match. The two must answer "ours?" the same way,
// and "safe to overwrite" is the stronger claim: a projection this reconcile would
// adopt and rewrite it must also be willing to withdraw. An exact match is strictly
// too strict here, and in the wrong direction. It walks away from a projection left
// by a previous incarnation of the application (right name, this sentinel's name in
// the owner reference, the PREVIOUS UID) — which adoption re-homes but GC never
// collects, so the retired anchor would stay tenant-readable forever — and from a
// projection whose BlockOwnerDeletion flag has drifted, the very drift the adoption
// path normalizes.
func (r *Reconciler) withdrawProjection(ctx context.Context, tp *internalv1alpha1.TenantProjection, target string) error {
	existing := &corev1.Secret{}
	if err := r.Reader.Get(ctx, types.NamespacedName{Namespace: tp.Namespace, Name: target}, existing); err != nil {
		if apierrors.IsNotFound(err) {
			return nil
		}
		return fmt.Errorf("get CA projection %s/%s: %w", tp.Namespace, target, err)
	}
	if !isOurProjection(existing, tp) {
		return nil
	}
	if err := r.Delete(ctx, existing); err != nil && !apierrors.IsNotFound(err) {
		return fmt.Errorf("withdraw CA projection %s/%s: %w", tp.Namespace, target, err)
	}
	return nil
}

// anotherSentinelForRelease returns the name of a DIFFERENT sentinel in the same
// namespace that carries the same release label and also declares a CACert
// projection, or "" when this sentinel is the only one for the release. Two such
// sentinels resolve to the same canonical projection name, so the reconcile refuses
// to publish from either; see the caller.
func (r *Reconciler) anotherSentinelForRelease(ctx context.Context, tp *internalv1alpha1.TenantProjection, release string) (string, error) {
	list := &internalv1alpha1.TenantProjectionList{}
	if err := r.List(ctx, list, client.InNamespace(tp.Namespace)); err != nil {
		return "", fmt.Errorf("list TenantProjections in %s: %w", tp.Namespace, err)
	}
	for i := range list.Items {
		other := &list.Items[i]
		if other.Name == tp.Name || other.DeletionTimestamp != nil {
			continue
		}
		if other.Labels[helmNameLabel] != release {
			continue
		}
		for j := range other.Spec.Projections {
			if other.Spec.Projections[j].Type == internalv1alpha1.ProjectionTypeCACert {
				return other.Name, nil
			}
		}
	}
	return "", nil
}

// reconcileSourceAtCanonicalName handles the degenerate case where the source
// itself sits at the canonical projection name — a Secret cannot be projected
// over itself.
//
// The canonical name is chosen so that no operator the platform ships claims
// it, so this is not reachable through any of them today; it is reachable
// through a misconfiguration (a sentinel pointing sourceSecretName at
// "<release>.tenant-ca" itself), and the answer depends entirely on content.
//
// A key-FREE Secret there is already the trust anchor the tenant needs: there
// is nothing to extract, it is not the controller's to adopt, and that is a
// silent success. A key-BEARING Secret there is the dangerous shape: the
// canonical name is occupied by an object a tenant must never read, and no
// projection could be written without destroying live key material. Refuse it
// loudly — never overwrite, never adopt.
func (r *Reconciler) reconcileSourceAtCanonicalName(src *corev1.Secret, target string) metav1.Condition {
	for _, key := range sortedKeys(src.Data) {
		if containsPrivateKey(string(src.Data[key])) {
			r.warn(src, reasonCanonicalNameOccupied,
				"the Secret %q occupies the canonical trust-anchor name and carries private key material under %q; no trust anchor is published for this release until that Secret is renamed",
				target, key)
			return notReady(reasonCanonicalOccupied,
				fmt.Sprintf("the canonical name %q is occupied by a Secret carrying private key material", target))
		}
	}

	// Key-free and already canonical: the engine publishes its own trust anchor,
	// there is nothing to extract, and the Secret is not the controller's to
	// adopt or rewrite.
	//
	// But deciding to leave it alone hands the canonical name to the tenant
	// unexamined, and that name is a promise with a specific shape: an Opaque
	// Secret carrying ca.crt and nothing else, which the shared write path
	// GUARANTEES for every projection it writes. Here the object belongs to the
	// engine, so the controller can only check it — and the one thing it must not
	// do is skip the check and imply it passed.
	if deviation := canonicalContractDeviation(src); deviation != "" {
		r.warn(src, reasonCanonicalNameContract,
			"the Secret %q occupies the canonical trust-anchor name but %s; it is left untouched, so it does not meet the clean, tenant-readable ca.crt that name promises",
			target, deviation)
	}

	return metav1.Condition{
		Status:  metav1.ConditionTrue,
		Reason:  reasonProjected,
		Message: fmt.Sprintf("the source already sits at the canonical name %q and is key-free", target),
	}
}

// canonicalContractDeviation describes how a Secret sitting at the canonical
// trust-anchor name departs from the contract that name carries, or "" when it
// satisfies it. Key material is NOT its business: the caller has already refused
// that, louder and for a different reason.
//
// The contract is not only about SHAPE. The name promises a clean ca.crt the
// TENANT can read, and tenant visibility comes from the internal.cozystack.io/tenant-ca
// label: the lineage webhook stamps tenantresource=false on a Secret that lacks it,
// so an engine-owned anchor at the canonical name is locked away from the tenant
// until it is labelled. A key-free Secret of the right shape but missing that label
// is "published" only in name, so the label is checked alongside type and keys.
//
// All deviations are collected, not just the first, so the warning names every way
// the object falls short in one pass.
func canonicalContractDeviation(s *corev1.Secret) string {
	var deviations []string
	if s.Type != corev1.SecretTypeOpaque {
		deviations = append(deviations, fmt.Sprintf("is of type %q rather than %q", s.Type, corev1.SecretTypeOpaque))
	}
	// Reuse the write path's own guard, so "is this a trust anchor?" is answered
	// by one piece of code wherever it is asked.
	if _, err := projectionData(s.Data[caCertKey]); err != nil {
		deviations = append(deviations, fmt.Sprintf("its %q does not carry a usable trust anchor (%v)", caCertKey, err))
	}
	var extra []string
	for _, key := range sortedKeys(s.Data) {
		if key != caCertKey {
			extra = append(extra, key)
		}
	}
	if len(extra) > 0 {
		deviations = append(deviations, fmt.Sprintf("carries %s besides %q", strings.Join(extra, ", "), caCertKey))
	}
	if s.Labels[TenantCALabel] != trueValue {
		deviations = append(deviations, fmt.Sprintf("is missing the %q label the tenant read path selects on, so no tenant can read it", TenantCALabel))
	}
	return strings.Join(deviations, "; and ")
}

// sortedKeys returns a map's keys in a stable order, so an event message reads
// the same on every reconcile rather than reshuffling with Go's map iteration.
func sortedKeys(m map[string][]byte) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// applicationDefinition returns the ApplicationDefinition for an application
// kind, or nil when none is registered.
//
// The list is walked in the API server's unspecified order, so the match is
// gated on the group as well as the kind: every ApplicationDefinition describes a
// kind in appsGroup, and the release's own group label must be that group, so
// requiring it keeps a release stamped with an unexpected group from resolving a
// same-kind definition by accident. ApplicationDefinition carries no group field
// of its own — the group is invariant across all of them — so the check is
// against the constant.
func (r *Reconciler) applicationDefinition(ctx context.Context, app application) (*cozyv1alpha1.ApplicationDefinition, error) {
	if app.Group != appsGroup {
		return nil, nil
	}
	list := &cozyv1alpha1.ApplicationDefinitionList{}
	if err := r.List(ctx, list); err != nil {
		return nil, fmt.Errorf("list ApplicationDefinitions: %w", err)
	}
	for i := range list.Items {
		if list.Items[i].Spec.Application.Kind == app.Kind {
			return &list.Items[i], nil
		}
	}
	return nil, nil
}

// selectorsDigestForRelease resolves the release's ApplicationDefinition through
// its HelmRelease and digests its spec.secrets. It is best-effort: a release
// whose HelmRelease or definition is not resolvable yet gets an empty digest,
// which fills in — forcing exactly one re-admission — once they appear. Only a
// transient API error is returned; a not-yet-present HelmRelease or definition
// is not one.
func (r *Reconciler) selectorsDigestForRelease(ctx context.Context, namespace, release string) (string, error) {
	hr := &helmv2.HelmRelease{}
	if err := r.Get(ctx, types.NamespacedName{Namespace: namespace, Name: release}, hr); err != nil {
		if apierrors.IsNotFound(err) {
			return "", nil
		}
		return "", fmt.Errorf("get HelmRelease %s/%s: %w", namespace, release, err)
	}
	app, ok := applicationOf(hr)
	if !ok {
		return "", nil
	}
	def, err := r.applicationDefinition(ctx, app)
	if err != nil {
		return "", err
	}
	if def == nil {
		return "", nil
	}
	return selectorsDigest(def)
}

// upsertProjection creates or refreshes the canonical trust-anchor Secret. This
// is the only place bytes reach a projection, and the guard in projectionData
// runs on exactly the bytes about to be written.
func (r *Reconciler) upsertProjection(ctx context.Context, tp *internalv1alpha1.TenantProjection, target, digest string, sourceSecret *corev1.Secret, value []byte) error {
	desired, err := projectionData(value)
	if err != nil {
		return err
	}
	owner := sentinelOwnerRef(tp)
	ref := sourceSecret.Namespace + "/" + sourceSecret.Name
	ns := tp.Namespace

	existing := &corev1.Secret{}
	err = r.Reader.Get(ctx, types.NamespacedName{Namespace: ns, Name: target}, existing)
	switch {
	case apierrors.IsNotFound(err):
		projection := &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Namespace: ns,
				Name:      target,
				Labels:    map[string]string{TenantCALabel: trueValue, ManagedLabel: trueValue},
				Annotations: map[string]string{
					SourceRefAnnotation:       ref,
					SelectorsDigestAnnotation: digest,
				},
				OwnerReferences: []metav1.OwnerReference{owner},
			},
			Type: corev1.SecretTypeOpaque,
			Data: desired,
		}
		if err := r.Create(ctx, projection); err != nil {
			return fmt.Errorf("create CA projection %s/%s: %w", ns, target, err)
		}
		return nil
	case err != nil:
		return fmt.Errorf("get CA projection %s/%s: %w", ns, target, err)
	}

	// Adoption keys on the OWNER REFERENCE back to this sentinel, not on the marker
	// label. The marker is an ordinary label any Secret writer in the namespace can
	// forge OR STRIP, and keying adoption on it fails in the dangerous direction: an
	// actor who strips the marker off the GENUINE projection and writes their own
	// bytes under ca.crt would make the controller disown its own object, refuse to
	// touch it as a "collision", and keep serving the forged anchor forever. The
	// owner reference is the relationship this controller actually established;
	// isOurProjection matches it by the sentinel's NAME so a delete-and-recreate of
	// the application (same name, new UID) is still recognised as ours to re-home.
	//
	// Forging the owner reference does not help an attacker: it makes the object
	// adoptable, and adoption OVERWRITES it with the genuine key-free CA and the
	// genuine owner reference. That is the self-healing direction — the forged bytes
	// are destroyed, not served. A Secret with NO owner reference back to this
	// sentinel is a stranger, whatever its labels say, and is refused as a collision
	// rather than overwritten, because it may hold live key material this controller
	// must never destroy — and a stranger carries no owner chain to an application,
	// so the lineage webhook cannot mark it tenantresource=true and no tenant reads
	// it while the collision stands visible for an admin to clear.
	//
	// The Opaque type stays as a cheap sanity guard inside isOurProjection — every
	// projection this controller creates is Opaque — but it is no longer
	// load-bearing. The update path below is a read-modify-write that never writes
	// Type, so adopting a non-Opaque Secret could not self-DoS on Secret.type's
	// immutability; the guard just declines to treat an object of the wrong shape as
	// one of ours.
	if !isOurProjection(existing, tp) {
		// A Secret of this name exists that the controller did not create.
		// Overwriting it could destroy live key material, so refuse and surface
		// the refusal on the colliding object itself.
		r.warn(existing, reasonCollision,
			"a Secret named %q already exists and was not created by the CA extraction controller; the trust anchor is not published until that Secret is removed",
			target)
		return errProjectionCollision
	}

	if existing.DeletionTimestamp != nil {
		// Do not write to an object the garbage collector is already removing:
		// the write would land and then be thrown away with it.
		return errProjectionTerminating
	}

	// The selectors digest is part of the drift check, not decoration: it is what
	// makes a change to spec.secrets — the selectors that decide whether the tenant
	// may read this projection at all — produce a write. See
	// SelectorsDigestAnnotation.
	selectorsChanged := existing.Annotations[SelectorsDigestAnnotation] != digest
	if maps.EqualFunc(existing.Data, desired, bytes.Equal) &&
		existing.Labels[TenantCALabel] == trueValue &&
		existing.Labels[ManagedLabel] == trueValue &&
		existing.Annotations[SourceRefAnnotation] == ref &&
		!selectorsChanged &&
		ownedSolelyBy(existing.OwnerReferences, owner) {
		return nil
	}

	// Merge, never replace: the lineage admission webhook stamps its own labels
	// (managed-by-cozystack, tenantresource, application.*) on the projection, and
	// replacing the map wholesale would strip the tenantresource label the tenant's
	// read path depends on.
	if existing.Labels == nil {
		existing.Labels = map[string]string{}
	}
	existing.Labels[TenantCALabel] = trueValue
	existing.Labels[ManagedLabel] = trueValue
	if existing.Annotations == nil {
		existing.Annotations = map[string]string{}
	}
	existing.Annotations[SourceRefAnnotation] = ref
	existing.Annotations[SelectorsDigestAnnotation] = digest

	if selectorsChanged {
		// Hand the projection BACK to the lineage webhook, by dropping the marker
		// that keeps the webhook away from it.
		//
		// Forcing a write is not enough, and believing it was is the hole this
		// closes. The webhook is registered with objectSelector
		// managed-by-cozystack DoesNotExist, and Kubernetes evaluates
		// objectSelector against BOTH oldObject and newObject, running the webhook
		// only if EITHER matches. The webhook stamps that marker in the same pass
		// as its tenantresource verdict, so from the first admission onward both
		// objects carry it, neither matches DoesNotExist, and every subsequent
		// UPDATE is skipped — however many the digest forces. The verdict would
		// then be frozen at whatever the CREATE-time selectors said, forever.
		//
		// Deleting the marker makes newObject match, so this one write is admitted
		// and the webhook recomputes tenantresource from the CURRENT selectors and
		// re-stamps the marker along with it. The next reconcile sees a matching
		// digest and writes nothing, so it settles after exactly one pass.
		//
		// Both directions of the verdict depend on this, and the dangerous one is
		// REVOCATION: a definition that stops selecting the trust anchor must take
		// the tenant's read access away, and without re-admission tenantresource
		// stays "true" and the tenant keeps reading an anchor the platform has
		// withdrawn. Granting merely fails to arrive; revoking fails silently open.
		//
		// It is safe to remove: the webhook is failurePolicy Fail, so if it cannot
		// be reached the UPDATE is rejected and the reconciler retries with
		// backoff. The projection is never left stored without its labels.
		//
		// Only a SELECTOR change does this. A data rotation cannot alter the
		// webhook's verdict, so re-admitting on one would be pure load.
		delete(existing.Labels, managedByCozystackLabel)
	}
	if !ownedSolelyBy(existing.OwnerReferences, owner) {
		// REPLACE, never append, and replace down to EXACTLY one reference.
		//
		// Appending is wrong twice over. The API server rejects a second reference
		// with Controller=true outright ("Only one reference can have Controller
		// set to true"), and a stale reference is exactly what deleting and
		// recreating an application under the same name leaves behind — same
		// sentinel name, new UID — so appending would wedge the reconciler on an
		// Invalid error instead of re-homing the projection onto the live sentinel.
		//
		// Keeping any OTHER reference is wrong too, which is why this asks
		// ownedSolelyBy and not hasOwner: garbage collection deletes a dependent
		// only once EVERY owner is gone, so one extra reference outlives the
		// sentinel and keeps the projection — a trust anchor for an application
		// that no longer exists — readable in the tenant's namespace. Nothing
		// legitimate co-owns a projection: this controller creates it with one
		// owner, and the lineage webhook stamps labels, not references.
		existing.OwnerReferences = []metav1.OwnerReference{owner}
	}
	// Data is REPLACED, not merged: the projection holds ca.crt and nothing else,
	// on every write, whatever it held before.
	//
	// Type is deliberately NOT written. It is immutable in Kubernetes, so writing
	// it can only ever be a no-op or an Invalid error — and the adoption gate above
	// has already established that this object is Opaque.
	existing.Data = desired
	if err := r.Update(ctx, existing); err != nil {
		return fmt.Errorf("update CA projection %s/%s: %w", ns, target, err)
	}
	return nil
}

// isOurProjection reports whether the Secret at the canonical name is one this
// controller created — and so is safe to adopt and overwrite with the genuine
// trust anchor.
//
// The signal is the OWNER REFERENCE back to a TenantProjection of this sentinel's
// name, not the ManagedLabel marker. The marker is an ordinary label any Secret
// writer in the namespace can forge or STRIP, so keying adoption on it fails in
// the dangerous direction: stripping it off the genuine projection would make the
// controller disown its own object and keep serving whatever bytes the same actor
// wrote. The owner reference is the relationship this controller actually
// established; an actor can forge one, but forging it only causes an
// adopt-and-overwrite with the genuine key-free CA — the self-healing direction —
// so it grants nothing.
//
// The reference is matched by NAME rather than by exact UID on purpose: a
// delete-and-recreate of the application leaves the projection owner-referenced to
// the PREVIOUS sentinel incarnation (same name, new UID) until the garbage
// collector catches up, and that stale projection is ours to re-home, not a
// stranger to refuse. Owner references are namespaced and a TenantProjection name
// is unique in its namespace, so a name match is an identity match.
//
// The Opaque type stays as a cheap sanity guard — every projection this controller
// creates is Opaque — but it is no longer load-bearing: the update path is a
// read-modify-write that never writes Type, so it could not self-DoS on an adopted
// non-Opaque Secret even without this check.
func isOurProjection(s *corev1.Secret, tp *internalv1alpha1.TenantProjection) bool {
	if s.Type != corev1.SecretTypeOpaque {
		return false
	}
	for i := range s.OwnerReferences {
		ref := &s.OwnerReferences[i]
		if ref.Kind == "TenantProjection" &&
			ref.APIVersion == internalv1alpha1.GroupVersion.String() &&
			ref.Name == tp.Name {
			return true
		}
	}
	return false
}

// selectorsDigest digests the ApplicationDefinition's spec.secrets — the
// selectors the lineage webhook uses to decide whether the projection is visible
// to the tenant. It is the drift signal that forces a re-admission when tenant
// visibility changes; see SelectorsDigestAnnotation.
//
// The full digest is stored, not a truncated prefix. A collision skips exactly the
// re-admission the doc calls load-bearing — the revocation direction, where a
// tenant would silently keep read access after a definition stopped selecting the
// anchor — and the annotation carries no length limit, so the full hash is free
// hardening on the path that matters most.
func selectorsDigest(def *cozyv1alpha1.ApplicationDefinition) (string, error) {
	encoded, err := json.Marshal(def.Spec.Secrets)
	if err != nil {
		return "", fmt.Errorf("digest spec.secrets of ApplicationDefinition %s: %w", def.Name, err)
	}
	sum := sha256.Sum256(encoded)
	return hex.EncodeToString(sum[:]), nil
}

// projectionData builds the projection payload and is the single write-path
// guard. It emits exactly one key — the canonical ca.crt — whose value is
// REBUILT from the certificate blocks it validated, never copied from the
// input. It refuses anything that carries a PEM private-key header or is not a
// PEM certificate. Every byte written to a projection passes through here, on
// create and on update.
//
// The two checks run in this order deliberately. The key guard is the one whose
// failure is a security incident and it must own that verdict: a private key
// wrapped in certificate armour has to be refused AS key material, with the
// event that says so, not as a parse failure.
//
// Rebuilding the value from the parsed blocks — instead of cloning the input
// once it validates — is what makes the guard airtight, and it closes a real
// hole. The key guard above matches a PEM "-----BEGIN ... PRIVATE KEY-----"
// header; a RAW DER or JWK private key wears no such header, so it walks past.
// pem.Decode then SKIPS bytes before the first block and between blocks, so
// that key never reaches the certificate loop either. A projection cloned from
// the input would therefore hand the tenant a trust anchor with a private key
// tucked in front of, or between, the certificates. A projection assembled only
// from the re-encoded certificate DER cannot carry a byte the guard did not
// parse as a certificate.
func projectionData(value []byte) (map[string][]byte, error) {
	if containsPrivateKey(string(value)) {
		return nil, errPrivateKey
	}
	chain, err := certificateChainPEM(value)
	if err != nil {
		return nil, err
	}
	return map[string][]byte{caCertKey: chain}, nil
}

// certificateChainPEM validates that value is a PEM sequence of nothing but
// certificates that x509 accepts, and returns those certificates re-encoded as
// a clean PEM chain — the exact bytes the projection publishes.
//
// A header match is not this check, and the difference is the guard's whole
// worth. "-----BEGIN CERTIFICATE-----" around a truncated DER prefix, around
// base64 of an English sentence, or around nothing at all satisfies a regex and
// is not a certificate: the projection would be handed to the tenant as a
// vouched trust anchor carrying bytes no verifier can load. So every block is
// DECODED and PARSED, and a value is a trust anchor only if it is certificates
// all the way down:
//
//   - at least one block, because an empty value vouches for nothing;
//   - every block of type CERTIFICATE, so a PUBLIC KEY block — parseable, and
//     not a trust anchor — is refused with the rest;
//   - every block accepted by x509.ParseCertificate, which is the actual claim;
//   - nothing but PEM blocks left at the end, so a value cannot carry a
//     certificate and then trail arbitrary bytes past the first match.
//
// The returned chain is assembled from the PARSED certificate DER, not from the
// input. pem.Decode silently skips text before a block and between blocks — the
// human-readable preamble `openssl x509 -text` emits, but also a raw DER or JWK
// key that carries no PEM private-key header — so copying the input verbatim
// would republish those bytes to the tenant. Re-encoding only the certificates
// makes preamble and interstitial key material structurally unreachable; the
// trailing-byte rejection still keeps a value from parsing as a certificate and
// then trailing arbitrary bytes past the last block.
func certificateChainPEM(value []byte) ([]byte, error) {
	// "At least one block" is enforced HERE and only here: an empty value is
	// rejected up front, which is what guarantees the loop below runs at least
	// once, and every iteration either returns an error or parses a certificate.
	// A counter checked afterwards would be unreachable.
	rest := bytes.TrimSpace(value)
	if len(rest) == 0 {
		return nil, fmt.Errorf("%w: value is empty", errNotCertificate)
	}
	var chain []byte
	for len(rest) > 0 {
		var block *pem.Block
		block, rest = pem.Decode(rest)
		if block == nil {
			return nil, fmt.Errorf("%w: value carries %d bytes that are not a PEM block", errNotCertificate, len(rest))
		}
		if block.Type != certificatePEMType {
			return nil, fmt.Errorf("%w: PEM block is of type %q, want %q", errNotCertificate, block.Type, certificatePEMType)
		}
		if _, err := x509.ParseCertificate(block.Bytes); err != nil {
			return nil, fmt.Errorf("%w: %v", errNotCertificate, err)
		}
		// Re-encode ONLY the parsed certificate DER: no PEM headers, no
		// surrounding bytes, nothing the loop did not just verify is a
		// certificate. This is the byte-for-byte content of the projection.
		chain = append(chain, pem.EncodeToMemory(&pem.Block{
			Type:  certificatePEMType,
			Bytes: block.Bytes,
		})...)
		rest = bytes.TrimSpace(rest)
	}
	return chain, nil
}

// applicationOf reads the application identity the cozystack API stamps on every
// HelmRelease it creates. A HelmRelease without it is not an application release.
func applicationOf(hr *helmv2.HelmRelease) (application, bool) {
	app := application{
		Group: hr.Labels[appsv1alpha1.ApplicationGroupLabel],
		Kind:  hr.Labels[appsv1alpha1.ApplicationKindLabel],
		Name:  hr.Labels[appsv1alpha1.ApplicationNameLabel],
	}
	if app.Group == "" || app.Kind == "" || app.Name == "" {
		return application{}, false
	}
	return app, true
}

// containsPrivateKey reports whether a PEM blob carries private key material.
// Anchored to the header line: a certificate whose body or subject text
// mentions "PRIVATE KEY" is not a false positive.
func containsPrivateKey(pem string) bool {
	return privateKeyHeader.MatchString(pem)
}

// sentinelOwnerRef builds the owner reference the projection carries back to its
// TenantProjection sentinel. The sentinel — not the HelmRelease — is the owner:
// the lineage webhook resolves the application by walking owner references, and
// the sentinel's own helm.toolkit.fluxcd.io/name label carries that walk on to
// the HelmRelease (see the package doc). Deleting the sentinel garbage-collects
// the projection through this reference.
func sentinelOwnerRef(tp *internalv1alpha1.TenantProjection) metav1.OwnerReference {
	return metav1.OwnerReference{
		APIVersion: internalv1alpha1.GroupVersion.String(),
		Kind:       "TenantProjection",
		Name:       tp.Name,
		UID:        tp.UID,
		// The projection has exactly one manager, this controller.
		Controller: ptr.To(true),
		// BlockOwnerDeletion stays false: the trust anchor must never hold up the
		// teardown of the application it belongs to.
		BlockOwnerDeletion: ptr.To(false),
	}
}

// ownedSolelyBy reports whether refs is EXACTLY the desired controller reference
// and nothing besides.
//
// Presence is not the question, and it is not enough. Kubernetes garbage
// collection removes a dependent only once EVERY owner in its list is gone, and
// an extra reference is something any Secret writer in the namespace can append
// — the API server accepts it as long as it is not a second Controller=true. A
// projection carrying one therefore SURVIVES the deletion of the sentinel it
// vouches for, and that owner reference is the entire mechanism by which a
// retired trust anchor is collected. The tenant would be left reading the CA of
// an application that no longer exists.
//
// Requiring sole ownership turns that into drift, which the write path then
// normalizes away by replacing the list. The cost of being wrong is one extra
// write; the cost of accepting mere presence is a projection that outlives its
// sentinel, permanently.
func ownedSolelyBy(refs []metav1.OwnerReference, want metav1.OwnerReference) bool {
	return len(refs) == 1 && hasOwner(refs, want)
}

// hasOwner reports whether refs already carries the desired CONTROLLER reference.
//
// Both boolean flags are part of the comparison, not only the identity fields.
// The Controller flag is: a reference that names the right owner but is not
// marked as the controller (hand-edited, say) leaves the projection without a
// controlling owner, and treating it as a match would let the drift check pass
// and never re-home it. BlockOwnerDeletion is too, for the symmetric reason:
// sentinelOwnerRef pins it to false so the trust anchor never holds up the
// teardown of its application, and a reference that drifts it to true — which
// makes a foreground deletion of the sentinel block on the projection — must be
// re-homed back to false, not accepted as already-correct.
func hasOwner(refs []metav1.OwnerReference, want metav1.OwnerReference) bool {
	for _, ref := range refs {
		if ref.UID == want.UID && ref.Kind == want.Kind && ref.Name == want.Name &&
			ref.APIVersion == want.APIVersion &&
			ptr.Deref(ref.Controller, false) == ptr.Deref(want.Controller, false) &&
			ptr.Deref(ref.BlockOwnerDeletion, false) == ptr.Deref(want.BlockOwnerDeletion, false) {
			return true
		}
	}
	return false
}

// notReady builds a Ready=False condition; setReady fills in the type and
// observed generation.
func notReady(reason, message string) metav1.Condition {
	return metav1.Condition{
		Status:  metav1.ConditionFalse,
		Reason:  reason,
		Message: message,
	}
}

// setReady writes the Ready condition, skipping the API round-trip when nothing
// changed so a steady projection does not churn the status on every resync.
func (r *Reconciler) setReady(ctx context.Context, tp *internalv1alpha1.TenantProjection, cond metav1.Condition) error {
	cond.Type = conditionReady
	cond.ObservedGeneration = tp.Generation

	updated := tp.DeepCopy()
	updated.Status.ObservedGeneration = tp.Generation
	meta.SetStatusCondition(&updated.Status.Conditions, cond)

	if statusEqual(tp.Status, updated.Status) {
		return nil
	}
	tp.Status = updated.Status
	return r.Status().Update(ctx, tp)
}

// statusEqual reports whether two statuses are equal for the fields this
// controller sets, ignoring the condition's lastTransitionTime so an unchanged
// verdict is a no-op.
func statusEqual(a, b internalv1alpha1.TenantProjectionStatus) bool {
	if a.ObservedGeneration != b.ObservedGeneration {
		return false
	}
	if len(a.Conditions) != len(b.Conditions) {
		return false
	}
	for i := range a.Conditions {
		ac, bc := a.Conditions[i], b.Conditions[i]
		if ac.Type != bc.Type ||
			ac.Status != bc.Status ||
			ac.Reason != bc.Reason ||
			ac.Message != bc.Message ||
			ac.ObservedGeneration != bc.ObservedGeneration {
			return false
		}
	}
	return true
}

// warn records a Warning Event on obj, so a refusal is visible without reading
// controller logs.
func (r *Reconciler) warn(obj client.Object, reason, format string, args ...any) {
	log.Log.Info("CA extraction refused",
		"reason", reason, "object", obj.GetNamespace()+"/"+obj.GetName(), "detail", fmt.Sprintf(format, args...))
	if r.Recorder != nil {
		r.Recorder.Eventf(obj, corev1.EventTypeWarning, reason, format, args...)
	}
}

// secretGVK types the metadata-only Secret watch. The informer holds Secret
// metadata stubs cluster-wide, never the Data — no private key ever enters the
// cache — and the actual bytes are read on demand through the uncached Reader.
var secretGVK = schema.GroupVersionKind{Version: "v1", Kind: "Secret"}

// secretMeta returns a PartialObjectMetadata typed as a Secret for the
// metadata-only watch.
func secretMeta() *metav1.PartialObjectMetadata {
	obj := &metav1.PartialObjectMetadata{}
	obj.SetGroupVersionKind(secretGVK)
	return obj
}

// SetupWithManager wires the reconciler to the TenantProjection sentinels.
//
// The reconcile unit is the sentinel. Source Secrets are watched as metadata
// only — the informer holds stubs, not key material — and mapped back to the
// sentinels that reference them through a field index on
// spec.projections.sourceSecretName; the reconcile then reads the one source it
// needs through the uncached Reader. ApplicationDefinitions are watched so a
// change to the selectors that decide tenant visibility takes effect without
// waiting for the resync.
//
// The SAME metadata Secret cache also feeds a second mapping: the projection this
// controller writes is itself a Secret, and an in-place rewrite of its ca.crt (a
// tamper the owner-reference adoption gate then heals) must wake the reconciler at
// once, not five minutes later on the resync. The projection's name is
// "<release>.tenant-ca", which matches no sentinel's sourceSecretName, so the
// source index never enqueues it; projectionsForOwnedProjection resolves the
// owning sentinel from the projection's controller owner reference instead.
// Owns(&corev1.Secret{}) would be the obvious wiring and is a TRAP here: it
// registers against the MANAGER's cache, whose Secret informer is label-scoped to
// the WildcardSecret replicas (below), so a projection — which carries no wildcard
// label — is never delivered and the watch silently never fires.
//
// The Secret watch runs on secretMetaCache, a DEDICATED cache, NOT the manager's.
// It cannot use the manager's cache, and the reason is a controller-runtime trap
// rather than a preference: that cache scopes its v1/Secret informer to the
// WildcardSecret reconciler's replicas with a label selector (see main.go), a
// metadata informer for the same GVK routes to that same per-GVK cache
// (delegatingByGVKCache keys on GVK, and a PartialObjectMetadata typed as Secret
// resolves to v1/Secret), and the selector is applied to every informer in it —
// metadata included. On the manager cache this watch would therefore silently
// never fire for a CA source, which carries no wildcard label, and the projection
// would appear only on the slow resync. A dedicated cache holds Secret metadata
// stubs cluster-wide, unscoped and key-free, so every source's creation and
// rotation wakes the reconciler at once.
func (r *Reconciler) SetupWithManager(mgr ctrl.Manager, secretMetaCache cache.Cache) error {
	if err := mgr.GetFieldIndexer().IndexField(context.Background(), &internalv1alpha1.TenantProjection{}, sourceSecretNameField, indexBySourceSecretName); err != nil {
		return fmt.Errorf("index TenantProjection by source Secret name: %w", err)
	}

	return ctrl.NewControllerManagedBy(mgr).
		Named("cacert").
		For(&internalv1alpha1.TenantProjection{}).
		WatchesRawSource(crsource.Kind(secretMetaCache, secretMeta(),
			handler.TypedEnqueueRequestsFromMapFunc(r.projectionsForSourceSecret),
		)).
		WatchesRawSource(crsource.Kind(secretMetaCache, secretMeta(),
			handler.TypedEnqueueRequestsFromMapFunc(r.projectionsForOwnedProjection),
		)).
		Watches(&cozyv1alpha1.ApplicationDefinition{},
			handler.EnqueueRequestsFromMapFunc(r.projectionsForApplicationDefinition),
			builder.WithPredicates(applicationDefinitionPredicate),
		).
		Complete(r)
}

// indexBySourceSecretName indexes a TenantProjection by the source Secret names
// its entries reference, so a Secret event maps back to the sentinels that
// consume it in one cache lookup. Shared by SetupWithManager's field index and
// the tests that exercise the mapping.
func indexBySourceSecretName(obj client.Object) []string {
	tp, ok := obj.(*internalv1alpha1.TenantProjection)
	if !ok {
		return nil
	}
	names := make([]string, 0, len(tp.Spec.Projections))
	for i := range tp.Spec.Projections {
		if n := tp.Spec.Projections[i].SourceSecretName; n != "" {
			names = append(names, n)
		}
	}
	return names
}

// projectionsForSourceSecret maps a source Secret back to the sentinels that
// consume it, so a rotation (or the source's first appearance) propagates
// immediately. It takes the metadata stub the dedicated cache delivers, not a
// full Secret. The field index keeps this to a single cache lookup, and a Secret
// no sentinel references maps to nothing.
func (r *Reconciler) projectionsForSourceSecret(ctx context.Context, obj *metav1.PartialObjectMetadata) []reconcile.Request {
	list := &internalv1alpha1.TenantProjectionList{}
	if err := r.List(ctx, list,
		client.InNamespace(obj.GetNamespace()),
		client.MatchingFields{sourceSecretNameField: obj.GetName()},
	); err != nil {
		log.FromContext(ctx).Error(err, "map source Secret to its TenantProjections",
			"secret", obj.GetNamespace()+"/"+obj.GetName())
		return nil
	}
	out := make([]reconcile.Request, 0, len(list.Items))
	for i := range list.Items {
		out = append(out, reconcile.Request{NamespacedName: types.NamespacedName{
			Namespace: list.Items[i].Namespace, Name: list.Items[i].Name,
		}})
	}
	return out
}

// projectionsForOwnedProjection maps a Secret back to the sentinel that owns it as
// its published projection, so an in-place tamper of the canonical
// "<release>.tenant-ca" Secret's data is corrected at once rather than waiting for
// the slow resync. The projection's own name matches no sentinel's
// sourceSecretName, so projectionsForSourceSecret never enqueues it; the controller
// owner reference the projection carries is what identifies its sentinel. The
// reference lives in the object metadata, so the metadata stub the dedicated cache
// delivers is enough and no key material is read.
func (r *Reconciler) projectionsForOwnedProjection(_ context.Context, obj *metav1.PartialObjectMetadata) []reconcile.Request {
	var out []reconcile.Request
	for _, ref := range obj.GetOwnerReferences() {
		if ref.Kind == "TenantProjection" && ref.APIVersion == internalv1alpha1.GroupVersion.String() {
			out = append(out, reconcile.Request{NamespacedName: types.NamespacedName{
				Namespace: obj.GetNamespace(), Name: ref.Name,
			}})
		}
	}
	return out
}

// projectionsForApplicationDefinition maps an ApplicationDefinition change to
// every sentinel, so a change to the spec.secrets selectors that decide tenant
// visibility recomputes the selectors digest and re-admits a changed verdict at
// once. It fans out unconditionally by design — a definition serves every release
// of its kind, and this controller has no cheap index from a definition back to
// only the sentinels of that kind. applicationDefinitionPredicate is what keeps
// the fan-out rare: it gates the watch so only a change that can move the digest
// reaches this mapping at all.
func (r *Reconciler) projectionsForApplicationDefinition(ctx context.Context, _ client.Object) []reconcile.Request {
	list := &internalv1alpha1.TenantProjectionList{}
	if err := r.List(ctx, list); err != nil {
		log.FromContext(ctx).Error(err, "map ApplicationDefinition to TenantProjections")
		return nil
	}
	out := make([]reconcile.Request, 0, len(list.Items))
	for i := range list.Items {
		out = append(out, reconcile.Request{NamespacedName: types.NamespacedName{
			Namespace: list.Items[i].Namespace, Name: list.Items[i].Name,
		}})
	}
	return out
}

// applicationDefinitionPredicate gates the ApplicationDefinition watch so the
// unconditional fan-out above fires only when it can matter. Without it, Flux's
// periodic no-op re-apply of every *-rd definition would enqueue every sentinel in
// the cluster on each pass; the digest drift check then suppresses the write, but
// not the load — each such reconcile still issues several uncached reads.
//
// Create, delete and generic events keep the default pass-through: a definition
// appearing or disappearing can change a verdict. An UPDATE is delivered only when
// spec.secrets changed — the exact field selectorsDigest digests, so a change that
// cannot move the digest cannot pass this gate, and the two cannot drift apart.
var applicationDefinitionPredicate = predicate.Funcs{
	UpdateFunc: applicationDefinitionSecretsChanged,
}

// applicationDefinitionSecretsChanged reports whether an ApplicationDefinition
// UPDATE touched spec.secrets. A non-ApplicationDefinition event falls through as
// delivered rather than being silently dropped.
func applicationDefinitionSecretsChanged(e event.UpdateEvent) bool {
	oldDef, ok := e.ObjectOld.(*cozyv1alpha1.ApplicationDefinition)
	if !ok {
		return true
	}
	newDef, ok := e.ObjectNew.(*cozyv1alpha1.ApplicationDefinition)
	if !ok {
		return true
	}
	return !reflect.DeepEqual(oldDef.Spec.Secrets, newDef.Spec.Secrets)
}
