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
// holds ca.crt almost always holds a private key next to it: the
// cert-manager CA Secret carries tls.key, CloudNativePG's <release>-ca
// carries ca.key. Handing a tenant the trust anchor by granting read on one
// of those objects hands over key material too. The consume contract is
// therefore one canonical object per release — an Opaque Secret named
// "<release>.tenant-ca" holding ONLY ca.crt — and this controller produces
// it for every engine, so a tenant learns exactly one name.
//
// The canonical name is "<release>.tenant-ca", and the DOT is the whole point.
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
// The name stays symmetric with the internal.cozystack.io/tenant-ca label
// stamped on every projection; only the separator carries the guarantee.
//
// # Source discovery has two legs
//
// The reconcile unit is the application RELEASE (its HelmRelease), not the
// source Secret, because on one of the two legs the source does not exist
// yet — and may not exist for minutes — when the release appears.
//
// Which leg an engine uses is decided by its ApplicationDefinition, and the
// DECLARED leg wins: if spec.caCert is set, the engine's CA is taken only from
// that named source and the label leg is not consulted for it at all. The label
// leg serves the engines that declare nothing. This ordering is a security
// boundary — see resolveSource — not a stylistic default.
//
//  1. Label-driven. A Secret labelled
//     internal.cozystack.io/publish-ca-cert = "true" is a source, and
//     internal.cozystack.io/publish-ca-cert-release names the release it
//     belongs to; the optional annotation
//     internal.cozystack.io/publish-ca-cert-key names the key to lift. This is
//     how the cert-manager-minting charts opt in — all of it rides
//     Certificate.spec.secretTemplate.labels. Sources on this leg are watched,
//     so a rotation propagates immediately.
//
//     The release label is REQUIRED, not a convenience. A cert-manager-issued
//     Secret is attributable by nothing else: cert-manager sets no
//     OwnerReference (the platform ships enableCertificateOwnerRef=false) and
//     Helm did not create it, so it carries no Helm metadata — and because the
//     lineage webhook resolves ancestry through exactly those two, it stamps no
//     application.* labels on it either. Without the release label such a
//     source cannot be tied to any release, and its CA cannot be published.
//     That mistake is surfaced as a Warning Event on the Secret rather than
//     being dropped in silence.
//
//  2. Name-driven (declared, and authoritative when present). Some operators
//     create the CA Secret themselves and offer NO way to label it: CloudNativePG builds its PKI
//     Secrets without applying spec.inheritedMetadata, and Percona Server for
//     MongoDB does not expose secretTemplate at all and reconciles an
//     out-of-band patch away. For those engines the ApplicationDefinition
//     declares the source by name (spec.caCert), and the controller reads it
//     by name. Such a Secret carries no label the informer could select on,
//     so it is NOT watched: it is read through the uncached API reader and
//     re-read on a periodic resync, which is what carries a rotation through.
//
// The legs differ ONLY in how the source is found. Extraction, sanitization,
// the private-key guard, ownership and the collision rules are one shared
// write path — forking it is exactly where a key-leak bug would hide. On the
// name-driven leg the source is key-bearing BY CONSTRUCTION, so the guard
// matters more there, not less.
//
// # Load-bearing behaviours
//
//  1. Fail closed on key material. projectionData is the only way bytes reach
//     a projection, and it re-asserts on EVERY write that the value carries a
//     PEM certificate and no PEM private-key header. The check mirrors the
//     chart-side guard in cozy-lib.tls.caCertSecret
//     (packages/library/cozy-lib/templates/_tls.tpl), so both ends of the
//     contract reject the same inputs.
//  2. Sanitize at write time. Exactly one whitelisted key is emitted, under
//     the canonical name, and its value is rebuilt from the certificate blocks
//     the guard parsed rather than copied from the source. The source Data map
//     is never copied wholesale — that is the precise bug this controller
//     exists to prevent.
//  3. Tolerate asynchronous sources. An operator creates its CA Secret long
//     after the chart renders, and may populate it later still. "Not there
//     yet" is the normal startup state: a quiet, retried wait, never an error
//     and never a busy-loop.
//  4. Own the projection, never adopt a stranger. The projection is
//     owner-referenced to the release, so deleting the application collects it
//     natively. A Secret of the same name that the controller did not create is
//     left untouched, and the collision is surfaced as a Warning Event.
//  5. Withdraw on opt-out, but only on a definitive one. An owner reference
//     retires the projection when the whole application goes away; it does
//     nothing when the application merely stops publishing a CA (its publish
//     label flipped to "false", its declaration removed), and a tenant left
//     holding a retired trust anchor is a wrong answer, not a stale one. So a
//     release with no source at all has its projection deleted. The two
//     INDETERMINATE states are pointedly not that: a declared source that does
//     not exist yet is an operator still minting it, and a declaration that
//     will not render is a platform-side typo. Neither withdraws anything —
//     a bootstrapping engine and a bad character in a shipped
//     ApplicationDefinition must never read as a tenant's decision to stop.
//
// # Why the owner is the HelmRelease
//
// The HelmRelease IS the application instance in storage: the aggregated
// apps.cozystack.io object is a projection of it and shares its UID
// (pkg/registry/apps/application/rest.go), and deleting the application
// deletes the HelmRelease. Referencing the HelmRelease rather than the
// aggregated object has one further, load-bearing consequence: the lineage
// admission webhook resolves an object's application by walking owner
// references up to a HelmRelease (pkg/lineage/lineage.go), so an
// OwnerReference to the aggregated object would terminate that walk with no
// ancestor, leaving the projection without the
// internal.cozystack.io/tenantresource label — the very label that lets the
// tenant read it through the tenantsecrets API.
//
// # What this controller does NOT decide
//
// It does not grant the tenant read access, and cannot. That verdict belongs to
// the lineage admission webhook, which derives it from the
// ApplicationDefinition's spec.secrets selectors and stamps
// internal.cozystack.io/tenantresource — "true" or "false" — overwriting
// whatever the writer put there.
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
// internal.cozystack.io/tenant-ca on the projection, the single
// engine-agnostic label a definition's spec.secrets can select on, and to make
// sure the webhook is asked again whenever the definition changes (see
// SelectorsDigestAnnotation).
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
	"regexp"
	"sort"
	"strings"
	"text/template"
	"time"

	helmv2 "github.com/fluxcd/helm-controller/api/v2"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/selection"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/validation"
	"k8s.io/client-go/tools/record"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	crsource "sigs.k8s.io/controller-runtime/pkg/source"

	cozyv1alpha1 "github.com/cozystack/cozystack/api/v1alpha1"
	appsv1alpha1 "github.com/cozystack/cozystack/pkg/apis/apps/v1alpha1"
)

const (
	// SourceLabel marks a Secret whose CA certificate must be published to
	// the tenant. On the label-driven leg it — never the name — selects the
	// source.
	SourceLabel = "internal.cozystack.io/publish-ca-cert"

	// SourceKeyAnnotation optionally names the key holding the CA certificate
	// in a labelled source. It defaults to caCertKey.
	SourceKeyAnnotation = "internal.cozystack.io/publish-ca-cert-key"

	// SourceReleaseLabel names the release a labelled source belongs to, and is
	// REQUIRED on the label-driven leg.
	//
	// It exists because the obvious attributions are both unavailable on the
	// objects that leg serves. A cert-manager-issued Secret carries no
	// OwnerReference (the platform ships cert-manager with
	// enableCertificateOwnerRef=false) and no Helm metadata (Helm did not
	// create it — cert-manager did, out of a Certificate). With neither, the
	// lineage webhook cannot resolve an ancestor, so it stamps no
	// application.* labels either, and the Secret is attributable to nothing.
	//
	// The chart therefore names the release explicitly, in the very same
	// Certificate.spec.secretTemplate.labels block that carries SourceLabel:
	//
	//	secretTemplate:
	//	  labels:
	//	    internal.cozystack.io/publish-ca-cert: "true"
	//	    internal.cozystack.io/publish-ca-cert-release: "{{ .Release.Name }}"
	//
	// Attribution by lineage or Helm labels is still honoured, for a source that
	// happens to carry them (an operator-created Secret with an OwnerReference),
	// but nothing may rely on them alone. In practice a source on this leg is
	// always cert-manager-issued and attributed by the release label, because a
	// platform admission policy (cozystack-publish-ca-cert-writer-policy) allows
	// only cert-manager to write a publish-ca-cert Secret at all — a plain
	// Helm/Flux-rendered one is rejected before it exists. The lineage/Helm
	// routes remain as defensive generality, not an expected path.
	SourceReleaseLabel = "internal.cozystack.io/publish-ca-cert-release"

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

	// SourceRefAnnotation records the "<namespace>/<name>" of the source on
	// the projection, for traceability across both legs.
	SourceRefAnnotation = "internal.cozystack.io/publish-ca-cert-source"

	// SourceModeAnnotation records WHICH LEG resolved the source at the
	// projection's last write: modeDeclared or modeLabelled.
	//
	// It exists because "no source resolved" has two opposite meanings and, by
	// the time the question is asked, the evidence that would separate them is
	// already gone. Withdrawal turns on whether the recorded source Secret still
	// exists — absence is indeterminate, because deleting the CA Secret is how a
	// cert-manager reissue is forced. But a source that vanished BEFORE its
	// declaration was removed leaves that question answering "merely absent,
	// hold" about a release the platform has definitively stopped vouching for,
	// and nothing ever revisits it: the projection carries no publish label, so
	// it is neither cached nor watched, and a release with no source requests no
	// requeue. The tenant keeps a retired trust anchor forever.
	//
	// The leg cannot be re-derived at prune time. A removed declaration is
	// exactly a definition with no spec.caCert — indistinguishable from an
	// engine that never declared one — so the projection has to have written
	// down where it came from while it still knew.
	//
	// Recording the leg, rather than a flag like "withdraw on absence", keeps
	// the annotation a FACT about how the projection was built. The policy that
	// reads it lives in pruneProjection, where it can be read next to the
	// declaration it is weighed against.
	SourceModeAnnotation = "internal.cozystack.io/publish-ca-cert-mode"

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
	// the release IS reconciled (definitions are watched), but the data, labels,
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
	// Revocation is the direction that makes this load-bearing rather than
	// tidy. A definition that stops selecting the anchor must take the tenant's
	// access away; with the verdict frozen it stays "true" and the tenant keeps
	// reading a trust anchor the platform withdrew.
	//
	// It digests spec.secrets rather than recording the definition's
	// resourceVersion, which would be the cruder signal: resourceVersion moves on
	// ANY edit to the definition, so every unrelated field change would rewrite
	// every projection of that kind for no gain.
	SelectorsDigestAnnotation = "internal.cozystack.io/publish-ca-cert-selectors"

	// managedByCozystackLabel is stamped by the lineage admission webhook.
	// The reconciler never writes it; it only takes care not to strip it.
	managedByCozystackLabel = "internal.cozystack.io/managed-by-cozystack"

	// helmNameLabel is the Helm ownership label Flux stamps on every rendered
	// object. It attributes a chart-rendered source to its release even if the
	// lineage labels are absent.
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

	// modeDeclared and modeLabelled are the two legs, as recorded in
	// SourceModeAnnotation. A projection carrying neither (one written before
	// the annotation existed) is treated as modeLabelled: that is the reading
	// that HOLDS the anchor on an absent source, so an unknown provenance
	// resolves to the safe answer rather than to a withdrawal.
	modeDeclared = "declared"
	modeLabelled = "labelled"

	// resyncInterval carries a rotation of a name-driven (unwatched) source
	// through, and heals a projection deleted out of band or a name collision
	// the operator has since cleared. A label-driven source is watched, so it
	// does not wait for this.
	resyncInterval = 5 * time.Minute

	// missingSourceRetry is the retry cadence while a declared source has not
	// been created yet AND the release is young enough that it plausibly still
	// will be. It is much shorter than resyncInterval because this is the
	// window between an application appearing and its tenant getting a trust
	// anchor, and an operator mints its CA seconds after the chart lands.
	missingSourceRetry = 30 * time.Second

	// bootstrapWindow bounds how long missingSourceRetry applies, measured
	// from the release's creation. Past it, a declared source that still does
	// not exist is not "late" — it is a release whose TLS is switched off, so
	// its CA was never going to be minted. Those releases must not poll at the
	// bootstrap cadence for the lifetime of the cluster: they fall back to the
	// normal resync, which still picks the source up (5 minutes late) if TLS is
	// enabled later.
	bootstrapWindow = 15 * time.Minute
)

// Event reasons surfaced on the affected object.
const (
	reasonPrivateKeyRefused     = "CACertPrivateKeyRefused"
	reasonInvalidCACert         = "CACertInvalid"
	reasonCollision             = "CACertSecretCollision"
	reasonCanonicalNameOccupied = "CACertCanonicalNameOccupied"
	reasonCanonicalNameContract = "CACertCanonicalNameContract"
	reasonAmbiguousSource       = "CACertSourceAmbiguous"
	reasonInvalidDeclaration    = "CACertDeclarationInvalid"
	reasonUnattributableSource  = "CACertSourceUnattributable"
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

	// errAmbiguousSource marks more than one labelled source in a release.
	errAmbiguousSource = errors.New("more than one labelled CA source in this release")

	// errUnusableDeclaration marks an ApplicationDefinition whose caCert
	// declaration cannot be rendered. It is emphatically NOT "this engine
	// publishes nothing": a platform-side typo must never read as an opt-out,
	// or a single bad character in a shipped ApplicationDefinition would
	// withdraw the trust anchor of every release of that kind at once.
	errUnusableDeclaration = errors.New("unusable caCert declaration")

	// errProjectionTerminating marks a projection still being garbage-collected
	// — what deleting and recreating an application under the same name leaves
	// behind. Writing to a terminating object accomplishes nothing; the
	// reconciler waits for the collector and then creates a fresh projection.
	errProjectionTerminating = errors.New("CA projection is terminating")
)

// Reconciler publishes the trust anchor of an application release as a
// canonical, key-free "<release>.tenant-ca" Secret in the release namespace.
type Reconciler struct {
	client.Client
	// Reader is the manager's uncached APIReader. Three reads must not go
	// through the cache: a name-driven source (it carries no label the scoped
	// Secret informer selects on), the projection, and a foreign Secret
	// squatting on the projection's name — the last two would otherwise be
	// invisible, and the collision guard must not be fooled by a cache miss.
	Reader client.Reader
	// Cache reads this controller's DEDICATED, scoped Secret informer
	// (SecretCacheByObject), and is used for exactly one thing: listing the
	// labelled sources of a release. Those opted-in CA sources are the ONLY
	// Secrets the informer holds, so the list is cheap and the cache never pulls
	// in every Secret in the cluster — but it is not key-free: a cert-manager
	// source carries the CA private key under tls.key, so labelled sources with
	// key material do sit in this cache. That is safe because the write path only
	// ever lifts one whitelisted key and re-guards it; no cached key byte is
	// copied anywhere. Every other Secret read (a name-driven source, the
	// projection, a foreign squatter) goes through the uncached Reader.
	Cache client.Reader
	// Recorder surfaces every refusal — key material under the lifted key, a
	// malformed certificate, an ambiguous or unusable source, a name collision
	// — as a Warning Event, so an otherwise silent skip is visible with
	// `kubectl get events -n <namespace>`.
	Recorder record.EventRecorder
}

// application identifies the application instance a release belongs to, as
// the cozystack API stamps it on every HelmRelease it creates.
type application struct {
	Group string
	Kind  string
	Name  string
}

// source is a resolved CA source: the Secret to lift from, and the key to
// lift out of it.
type source struct {
	secret *corev1.Secret
	key    string
	// declared is the LEG: true for a name-driven source (the
	// ApplicationDefinition names it in spec.caCert), false for a label-driven
	// one (a Secret opted in through SourceLabel).
	//
	// It is stored rather than derived because two decisions hang off it and
	// neither can recover it later. Whether the source is WATCHED follows from
	// it — a declared source carries no label the scoped informer selects on, so
	// only the resync re-reads it (see watched and refusalRetry). And what an
	// unresolved source MEANS later follows from it too, which is why it is
	// recorded on the projection (see SourceModeAnnotation).
	declared bool
}

// watched reports whether changes to the source reach the reconciler as events.
// Only the label-driven leg is: it is the only one the scoped Secret informer
// can select on.
func (s *source) watched() bool { return !s.declared }

// mode returns the leg as recorded in SourceModeAnnotation.
func (s *source) mode() string {
	if s.declared {
		return modeDeclared
	}
	return modeLabelled
}

// +kubebuilder:rbac:groups="",resources=secrets,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups="",resources=events,verbs=create;patch
// +kubebuilder:rbac:groups=helm.toolkit.fluxcd.io,resources=helmreleases,verbs=get;list;watch
// +kubebuilder:rbac:groups=cozystack.io,resources=applicationdefinitions,verbs=get;list;watch

// Reconcile publishes the trust anchor of one application release.
//
// Everything that is not a transient API error resolves to a quiet wait or a
// Warning Event: an unpopulated source, an ambiguous one, a poisoned value
// and a name collision must never wedge the workqueue, and must never
// overwrite an object the controller does not own.
func (r *Reconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	hr := &helmv2.HelmRelease{}
	if err := r.Get(ctx, req.NamespacedName, hr); err != nil {
		if apierrors.IsNotFound(err) {
			// The release is gone. The projection is owner-referenced to it,
			// so the garbage collector removes it — there is no prune logic
			// here, by design.
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, fmt.Errorf("get HelmRelease: %w", err)
	}
	if hr.DeletionTimestamp != nil {
		return ctrl.Result{}, nil
	}
	app, ok := applicationOf(hr)
	if !ok {
		// Not an application release (a platform component's HelmRelease).
		return ctrl.Result{}, nil
	}

	// Resolve the ApplicationDefinition once. It answers two questions: where a
	// name-driven source lives (spec.caCert), and — through spec.secrets — whether
	// the projection is visible to the tenant at all. Both legs need it, so it is
	// fetched here rather than inside one of them.
	def, err := r.applicationDefinition(ctx, app)
	if err != nil {
		return ctrl.Result{}, err
	}
	if def == nil {
		// No ApplicationDefinition registered for this kind. INDETERMINATE, not an
		// opt-out: definitions are registered dynamically at runtime, so reading
		// their absence as "this engine publishes nothing" would prune the trust
		// anchor of every release of the kind at once. Registering one is an
		// ApplicationDefinition event, which is watched.
		logger.Info("no ApplicationDefinition registered for this kind; leaving the release untouched",
			"release", req.NamespacedName, "kind", app.Kind)
		return ctrl.Result{}, nil
	}

	src, err := r.resolveSource(ctx, hr, app, def)
	switch {
	case err == nil:
	case errors.Is(err, errAmbiguousSource):
		r.warn(hr, reasonAmbiguousSource,
			"more than one Secret in this release is labelled %s; the trust anchor is not published until exactly one is",
			SourceLabel)
		return ctrl.Result{}, nil
	case errors.Is(err, errUnusableDeclaration):
		// Already surfaced as a Warning Event on the release. Leave the release
		// exactly as it stands — and in particular do NOT prune an existing
		// projection: an unrenderable declaration is a platform-side mistake,
		// not a tenant-visible decision to stop publishing.
		//
		// No requeue: ApplicationDefinitions are watched, so correcting the
		// declaration wakes the reconciler at once.
		return ctrl.Result{}, nil
	case apierrors.IsNotFound(err):
		// The declared source has not been created yet. This is the normal
		// state between an application appearing and its operator minting the
		// CA — a quiet wait, fast while the release is young, then slow (see
		// missingSourceWait), so a TLS-less release does not poll forever at
		// the bootstrap cadence.
		//
		// An absent-but-declared source is INDETERMINATE — the operator may
		// still be minting it — so any existing projection stays put. Only a
		// definitive "no source at all" (below) withdraws one.
		wait := missingSourceWait(hr)
		logger.Info("declared CA source does not exist yet; waiting for the operator to create it",
			"release", req.NamespacedName, "retryIn", wait)
		return ctrl.Result{RequeueAfter: wait}, nil
	default:
		return ctrl.Result{}, err
	}
	if src == nil {
		// Nothing resolved: no Secret opted in through the label, and the
		// ApplicationDefinition declares no source.
		//
		// That is NOT yet enough to withdraw the trust anchor, because it conflates
		// two opposite situations — the release genuinely stopped publishing, or its
		// source is merely absent this instant. pruneProjection tells them apart,
		// and needs the definition to do it: whether a declaration still stands is
		// half the answer.
		return r.pruneProjection(ctx, hr, def)
	}

	target := hr.Name + projectionSuffix
	if src.secret.Name == target {
		return r.reconcileSourceAtCanonicalName(src, target)
	}

	value, ok := src.secret.Data[src.key]
	if !ok || len(bytes.TrimSpace(value)) == 0 {
		// The source exists but the CA is not written yet — operators create
		// the Secret before they populate it. On the label leg the watch
		// delivers the write; on the name leg the wait below re-reads it.
		wait := missingSourceWait(hr)
		logger.Info("CA source does not carry its certificate yet; waiting",
			"secret", src.secret.Name, "key", src.key, "retryIn", wait)
		return ctrl.Result{RequeueAfter: wait}, nil
	}

	if err := r.upsertProjection(ctx, hr, def, src, target, value); err != nil {
		switch {
		case errors.Is(err, errPrivateKey):
			r.warn(src.secret, reasonPrivateKeyRefused,
				"key %q carries PEM private key material; refusing to publish it as a trust anchor", src.key)
			return ctrl.Result{RequeueAfter: refusalRetry(src)}, nil
		case errors.Is(err, errNotCertificate):
			r.warn(src.secret, reasonInvalidCACert,
				"key %q does not carry a PEM certificate; refusing to publish it as a trust anchor", src.key)
			return ctrl.Result{RequeueAfter: refusalRetry(src)}, nil
		case errors.Is(err, errProjectionTerminating):
			// The previous projection is still being collected, which is what a
			// delete-and-recreate of the application under the same name leaves
			// behind. Wait for it to go, then create a fresh one.
			wait := missingSourceWait(hr)
			logger.Info("the previous CA projection is still being collected; waiting to republish",
				"secret", target, "retryIn", wait)
			return ctrl.Result{RequeueAfter: wait}, nil
		}
		return ctrl.Result{}, err
	}

	// Resync: a name-driven source is not watched, so this is the only thing
	// that carries its rotation through. It also heals a projection deleted
	// out of band, and retries a name collision once the colliding Secret is
	// removed.
	return ctrl.Result{RequeueAfter: resyncInterval}, nil
}

// reconcileSourceAtCanonicalName handles the degenerate case where the source
// itself sits at the canonical projection name — a Secret cannot be projected
// over itself.
//
// The canonical name is chosen so that no operator the platform ships claims
// it, so this is not reachable through any of them today; it is reachable
// through a misconfiguration (a declaration or a publish label pointing at
// "<release>.tenant-ca" itself), and the answer depends entirely on content.
//
// A key-FREE Secret there is already the trust anchor the tenant needs: there
// is nothing to extract, it is not the controller's to adopt, and that is a
// silent no-op. A key-BEARING Secret there is the dangerous shape: the
// canonical name is occupied by an object a tenant must never read, and no
// projection could be written without destroying live key material. Refuse it
// loudly — never overwrite, never adopt.
func (r *Reconciler) reconcileSourceAtCanonicalName(src *source, target string) (ctrl.Result, error) {
	for _, key := range sortedKeys(src.secret.Data) {
		if containsPrivateKey(string(src.secret.Data[key])) {
			r.warn(src.secret, reasonCanonicalNameOccupied,
				"the Secret %q occupies the canonical trust-anchor name and carries private key material under %q; no trust anchor is published for this release until that Secret is renamed",
				target, key)
			return ctrl.Result{RequeueAfter: resyncInterval}, nil
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
	if deviation := canonicalContractDeviation(src.secret); deviation != "" {
		r.warn(src.secret, reasonCanonicalNameContract,
			"the Secret %q occupies the canonical trust-anchor name but %s; the tenant reads it as this release's trust anchor exactly as it stands",
			target, deviation)
	}

	// Re-examine on the resync, like every other outcome here.
	//
	// This is not symmetry for its own sake. A source reaching this branch through
	// a DECLARATION is not watched — nothing observes its contents — so returning
	// no requeue made the private-key refusal above one-shot for it:
	// key material added to this Secret after the first pass would never be
	// noticed, and the canonical name would go on being served to the tenant with
	// a key in it for the lifetime of the release.
	return ctrl.Result{RequeueAfter: resyncInterval}, nil
}

// canonicalContractDeviation describes how a Secret sitting at the canonical
// trust-anchor name departs from the contract that name carries, or "" when it
// satisfies it. Key material is NOT its business: the caller has already refused
// that, louder and for a different reason.
func canonicalContractDeviation(s *corev1.Secret) string {
	if s.Type != corev1.SecretTypeOpaque {
		return fmt.Sprintf("is of type %q rather than %q", s.Type, corev1.SecretTypeOpaque)
	}
	// Reuse the write path's own guard, so "is this a trust anchor?" is answered
	// by one piece of code wherever it is asked.
	if _, err := projectionData(s.Data[caCertKey]); err != nil {
		return fmt.Sprintf("its %q does not carry a usable trust anchor (%v)", caCertKey, err)
	}
	var extra []string
	for _, key := range sortedKeys(s.Data) {
		if key != caCertKey {
			extra = append(extra, key)
		}
	}
	if len(extra) > 0 {
		return fmt.Sprintf("it carries %s besides %q", strings.Join(extra, ", "), caCertKey)
	}
	return ""
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

// resolveSource finds the CA source of a release. It returns (nil, nil) when the
// engine publishes no trust anchor at all.
//
// The DECLARED source wins. When an ApplicationDefinition declares spec.caCert,
// that named source is authoritative and a labelled Secret is NOT consulted for
// the engine at all; the label leg serves only engines with no declaration.
//
// This precedence is a security boundary, not a preference. The declaration
// lives on the ApplicationDefinition, a platform object no tenant can write; a
// publish label sits on a Secret that someone with Secret-write in the release
// namespace can create. If a labelled Secret could override a declaration, that
// someone could swap a declared engine's genuine trust anchor for one they
// control — the projection is delivered to the tenant as a vouched CA, so a
// forged one is a spoofing/MITM primitive. Taking a declared engine's CA ONLY
// from its declaration makes every declared engine's trust anchor
// platform-attributed and unforgeable.
//
// This does not weaken the label leg for the engines that genuinely need it —
// those declare nothing, so they still resolve through it. Migrating a declared
// engine onto the label leg stays a single atomic edit: remove spec.caCert in
// the same commit the chart starts labelling, so there is never a window where a
// label silently overrides a live declaration.
func (r *Reconciler) resolveSource(ctx context.Context, hr *helmv2.HelmRelease, app application, def *cozyv1alpha1.ApplicationDefinition) (*source, error) {
	if def.Spec.CACert != nil {
		return r.declaredSource(ctx, hr, app, def)
	}
	return r.labelledSource(ctx, hr, app)
}

// labelledSource returns the Secret of this release that opts into publication
// through the label, if there is exactly one. Two is an ambiguity the operator
// must resolve — publishing the wrong CA breaks verification for every client.
func (r *Reconciler) labelledSource(ctx context.Context, hr *helmv2.HelmRelease, app application) (*source, error) {
	list := &corev1.SecretList{}
	if err := r.Cache.List(ctx, list,
		client.InNamespace(hr.Namespace),
		client.MatchingLabels{SourceLabel: trueValue},
	); err != nil {
		return nil, fmt.Errorf("list labelled CA sources: %w", err)
	}
	var found *corev1.Secret
	for i := range list.Items {
		s := &list.Items[i]
		// isSource re-states the opt-in locally, so the leg stays correct if
		// the List selector or the informer scope is ever widened.
		if !isSource(s) || !belongsToRelease(s, app, hr.Name) {
			continue
		}
		if found != nil {
			return nil, errAmbiguousSource
		}
		found = s
	}
	if found == nil {
		return nil, nil
	}
	return &source{secret: found, key: sourceKey(found), declared: false}, nil
}

// declaredSource returns the Secret named by the application's
// ApplicationDefinition (spec.caCert), for the engines whose operator creates
// the CA Secret itself and cannot be made to label it. The Secret is read
// through the uncached reader: it carries no label the scoped Secret informer
// selects on, so it is not in the cache.
//
// The caller resolves the declaration only when spec.caCert is set, so a
// non-nil declaration is a precondition here.
//
// A missing Secret is reported as NotFound, which the caller turns into a
// bounded wait — the operator has not minted the CA yet.
//
// The named source is read by name with no ownership check, so this leg trusts
// the release namespace: whoever can create the declared Secret name before the
// operator does can seed the CA that gets projected. That is not a cross-tenant
// escalation — it only affects the squatter's own namespace anchor — and it is
// the same trust the platform already places in operator-created Secrets, but it
// is why the label leg (which any namespace writer could also target) is not
// allowed to override this one; see resolveSource.
func (r *Reconciler) declaredSource(ctx context.Context, hr *helmv2.HelmRelease, app application, def *cozyv1alpha1.ApplicationDefinition) (*source, error) {
	decl := def.Spec.CACert

	name, err := renderSourceName(decl.SourceSecretName, app, hr.Name, hr.Namespace)
	if err != nil {
		// A broken template in the ApplicationDefinition is a platform-side
		// mistake, not a transient failure: surface it and stop, rather than
		// hot-looping on a string that will never render.
		//
		// It returns errUnusableDeclaration rather than "no source", because
		// the two must not be confused: "no source" now withdraws the
		// projection, and a typo in a shipped ApplicationDefinition would then
		// strip the trust anchor from every release of that kind.
		r.warn(hr, reasonInvalidDeclaration,
			"ApplicationDefinition %q declares an unusable caCert.sourceSecretName %q: %v",
			def.Name, decl.SourceSecretName, err)
		return nil, fmt.Errorf("%w: ApplicationDefinition %s", errUnusableDeclaration, def.Name)
	}

	secret := &corev1.Secret{}
	if err := r.Reader.Get(ctx, types.NamespacedName{Namespace: hr.Namespace, Name: name}, secret); err != nil {
		// NotFound flows up: the caller waits for the operator.
		return nil, err
	}

	// A projection is never a CA source. The invariant is worth one label read
	// because of what it prevents: this controller laundering one release's CA
	// into another's.
	//
	// It was written for a collision the canonical name no longer permits. While
	// the projection was "<release>-tenant-ca", a sibling application named
	// "mydb-tenant" declared "{{ .release }}-ca" — which resolved to exactly
	// "mydb"'s projection — so the sibling lifted its neighbour's ca.crt and
	// republished it under its own canonical name. The tenant was handed, as their
	// application's vouched trust anchor, a CA that signs for a different
	// application, and every check downstream passed: the bytes really were a
	// certificate, key-free, from a Secret in the right namespace. Only provenance
	// was wrong, and only the marker records provenance.
	//
	// The dot in projectionSuffix ends that by construction — no
	// "<prefix><app><engine suffix>" can contain one — so no shipped declaration
	// can resolve to a projection today. The guard stays anyway: it is the last
	// check between a platform-side typo (a sourceSecretName template with a
	// literal dot) and publishing another application's CA as this one's, and that
	// failure is silent everywhere else. Cheap, and the only thing standing there.
	//
	// Refuse loudly rather than wait: unlike an unminted CA, this does not resolve
	// itself with time. It is surfaced on the release, not on the neighbour's
	// projection, because the release is what is misconfigured.
	if secret.Labels[ManagedLabel] == trueValue {
		r.warn(hr, reasonCollision,
			"ApplicationDefinition %q declares CA source %q, but that Secret is the trust-anchor projection of another release; refusing to republish another application's CA as this one's trust anchor — rename one of the two applications",
			def.Name, name)
		return nil, fmt.Errorf("%w: declared source %s/%s is another release's projection", errUnusableDeclaration, hr.Namespace, name)
	}

	key := strings.TrimSpace(decl.SourceKey)
	if key == "" {
		key = caCertKey
	}
	return &source{secret: secret, key: key, declared: true}, nil
}

// applicationDefinition returns the ApplicationDefinition for an application
// kind, or nil when none is registered.
func (r *Reconciler) applicationDefinition(ctx context.Context, app application) (*cozyv1alpha1.ApplicationDefinition, error) {
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

// upsertProjection creates or refreshes the canonical trust-anchor Secret.
// Both legs land here: this is the only place bytes reach a projection, and
// the guard in projectionData runs on exactly the bytes about to be written.
func (r *Reconciler) upsertProjection(ctx context.Context, hr *helmv2.HelmRelease, def *cozyv1alpha1.ApplicationDefinition, src *source, target string, value []byte) error {
	desired, err := projectionData(value)
	if err != nil {
		return err
	}
	owner := releaseOwnerRef(hr)
	ref := src.secret.Namespace + "/" + src.secret.Name
	digest, err := selectorsDigest(def)
	if err != nil {
		return err
	}

	existing := &corev1.Secret{}
	err = r.Reader.Get(ctx, types.NamespacedName{Namespace: hr.Namespace, Name: target}, existing)
	switch {
	case apierrors.IsNotFound(err):
		projection := &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Namespace: hr.Namespace,
				Name:      target,
				Labels:    map[string]string{TenantCALabel: trueValue, ManagedLabel: trueValue},
				Annotations: map[string]string{
					SourceRefAnnotation:       ref,
					SourceModeAnnotation:      src.mode(),
					SelectorsDigestAnnotation: digest,
				},
				OwnerReferences: []metav1.OwnerReference{owner},
			},
			Type: corev1.SecretTypeOpaque,
			Data: desired,
		}
		if err := r.Create(ctx, projection); err != nil {
			return fmt.Errorf("create CA projection %s/%s: %w", hr.Namespace, target, err)
		}
		return nil
	case err != nil:
		return fmt.Errorf("get CA projection %s/%s: %w", hr.Namespace, target, err)
	}

	// Adoption demands BOTH the marker label and the Opaque type, and the type is
	// the load-bearing half.
	//
	// ManagedLabel is only a claim: it is an ordinary label on an ordinary Secret,
	// so anyone who can write Secrets in the namespace — the tenant — can forge
	// it. The type is not a claim but a fact this controller controls, because
	// every projection it creates is Opaque. So a non-Opaque Secret sitting at the
	// canonical name did NOT come from here, whatever its labels say.
	//
	// Checking it is not pedantry. Secret.type is immutable in Kubernetes
	// (ValidateSecretUpdate rejects a change to it), and the update path below
	// writes the type unconditionally. Adopting, say, a kubernetes.io/tls Secret
	// would therefore make every Update fail Invalid, forever — a tenant could
	// permanently deny their own release its trust anchor by creating one Secret
	// with a forged label. Treat it as what it is: a collision.
	//
	// The converse — an Opaque Secret at the canonical name carrying the marker,
	// which a forger could also produce — is deliberately adopted and OVERWRITTEN,
	// not refused, and that is the SAFE direction. The projection's whole job is to
	// be the tenant's vouched trust anchor, so if a forged copy is sitting there
	// (marker + tenant-ca label + an attacker ca.crt), the correct response is to
	// stamp the genuine CA and the genuine owner reference over it — which the
	// update path does. Requiring a non-forgeable owner reference here instead, as
	// a stricter-looking gate, would invert that: the forged copy would be treated
	// as a stranger, left untouched, and keep being served to the tenant. Nothing
	// legitimate carries this controller-internal marker on an object it did not
	// create, so overwriting it destroys no real data — it heals a forgery.
	if !isOurProjection(existing) {
		// A Secret of this name exists that the controller did not create.
		// Overwriting it could destroy live key material, so refuse and
		// surface the refusal on the colliding object itself.
		r.warn(existing, reasonCollision,
			"a Secret named %q already exists and was not created by the CA extraction controller; the trust anchor of %q is not published until that Secret is removed",
			target, src.secret.Name)
		return nil
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
		existing.Annotations[SourceRefAnnotation] == ref &&
		existing.Annotations[SourceModeAnnotation] == src.mode() &&
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
	existing.Annotations[SourceModeAnnotation] = src.mode()
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
		// webhook's verdict, so re-admitting on one would be pure load — see
		// TestReconcile_UpdatePreservesAdmissionLabels.
		delete(existing.Labels, managedByCozystackLabel)
	}
	if !ownedSolelyBy(existing.OwnerReferences, owner) {
		// REPLACE, never append, and replace down to EXACTLY one reference.
		//
		// Appending is wrong twice over. The API server rejects a second
		// reference with Controller=true outright ("Only one reference can have
		// Controller set to true"), and a stale reference is exactly what
		// deleting and recreating an application under the same name leaves
		// behind — same release name, new UID — so appending would wedge the
		// reconciler on an Invalid error instead of re-homing the projection
		// onto the live release.
		//
		// Keeping any OTHER reference is wrong too, which is why this asks
		// ownedSolelyBy and not hasOwner: garbage collection deletes a dependent
		// only once EVERY owner is gone, so one extra reference outlives the
		// release and keeps the projection — a trust anchor for an application
		// that no longer exists — readable in the tenant's namespace. Nothing
		// legitimate co-owns a projection: this controller creates it with one
		// owner, and the lineage webhook stamps labels, not references.
		existing.OwnerReferences = []metav1.OwnerReference{owner}
	}
	// Data is REPLACED, not merged: the projection holds ca.crt and nothing
	// else, on every write, whatever it held before.
	//
	// Type is deliberately NOT written. It is immutable in Kubernetes, so writing
	// it can only ever be a no-op or an Invalid error — and the adoption gate above
	// has already established that this object is Opaque.
	existing.Data = desired
	if err := r.Update(ctx, existing); err != nil {
		return fmt.Errorf("update CA projection %s/%s: %w", hr.Namespace, target, err)
	}
	return nil
}

// isOurProjection reports whether the Secret at the canonical name is one this
// controller created — the single question both the write path and the
// withdrawal path must ask, which is why it is one function and not two
// conditions that drifted apart.
//
// It demands BOTH the marker label and the Opaque type, and the TYPE is the
// load-bearing half. ManagedLabel is only a claim: an ordinary label on an
// ordinary Secret, forgeable by anyone with namespace Secret write. The type is
// not a claim but a fact this controller controls, because every projection it
// creates is Opaque — so a non-Opaque Secret at the canonical name did NOT come
// from here, whatever its labels say.
//
// The two paths asked different questions once, and the asymmetry had teeth in
// the destructive direction: the write path refused to overwrite a forged
// non-Opaque Secret (correctly — it may be live key material), while withdrawal
// checked the label alone and DELETED that same object. "Never adopt a stranger"
// and "never delete a stranger" are the same rule, and a stranger recognised by
// one path and not the other is worse than either rule alone.
func isOurProjection(s *corev1.Secret) bool {
	return s.Labels[ManagedLabel] == trueValue && s.Type == corev1.SecretTypeOpaque
}

// pruneProjection decides what an unresolved source means for a projection that
// already exists, and withdraws it only when the answer is a genuine opt-out.
//
// "No source resolved" is ambiguous, and the two readings are opposites:
//
//   - POSITIVE OPT-OUT. The platform has stopped standing behind this trust
//     anchor — the source's publish label was flipped to "false", or the
//     declaration naming it was removed. A tenant still holding the anchor is a
//     wrong answer, not a stale one. Withdraw it.
//
//   - MERELY ABSENT. The source Secret this projection came from is gone right
//     now. That is INDETERMINATE, and it is an ordinary event rather than an
//     exotic one: deleting the CA Secret is precisely how a cert-manager reissue
//     is forced, and the operator writes a new one moments later. Withdrawing here
//     would rip the trust anchor out from under every client of a release that is
//     merely rotating its CA. Keep it, and wait.
//
// Two questions separate them, in this order, because the legs opt out through
// different acts and only one of them leaves the Secret behind to be asked about.
//
//  1. WAS THE DECLARATION REMOVED? A declared engine's opt-out is an edit to the
//     ApplicationDefinition, a platform object no tenant can write, and it is
//     definitive the instant it lands — it says nothing about the Secret, which
//     may be long gone. So a projection that records the declared leg
//     (SourceModeAnnotation) and finds no declaration standing is withdrawn
//     without asking about its source at all. Asking anyway is what stranded
//     retired anchors indefinitely: the source vanishing first and the
//     declaration being removed second is an ordinary sequence, and question 2
//     answers "merely absent, hold" to it, forever, because nothing ever comes
//     back — the projection carries no publish label, so it is neither cached nor
//     watched.
//
//  2. DOES THE RECORDED SOURCE STILL EXIST? On the label leg the opt-out IS the
//     Secret — its label flipped — so the Secret is still there to be read, and
//     its absence genuinely is the reissue case. This is the question that must
//     NOT be answered with a withdrawal.
//
// A projection with no recorded leg (written before the annotation existed) is
// read as the label leg: that is the reading that holds, so an unknown
// provenance resolves to the conservative answer.
//
// Only a Secret this controller created is ever deleted; a stranger at the
// canonical name is left exactly where it is. That is the same rule the write
// path applies, and literally so: both ask isOurProjection, so the two cannot
// drift into disagreeing about the same object.
func (r *Reconciler) pruneProjection(ctx context.Context, hr *helmv2.HelmRelease, def *cozyv1alpha1.ApplicationDefinition) (ctrl.Result, error) {
	logger := log.FromContext(ctx)
	target := hr.Name + projectionSuffix

	existing := &corev1.Secret{}
	err := r.Reader.Get(ctx, types.NamespacedName{Namespace: hr.Namespace, Name: target}, existing)
	switch {
	case apierrors.IsNotFound(err):
		// Nothing was ever published for this release. Nothing to withdraw, and
		// nothing to wait for.
		return ctrl.Result{}, nil
	case err != nil:
		return ctrl.Result{}, fmt.Errorf("get CA projection %s/%s: %w", hr.Namespace, target, err)
	}
	if !isOurProjection(existing) || existing.DeletionTimestamp != nil {
		// Not ours, or already going. Either way, not ours to remove.
		return ctrl.Result{}, nil
	}

	// A declaration that still stands is never an opt-out, whatever the caller
	// failed to resolve. The caller only reaches here with no declaration today —
	// resolveSource never returns (nil, nil) for a declared engine — so this is
	// unreachable, and deliberately kept anyway.
	//
	// An unreachable guard earns its place when it is the last check between
	// someone else's mistake and a destructive act, and this one is: the
	// invariant it protects belongs to resolveSource, not here, and its violation
	// is silent. The withdrawal below is only definitive because a declaration
	// would have been consulted first, so a resolveSource that ever returned
	// (nil, nil) for a declared engine would pull the trust anchor out from under
	// a live one. Compare a guard that merely restates a guarantee its own code
	// already makes: that one is noise, because it cannot fire and nothing is
	// lost if it somehow does.
	if def.Spec.CACert != nil {
		logger.Info("no source resolved but the declaration still stands; keeping the published trust anchor",
			"secret", hr.Namespace+"/"+target)
		return ctrl.Result{RequeueAfter: missingSourceWait(hr)}, nil
	}

	// Question 1: was this projection built from a declaration that is now gone?
	if existing.Annotations[SourceModeAnnotation] == modeDeclared {
		logger.Info("withdrawing the CA projection: the ApplicationDefinition no longer declares a CA source",
			"secret", hr.Namespace+"/"+target)
		return r.withdraw(ctx, hr, existing, target)
	}

	// Question 2: does the Secret this projection was built from still exist?
	if name, ok := recordedSource(existing, hr.Namespace); ok {
		recorded := &corev1.Secret{}
		err := r.Reader.Get(ctx, types.NamespacedName{Namespace: hr.Namespace, Name: name}, recorded)
		switch {
		case apierrors.IsNotFound(err):
			// MERELY ABSENT. Hold the trust anchor and wait for the source to come
			// back — fast while the release is young, then on the ordinary resync.
			wait := missingSourceWait(hr)
			logger.Info("the CA source is gone; keeping the published trust anchor and waiting for it to return",
				"secret", hr.Namespace+"/"+target, "source", hr.Namespace+"/"+name, "retryIn", wait)
			return ctrl.Result{RequeueAfter: wait}, nil
		case err != nil:
			return ctrl.Result{}, fmt.Errorf("get recorded CA source %s/%s: %w", hr.Namespace, name, err)
		}
		// The source is still there and simply no longer opts in. That is the
		// positive opt-out; fall through and withdraw.
	}

	logger.Info("withdrawing the CA projection: the release no longer publishes a trust anchor",
		"secret", hr.Namespace+"/"+target)
	return r.withdraw(ctx, hr, existing, target)
}

// withdraw deletes a projection the controller has decided is retired, guarding
// the delete on the exact object that was observed.
func (r *Reconciler) withdraw(ctx context.Context, hr *helmv2.HelmRelease, existing *corev1.Secret, target string) (ctrl.Result, error) {

	// Guard the delete on the exact object that was observed, so a projection that
	// changed under us is re-read rather than deleted blind.
	err := r.Delete(ctx, existing, client.Preconditions{
		UID:             &existing.UID,
		ResourceVersion: &existing.ResourceVersion,
	})
	switch {
	case err == nil, apierrors.IsNotFound(err):
		// Withdrawn, or someone got there first. Either way it is gone.
		return ctrl.Result{}, nil
	default:
		// Everything else, INCLUDING a failed precondition, is returned so the
		// workqueue retries with backoff.
		//
		// A Conflict must not be swallowed here. Reconciles for one release are
		// serialized, so this controller cannot have republished the projection
		// concurrently — a Conflict means an EXTERNAL writer touched it, which is
		// exactly the case that has to be re-read and re-decided. And nothing else
		// would ever come back to it: the projection carries no publish label, so
		// it is neither cached nor watched, and a release with no source requests
		// no requeue. Dropping the error would abandon the withdrawal and leave
		// the tenant holding a retired trust anchor indefinitely.
		return ctrl.Result{}, fmt.Errorf("withdraw CA projection %s/%s: %w", hr.Namespace, target, err)
	}
}

// recordedSource returns the NAME of the Secret a projection records as its
// source in SourceRefAnnotation, and reports false when there is no usable
// record. A projection with no usable record is treated as having none, which
// withdraws.
//
// The reference is honoured only when it names a Secret in the release's own
// namespace. That is not a limitation, it is the whole contract: the controller
// resolves sources exclusively within the release namespace, on both legs, so
// every reference it has ever WRITTEN is same-namespace by construction. A
// reference naming anywhere else is therefore not "a source elsewhere" — it is a
// record this controller did not write.
//
// Honouring one would hand a tenant a lever on a cluster-wide read. The
// annotation is an ordinary annotation on an ordinary Secret in the tenant's own
// namespace, so a tenant can set it; the reader that consumes it is the uncached
// cluster-scoped APIReader, bound by the controller's own RBAC and not the
// tenant's. Two consequences, and the second is the one that matters:
//
//   - a Secret-existence oracle. Point the record at kube-system/foo and watch
//     whether the projection is withdrawn; the answer discloses whether foo
//     exists, across a namespace boundary the tenant cannot otherwise see.
//
//   - withdrawal suppression, which is the real damage. Point the record at a
//     name that will never exist and the prune decision answers "merely absent,
//     hold" forever, so a trust anchor the platform has retired stays readable in
//     the tenant's namespace — the exact outcome this whole path exists to
//     prevent, arranged by the party it protects the tenant FROM.
//
// Confining the read to hr.Namespace costs nothing (no legitimate record is
// affected) and closes both. It is defence in depth: base tenant RBAC grants no
// Secret write today, so neither is reachable through a shipped role — but this
// path must not be the reason a widened role becomes a cross-namespace probe.
func recordedSource(projection *corev1.Secret, namespace string) (string, bool) {
	ns, name, ok := strings.Cut(projection.Annotations[SourceRefAnnotation], "/")
	if !ok || ns == "" || name == "" {
		return "", false
	}
	if ns != namespace {
		return "", false
	}
	return name, true
}

// selectorsDigest digests the ApplicationDefinition's spec.secrets — the
// selectors the lineage webhook uses to decide whether the projection is visible
// to the tenant. It is the drift signal that forces a re-admission when tenant
// visibility changes; see SelectorsDigestAnnotation.
func selectorsDigest(def *cozyv1alpha1.ApplicationDefinition) (string, error) {
	encoded, err := json.Marshal(def.Spec.Secrets)
	if err != nil {
		return "", fmt.Errorf("digest spec.secrets of ApplicationDefinition %s: %w", def.Name, err)
	}
	sum := sha256.Sum256(encoded)
	return hex.EncodeToString(sum[:8]), nil
}

// projectionData builds the projection payload and is the single write-path
// guard. It emits exactly one key — the canonical ca.crt — whose value is
// REBUILT from the certificate blocks it validated, never copied from the
// input. It refuses anything that carries a PEM private-key header or is not a
// PEM certificate. Every byte written to a projection passes through here, on
// both legs, on create and on update.
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

// refusalRetry returns how long to wait before re-examining a source whose value
// was refused by the write-path guard.
//
// A watched (label-driven) source needs no timer: the next write to it wakes the
// reconciler, so a corrected certificate is published the moment it lands.
//
// An unwatched (name-driven) source has no such event: nothing observes its
// contents. Without a resync a refusal there would be TERMINAL, and refusals on
// that leg include ordinary transients rather than only misconfiguration — an
// operator that writes a placeholder, or an empty/partial value, before the real
// certificate lands would then never be published at all. So it is re-examined
// on the resync.
func refusalRetry(src *source) time.Duration {
	if src.watched() {
		return 0
	}
	return resyncInterval
}

// missingSourceWait returns how long to wait before looking for an absent CA
// source again.
//
// A young release is bootstrapping: its operator is about to mint the CA, and
// this interval is the tenant's wait for a usable trust anchor, so it is
// short. An old release whose declared source still does not exist is not
// late — it is a release with TLS switched off, whose CA is never coming. Such
// a release must not poll at the bootstrap cadence for the lifetime of the
// cluster (a cluster full of them would turn a per-release retry into a steady
// stream of API reads), so it falls back to the normal resync. If TLS is
// enabled on it later, the resync still picks the CA up, one interval late.
func missingSourceWait(hr *helmv2.HelmRelease) time.Duration {
	if time.Since(hr.CreationTimestamp.Time) < bootstrapWindow {
		return missingSourceRetry
	}
	return resyncInterval
}

// applicationOf reads the application identity the cozystack API stamps on
// every HelmRelease it creates. A HelmRelease without it is not an
// application release.
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

// belongsToRelease attributes a Secret to a release.
//
// The explicit SourceReleaseLabel is the contract and the only attribution that
// works for a cert-manager-issued source, which carries neither an
// OwnerReference nor Helm metadata and therefore never receives the lineage
// labels either. It is checked first.
//
// The two implicit attributions are still honoured for sources that genuinely
// carry them: the lineage labels the admission webhook stamps on objects it can
// trace to an application (operator-created Secrets that do set an
// OwnerReference), and the Helm ownership label Flux stamps on every rendered
// object. Neither is required, and nothing may depend on them alone.
func belongsToRelease(s *corev1.Secret, app application, release string) bool {
	if r, ok := s.Labels[SourceReleaseLabel]; ok {
		// Once the release is named explicitly it is the whole answer: a source
		// that names a DIFFERENT release must not then be attributed to this one
		// through a stale lineage label.
		return r == release
	}
	if s.Labels[appsv1alpha1.ApplicationGroupLabel] == app.Group &&
		s.Labels[appsv1alpha1.ApplicationKindLabel] == app.Kind &&
		s.Labels[appsv1alpha1.ApplicationNameLabel] == app.Name {
		return true
	}
	return s.Labels[helmNameLabel] == release
}

// isSource reports whether a Secret opts into CA publication through the
// label. The value is pinned to "true" so a chart can turn publication off by
// flipping the label rather than deleting it.
func isSource(s *corev1.Secret) bool {
	return s.Labels[SourceLabel] == trueValue
}

// sourceKey returns the key to lift from a labelled source: the annotated
// one, or ca.crt.
func sourceKey(s *corev1.Secret) string {
	if v := strings.TrimSpace(s.Annotations[SourceKeyAnnotation]); v != "" {
		return v
	}
	return caCertKey
}

// renderSourceName renders an ApplicationDefinition's caCert.sourceSecretName
// template. The variables mirror the ones ApplicationDefinition already
// exposes for resourceNames selectors, plus the release name, since the
// operator-created CA Secrets are named after the release
// ("{{ .release }}-ca" on CloudNativePG).
func renderSourceName(tmpl string, app application, release, namespace string) (string, error) {
	t, err := template.New("caCertSourceSecretName").Option("missingkey=error").Parse(tmpl)
	if err != nil {
		return "", fmt.Errorf("parse template: %w", err)
	}
	var buf bytes.Buffer
	if err := t.Execute(&buf, map[string]string{
		"name":      app.Name,
		"kind":      strings.ToLower(app.Kind),
		"namespace": namespace,
		"release":   release,
	}); err != nil {
		return "", fmt.Errorf("render template: %w", err)
	}
	name := strings.TrimSpace(buf.String())
	if name == "" {
		return "", errors.New("rendered to an empty name")
	}
	// A template that renders cleanly to something that is not a Secret name is
	// the same class of mistake as one that does not render at all: a typo in a
	// shipped ApplicationDefinition. This is NOT a guard against hostile input —
	// the template is a platform object no tenant can write, and the values
	// substituted into it are already DNS-1123 by the time a HelmRelease carries
	// them.
	//
	// It is here for how the mistake failed. An invalid name is just a name
	// nothing is stored under, so the Get came back NotFound and the reconciler
	// read that as "the operator has not minted the CA yet" and waited — quietly,
	// permanently, with no event and no error. Checking the name turns a platform
	// typo that looks exactly like a normal bootstrap into the thing it is, and
	// routes it to the verdict this reconciler already has for the class:
	// errUnusableDeclaration warns, and pointedly does not withdraw anyone's trust
	// anchor over a bad character.
	if errs := validation.IsDNS1123Subdomain(name); len(errs) > 0 {
		return "", fmt.Errorf("rendered to %q, which is not a valid Secret name: %s", name, strings.Join(errs, "; "))
	}
	return name, nil
}

// containsPrivateKey reports whether a PEM blob carries private key material.
// Anchored to the header line: a certificate whose body or subject text
// mentions "PRIVATE KEY" is not a false positive.
func containsPrivateKey(pem string) bool {
	return privateKeyHeader.MatchString(pem)
}

func releaseOwnerRef(hr *helmv2.HelmRelease) metav1.OwnerReference {
	return metav1.OwnerReference{
		APIVersion: helmv2.GroupVersion.String(),
		Kind:       helmv2.HelmReleaseKind,
		Name:       hr.Name,
		UID:        hr.UID,
		// The projection has exactly one manager, this controller.
		Controller: ptr.To(true),
		// BlockOwnerDeletion stays false: the trust anchor must never hold up
		// the teardown of the application it belongs to.
		BlockOwnerDeletion: ptr.To(false),
	}
}

// ownedSolelyBy reports whether refs is EXACTLY the desired controller
// reference and nothing besides.
//
// Presence is not the question, and it is not enough. Kubernetes garbage
// collection removes a dependent only once EVERY owner in its list is gone, and
// an extra reference is something any Secret writer in the namespace can append
// — the API server accepts it as long as it is not a second Controller=true. A
// projection carrying one therefore SURVIVES the deletion of the HelmRelease it
// vouches for, and that owner reference is the entire mechanism by which a
// retired trust anchor is collected. The tenant would be left reading the CA of
// an application that no longer exists.
//
// Requiring sole ownership turns that into drift, which the write path then
// normalizes away by replacing the list. The cost of being wrong is one extra
// write; the cost of accepting mere presence is a projection that outlives its
// release, permanently.
func ownedSolelyBy(refs []metav1.OwnerReference, want metav1.OwnerReference) bool {
	return len(refs) == 1 && hasOwner(refs, want)
}

// hasOwner reports whether refs already carries the desired CONTROLLER reference.
//
// Both boolean flags are part of the comparison, not only the identity fields.
// The Controller flag is: a reference that names the right release but is not
// marked as the controller (hand-edited, say) leaves the projection without a
// controlling owner, and treating it as a match would let the drift check pass
// and never re-home it. BlockOwnerDeletion is too, for the symmetric reason:
// releaseOwnerRef pins it to false so the trust anchor never holds up the
// teardown of its application, and a reference that drifts it to true — which
// makes a foreground deletion of the release block on the projection — must be
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

// warn records a Warning Event on obj, so a refusal is visible without
// reading controller logs.
func (r *Reconciler) warn(obj client.Object, reason, format string, args ...any) {
	log.Log.Info("CA extraction refused",
		"reason", reason, "object", obj.GetNamespace()+"/"+obj.GetName(), "detail", fmt.Sprintf(format, args...))
	if r.Recorder != nil {
		r.Recorder.Eventf(obj, corev1.EventTypeWarning, reason, format, args...)
	}
}

// SecretCacheByObject returns the scoping for this controller's DEDICATED
// Secret cache, so its watch does not hold every Secret — and every private
// key — in the cluster. Only the label-driven sources are cached, because they
// are the only Secrets that must be WATCHED. The name-driven sources and the
// projections are read through the uncached API reader and re-read on the
// resync.
//
// It scopes a cache this controller owns, NOT the shared manager cache: a
// manager-wide Secret scoping would force one selector on every controller in
// the process, which cannot be reconciled with another controller that needs a
// different Secret scope. A private cache keeps the two independent.
//
// The selector is an EXISTS requirement, not publish-ca-cert=true: a source
// whose label is flipped to "false" must still be delivered (the reconciler
// then stops treating it as a source) rather than silently vanishing from the
// cache.
func SecretCacheByObject() cache.ByObject {
	req, err := labels.NewRequirement(SourceLabel, selection.Exists, nil)
	if err != nil {
		// Impossible: the key is a compile-time constant and a valid label
		// key. Panicking beats returning an unscoped cache that would hold
		// every Secret in the cluster.
		panic(fmt.Sprintf("build CA source label selector: %v", err))
	}
	return cache.ByObject{Label: labels.NewSelector().Add(*req)}
}

// SetupWithManager wires the reconciler to the application releases.
//
// The reconcile unit is the HelmRelease because it is the one object that
// exists before, during and after the source does — a name-driven source may
// appear minutes later, so it cannot be the trigger. Label-driven sources are
// still watched (so a rotation propagates at once) and mapped back to their
// release; ApplicationDefinitions are watched so that declaring a source for
// an engine takes effect without waiting for a resync.
//
// The Secret watch is deliberately NOT the manager's cache. secretSource is a
// DEDICATED cache scoped to labelled CA sources (SecretCacheByObject), owned by
// this controller alone. That keeps every other Secret — and so every private
// key in the cluster — out of an informer, without narrowing the shared
// manager cache and thereby colliding with any other controller that wants to
// scope Secrets differently. The ApplicationDefinition watch and the
// HelmRelease trigger stay on the manager cache, where they belong.
func (r *Reconciler) SetupWithManager(mgr ctrl.Manager, secretSource cache.Cache) error {
	return ctrl.NewControllerManagedBy(mgr).
		Named("cacert").
		For(&helmv2.HelmRelease{}).
		WatchesRawSource(crsource.Kind(secretSource, &corev1.Secret{},
			handler.TypedEnqueueRequestsFromMapFunc(r.releaseOfSecret),
			predicate.NewTypedPredicateFuncs(sourceCandidate),
		)).
		Watches(&cozyv1alpha1.ApplicationDefinition{},
			handler.EnqueueRequestsFromMapFunc(r.releasesOfApplicationDefinition),
		).
		Complete(r)
}

// sourceCandidate reports whether a Secret is worth mapping back to a release.
// It is deliberately WIDER than isSource: a Secret whose label was flipped to
// "false" must still reach the reconciler, which then re-resolves the release's
// source from scratch.
func sourceCandidate(secret *corev1.Secret) bool {
	_, labelled := secret.GetLabels()[SourceLabel]
	return labelled
}

// releaseOfSecret maps a labelled source Secret back to the release that owns
// it, so a rotation on the label-driven leg propagates immediately.
func (r *Reconciler) releaseOfSecret(ctx context.Context, secret *corev1.Secret) []reconcile.Request {
	group := secret.Labels[appsv1alpha1.ApplicationGroupLabel]
	kind := secret.Labels[appsv1alpha1.ApplicationKindLabel]
	name := secret.Labels[appsv1alpha1.ApplicationNameLabel]

	// The release label and the Helm ownership label both name the release
	// directly; the lineage labels need a lookup. Prefer the direct answers.
	if release := secret.Labels[SourceReleaseLabel]; release != "" {
		return []reconcile.Request{{NamespacedName: types.NamespacedName{
			Namespace: secret.Namespace, Name: release,
		}}}
	}
	if release := secret.Labels[helmNameLabel]; release != "" {
		return []reconcile.Request{{NamespacedName: types.NamespacedName{
			Namespace: secret.Namespace, Name: release,
		}}}
	}
	if group == "" || kind == "" || name == "" {
		// The Secret asks to be published but names no release, carries no Helm
		// metadata and was given no lineage labels — so there is no release to
		// own, or even to name, its projection. This is the failure a chart
		// author hits when they add the publish label to a cert-manager
		// Certificate and forget SourceReleaseLabel, so it must be loud: every
		// other refusal in this controller is a Warning Event, and a silent log
		// line here would leave them with a trust anchor that simply never
		// appears and nothing to explain why.
		if isSource(secret) {
			r.warn(secret, reasonUnattributableSource,
				"Secret is labelled %s but names no release: add %s (for example \"{{ .Release.Name }}\" in Certificate.spec.secretTemplate.labels), or its CA cannot be published",
				SourceLabel, SourceReleaseLabel)
		}
		log.FromContext(ctx).Info("labelled CA source belongs to no application release; ignoring it",
			"secret", secret.Namespace+"/"+secret.Name, "label", SourceLabel)
		return nil
	}

	list := &helmv2.HelmReleaseList{}
	if err := r.List(ctx, list,
		client.InNamespace(secret.Namespace),
		client.MatchingLabels{
			appsv1alpha1.ApplicationGroupLabel: group,
			appsv1alpha1.ApplicationKindLabel:  kind,
			appsv1alpha1.ApplicationNameLabel:  name,
		},
	); err != nil {
		log.FromContext(ctx).Error(err, "map CA source to its release", "secret", secret.Namespace+"/"+secret.Name)
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

// releasesOfApplicationDefinition maps an ApplicationDefinition to every
// release of its kind, so adding (or correcting) a caCert declaration takes
// effect at once rather than on the next resync.
func (r *Reconciler) releasesOfApplicationDefinition(ctx context.Context, obj client.Object) []reconcile.Request {
	def, ok := obj.(*cozyv1alpha1.ApplicationDefinition)
	if !ok {
		return nil
	}
	list := &helmv2.HelmReleaseList{}
	if err := r.List(ctx, list, client.MatchingLabels{
		appsv1alpha1.ApplicationKindLabel: def.Spec.Application.Kind,
	}); err != nil {
		log.FromContext(ctx).Error(err, "map ApplicationDefinition to its releases", "definition", def.Name)
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
