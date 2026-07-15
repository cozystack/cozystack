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

// Package wildcardsecret replicates the operator-provided wildcard TLS
// Secret into the tenant namespaces that terminate TLS, so per-tenant
// ingress controllers and Gateways can serve it from their own namespace
// without any cross-namespace Secret read.
//
// The root-tenant MVP wired the root tenant to an operator-supplied
// wildcard Secret: only the Secret NAME rides the platform values channel
// (never the key material), the root ingress controller serves it via
// --default-ssl-certificate, and the root Gateway references it in
// certMode=existingSecret. That MVP is same-namespace only. Child tenants
// run their own ingress controller / Gateway in their own namespace under
// a default-deny policy, so the one operator Secret must be replicated
// into each tenant namespace that owns a termination point.
//
// This controller does that replication with no extra operator input: it
// reads the same platform values channel the consumers read
// (cozy-system/cozystack-values), takes the wildcard Secret name from
// _cluster.wildcard-secret-name and the publishing namespace from
// _cluster.expose-ingress, and mirrors that Secret into every tenant
// namespace that owns a termination point. Because the source is derived
// from the same value that makes the consumers reference it, the replica
// is created whenever the consumers expect it — no manual labelling, and
// no window where a child controller references a Secret that will never
// exist. Clearing _cluster.wildcard-secret-name (disabling the feature) is
// the only path that prunes every replica. A values channel that is absent
// or unreadable, or a source Secret that is merely missing or mistyped,
// leaves existing replicas in place — so a transient gap, a delete+recreate
// rotation, or a misconfiguration never drops tenant TLS.
//
// Cache footprint: the manager's Secret informer is scoped (see
// SecretCacheByObject, wired in cmd/cozystack-controller/main.go) to the
// managed replicas and the values channel only — never every Secret in the
// cluster. Most reads in Reconcile are served from the cache to keep apiserver
// load down: the watched values channel, the namespace list, and the managed-
// replica prune list. Only two reads go through the uncached APIReader — the
// dynamic-source Get (the source's name is not in the scoped cache) and the
// foreign-collision check (a colliding Secret carries no CopyLabel, so its
// labels are absent from the scoped cache). The source is not watched; an
// in-place rotation is picked up by the periodic resync (sourceResyncInterval).
//
// Invariant preserved from the MVP: the wildcard is deliberately
// distributed to tenant namespaces, but no tenant RBAC is widened — every
// consumer reads only its own-namespace copy. The replica does carry the
// wildcard private key into each terminating tenant namespace; that is the
// same exposure as the per-host ACME Secret it replaces (which already
// lived there), except the key is now shared across tenants.
package wildcardsecret

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"maps"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/fields"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	utilerrors "k8s.io/apimachinery/pkg/util/errors"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	sigsyaml "sigs.k8s.io/yaml"
)

const (
	// platformValues* identify the system-scope values channel the
	// platform writes (packages/core/platform/templates/apps.yaml). The
	// controller reads the wildcard source identity from it.
	platformValuesNamespace = "cozy-system"
	platformValuesName      = "cozystack-values"
	platformValuesKey       = "values.yaml"

	// CopyLabel marks a Secret as a controller-managed replica. The
	// reconciler only ever updates or deletes Secrets bearing this label,
	// so a user Secret that happens to share the name is never touched. It
	// is also the cache scope (see SecretCacheByObject).
	CopyLabel = "cozystack.io/wildcard-secret-copy"

	// SourceRefAnnotation records the "<namespace>/<name>" of the source
	// on every replica for traceability.
	SourceRefAnnotation = "cozystack.io/wildcard-secret-source"

	// ingressOwnerLabel / gatewayOwnerLabel name the tenant namespace
	// labels written by the apps/tenant chart. A namespace OWNS a
	// termination point (and therefore needs the wildcard locally) when
	// the label value equals the namespace's own name; a value pointing
	// at an ancestor means the namespace merely inherits and does not run
	// its own controller / Gateway.
	ingressOwnerLabel = "namespace.cozystack.io/ingress"
	gatewayOwnerLabel = "namespace.cozystack.io/gateway"

	// defaultPublishingNamespace is the fallback publishing namespace when
	// the values channel does not carry expose-ingress.
	defaultPublishingNamespace = "tenant-root"

	// sourceResyncInterval bounds how stale a tenant replica can be after
	// the operator rotates the source Secret in place. The source is not
	// watched (it is not cached — see SecretCacheByObject), so the active
	// reconcile re-reads it on this cadence to propagate a rotation or pick
	// up the source first appearing. The publishing tenant serves the
	// source directly with no lag; only the replicated tenants see up to
	// this delay, which is immaterial for certificate rotation (the
	// previous certificate stays valid well past it).
	sourceResyncInterval = 5 * time.Minute
)

var (
	// configKey is the singleton reconcile key. Every watched event maps
	// to it; Reconcile re-reads the platform values channel and performs a
	// full sync, so the request payload itself is irrelevant.
	configKey = types.NamespacedName{Namespace: platformValuesNamespace, Name: platformValuesName}

	// errForeignCollision marks the one non-retryable per-namespace
	// outcome: a Secret of the target name already exists and is not a
	// managed replica. It is skipped, never requeued — retrying cannot
	// help and must not clobber a user Secret.
	errForeignCollision = errors.New("a non-managed Secret of the same name exists")
)

// Reconciler replicates the operator wildcard Secret into tenant
// termination namespaces.
type Reconciler struct {
	client.Client
	// Reader is the manager's uncached APIReader. It is used only for the two
	// reads the scoped cache cannot serve — the operator source (dynamic name,
	// not in the cache) and the foreign-collision check (a colliding Secret
	// carries no CopyLabel, so its labels are absent from the cache). Every
	// other read (the values channel, namespaces, and managed replicas) goes
	// through the embedded cached Client to keep apiserver load down.
	Reader client.Reader
	// Scheme is kept for construction symmetry with the other reconcilers
	// in this manager (all built with Client+Scheme in main.go). It is
	// intentionally unused here: replicas are tracked by label and
	// annotation, not owner references — a cross-namespace owner reference
	// (replica in a tenant namespace, source elsewhere) would be invalid.
	Scheme *runtime.Scheme
	// Recorder surfaces a Warning Event when a foreign Secret collision
	// makes the controller skip a namespace, so the otherwise-silent skip
	// is visible via `kubectl get events -n <namespace>` without reading
	// controller logs.
	Recorder record.EventRecorder
}

// platformValues is the subset of the _cluster channel this controller
// needs: which Secret is the operator wildcard, and which namespace
// publishes it.
type platformValues struct {
	Cluster struct {
		// WildcardSecretName is a pointer so an ABSENT key (nil) — a
		// partial or older values channel — is distinguishable from an
		// explicitly empty value (a deliberate disable). Only the explicit
		// empty prunes; absence keeps replicas.
		WildcardSecretName *string `json:"wildcard-secret-name"`
		ExposeIngress      string  `json:"expose-ingress"`
	} `json:"_cluster"`
}

// +kubebuilder:rbac:groups="",resources=secrets,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups="",resources=namespaces,verbs=get;list;watch

// Reconcile performs a full sync: it reads the wildcard source identity
// from the platform values channel, mirrors the source into every tenant
// namespace that owns a termination point, and prunes replicas that no
// longer belong. Only an explicit disable (channel present, empty wildcard
// name) prunes everything; an absent channel or an absent/non-TLS source
// keeps existing replicas. While the feature is active the result requeues
// on sourceResyncInterval so an in-place source rotation propagates even
// though the source is not watched.
func (r *Reconciler) Reconcile(ctx context.Context, _ ctrl.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	name, pubNS, present, err := r.readConfig(ctx)
	if err != nil {
		return ctrl.Result{}, err
	}
	if !present {
		// The platform values channel is absent or unreadable, so the
		// desired state is unknown. Keep existing replicas — a transient
		// loss of the channel must not drop tenant TLS cluster-wide. The
		// channel is watched, so its reappearance re-triggers; no requeue.
		logger.Info("platform values channel absent or unreadable; keeping existing replicas")
		return ctrl.Result{}, nil
	}
	if name == "" {
		// Feature explicitly disabled: the channel is present and the
		// operator cleared the wildcard name → tear every replica down.
		// This is the ONLY destructive, prune-everything path. The channel
		// is watched, so a re-enable re-triggers; no requeue.
		return ctrl.Result{}, r.pruneCopies(ctx, nil, "", "")
	}

	// Feature active. The source is not cached/watched (its name is
	// dynamic), so from here the result requeues on sourceResyncInterval to
	// pick up an in-place rotation or the source first appearing.
	src := &corev1.Secret{}
	err = r.Reader.Get(ctx, types.NamespacedName{Namespace: pubNS, Name: name}, src)
	switch {
	case apierrors.IsNotFound(err):
		// Named but absent: not yet created, mid-rotation, or the publishing
		// namespace was misresolved (the overloaded expose-ingress — see
		// readConfig). Keep existing replicas and poll. Name the resolved
		// namespace prominently so an operator can tell a one-off "not
		// created yet" from a persistent split-deployment misconfiguration
		// (where the source lives in a different namespace than expose-
		// ingress resolves to) — otherwise the feature would silently no-op.
		logger.Info("wildcard source Secret not found in the resolved publishing namespace; keeping existing replicas and retrying — if this persists, verify expose-ingress resolves to the namespace that holds the source",
			"publishingNamespace", pubNS, "sourceName", name)
		return ctrl.Result{RequeueAfter: sourceResyncInterval}, nil
	case err != nil:
		return ctrl.Result{}, fmt.Errorf("get source secret %s/%s: %w", pubNS, name, err)
	}
	if src.Type != corev1.SecretTypeTLS {
		// Misconfigured source type — keep replicas, poll for a fix.
		logger.Info("wildcard source is not a kubernetes.io/tls Secret; keeping existing replicas",
			"secret", pubNS+"/"+name, "type", src.Type)
		return ctrl.Result{RequeueAfter: sourceResyncInterval}, nil
	}

	targets, err := r.terminationNamespaces(ctx, pubNS)
	if err != nil {
		return ctrl.Result{}, err
	}

	var errs []error
	for _, ns := range targets {
		if err := r.upsertCopy(ctx, src, ns); err != nil {
			// A foreign-Secret collision is terminal for that namespace —
			// skip it, do not requeue on it. Any other error is transient,
			// so aggregate and return it for a back-off requeue.
			if errors.Is(err, errForeignCollision) {
				logger.Info("skipping wildcard replica: a non-managed Secret of the same name exists", "namespace", ns)
				continue
			}
			errs = append(errs, err)
		}
	}
	if err := r.pruneCopies(ctx, targets, pubNS, name); err != nil {
		errs = append(errs, err)
	}
	if err := utilerrors.NewAggregate(errs); err != nil {
		// Surface transient errors for an immediate back-off requeue; the
		// periodic resync is the steady-state fallback.
		return ctrl.Result{}, err
	}
	return ctrl.Result{RequeueAfter: sourceResyncInterval}, nil
}

// readConfig returns the wildcard source name, the publishing namespace,
// and whether the platform values channel was present and readable. An
// absent or unreadable channel — or a present channel that omits the
// wildcard-secret-name key — returns present=false (desired state unknown
// → the caller keeps existing replicas), which is deliberately distinct
// from a present channel carrying an explicitly empty name (an explicit
// disable that prunes).
//
// The publishing namespace is read from _cluster.expose-ingress. NOTE:
// packages/core/platform/templates/apps.yaml documents that expose-ingress
// is overloaded — it is simultaneously an ingressClassName and the
// publishing namespace, and the two coincide only in the default
// deployment (tenant-root). This controller relies on the publishing-
// namespace meaning, matching the MVP ingress/gateway consumers. If a
// future deployment ever splits the two (a separate
// _cluster.gateway-namespace key, as that comment anticipates), this read
// must follow it. Until then a split would make the source resolve in the
// wrong namespace and surface as a NotFound source — which Reconcile
// handles non-destructively (replicas are kept, never pruned, on absence)
// and logs a warning naming the namespace it looked in.
func (r *Reconciler) readConfig(ctx context.Context) (name, namespace string, present bool, err error) {
	values := &corev1.Secret{}
	err = r.Get(ctx, configKey, values)
	if apierrors.IsNotFound(err) {
		return "", "", false, nil
	}
	if err != nil {
		return "", "", false, fmt.Errorf("get platform values: %w", err)
	}
	raw, ok := values.Data[platformValuesKey]
	if !ok {
		return "", "", false, nil
	}
	var pv platformValues
	if err := sigsyaml.Unmarshal(raw, &pv); err != nil {
		// A malformed channel is surfaced as an error (Reconcile bails
		// before any prune and requeues), never as a disable.
		return "", "", false, fmt.Errorf("parse platform values: %w", err)
	}
	if pv.Cluster.WildcardSecretName == nil {
		// The wildcard-secret-name key is absent (a partial or older
		// values channel). Desired state is unknown → present=false so the
		// caller keeps existing replicas; this is never a disable. The
		// platform always writes the key, so this guards a misrender.
		return "", "", false, nil
	}
	ns := pv.Cluster.ExposeIngress
	if ns == "" {
		ns = defaultPublishingNamespace
	}
	return *pv.Cluster.WildcardSecretName, ns, true, nil
}

// terminationNamespaces returns every tenant namespace that owns a TLS
// termination point, excluding the publishing namespace (the operator
// Secret already lives there and must not be overwritten by a replica).
func (r *Reconciler) terminationNamespaces(ctx context.Context, sourceNS string) ([]string, error) {
	list := &corev1.NamespaceList{}
	if err := r.List(ctx, list); err != nil {
		return nil, fmt.Errorf("list namespaces: %w", err)
	}
	var out []string
	for i := range list.Items {
		ns := &list.Items[i]
		if ns.Name == sourceNS {
			continue
		}
		if ns.DeletionTimestamp != nil {
			// The namespace is terminating (a tenant teardown). Its owner
			// labels linger until GC completes, but the API server forbids
			// creating a replica in a Terminating namespace; skip it so a
			// routine tenant deletion does not produce a burst of
			// forbidden-error requeues.
			continue
		}
		if ownsTerminationPoint(ns) {
			out = append(out, ns.Name)
		}
	}
	return out, nil
}

// ownsTerminationPoint reports whether a namespace runs its own ingress
// controller or Gateway — true exactly when an owner label equals the
// namespace's own name.
func ownsTerminationPoint(ns *corev1.Namespace) bool {
	return ns.Labels[ingressOwnerLabel] == ns.Name || ns.Labels[gatewayOwnerLabel] == ns.Name
}

// upsertCopy creates or refreshes the replica of src in namespace ns. It
// returns errForeignCollision (wrapped) when a Secret of the same name
// exists that the controller does not own, so a pre-existing user Secret
// is preserved. The existence check goes through the uncached Reader so a
// foreign Secret (absent from the replica-scoped cache) is still seen.
func (r *Reconciler) upsertCopy(ctx context.Context, src *corev1.Secret, ns string) error {
	existing := &corev1.Secret{}
	err := r.Reader.Get(ctx, types.NamespacedName{Namespace: ns, Name: src.Name}, existing)
	switch {
	case apierrors.IsNotFound(err):
		replica := &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Namespace:   ns,
				Name:        src.Name,
				Labels:      map[string]string{CopyLabel: "true"},
				Annotations: map[string]string{SourceRefAnnotation: sourceRef(src)},
			},
			Type: src.Type,
			Data: cloneData(src.Data),
		}
		if err := r.Create(ctx, replica); err != nil {
			return fmt.Errorf("create replica in %s: %w", ns, err)
		}
		return nil
	case err != nil:
		return fmt.Errorf("get replica in %s: %w", ns, err)
	}

	if existing.Labels[CopyLabel] != "true" {
		// Surface the skip as a Warning Event on the colliding Secret (which
		// lives in the affected namespace), so an operator sees it via
		// `kubectl get events -n <ns>` rather than only in controller logs.
		if r.Recorder != nil {
			r.Recorder.Eventf(existing, corev1.EventTypeWarning, "WildcardReplicaSkipped",
				"a Secret named %q already exists and is not managed by the wildcard-secret controller; the operator wildcard certificate is not served in this namespace until that Secret is removed", src.Name)
		}
		return fmt.Errorf("%w: %s/%s", errForeignCollision, ns, src.Name)
	}

	desired := cloneData(src.Data)
	if maps.EqualFunc(existing.Data, desired, bytes.Equal) &&
		existing.Annotations[SourceRefAnnotation] == sourceRef(src) {
		return nil
	}
	// Re-assert the marker label, the data, and the back-reference so the
	// replica's managed-by invariant is explicit even after an update.
	if existing.Labels == nil {
		existing.Labels = map[string]string{}
	}
	existing.Labels[CopyLabel] = "true"
	if existing.Annotations == nil {
		existing.Annotations = map[string]string{}
	}
	existing.Annotations[SourceRefAnnotation] = sourceRef(src)
	existing.Data = desired
	if err := r.Update(ctx, existing); err != nil {
		return fmt.Errorf("update replica in %s: %w", ns, err)
	}
	return nil
}

// pruneCopies deletes every managed replica that is not the current source
// name in a kept namespace. With keep == nil (or sourceName == "") it
// removes all managed replicas — used when the feature is off or the
// source is gone. It NEVER deletes the Secret at the source location
// (sourceNS/sourceName), even if that Secret carries the copy label: the
// source namespace is excluded from keep, so without this guard a stale
// replica sitting at the source slot (e.g. after the publishing namespace
// switched to a former target) would be the very Secret the controller
// reads as its source, and deleting it would flap the source.
func (r *Reconciler) pruneCopies(ctx context.Context, keep []string, sourceNS, sourceName string) error {
	list := &corev1.SecretList{}
	if err := r.List(ctx, list, client.MatchingLabels{CopyLabel: "true"}); err != nil {
		return fmt.Errorf("list wildcard replicas: %w", err)
	}
	keepSet := make(map[string]struct{}, len(keep))
	for _, ns := range keep {
		keepSet[ns] = struct{}{}
	}
	var errs []error
	for i := range list.Items {
		replica := &list.Items[i]
		if sourceName != "" && replica.Namespace == sourceNS && replica.Name == sourceName {
			continue
		}
		if _, kept := keepSet[replica.Namespace]; kept && replica.Name == sourceName {
			continue
		}
		if err := r.Delete(ctx, replica); err != nil && !apierrors.IsNotFound(err) {
			errs = append(errs, fmt.Errorf("delete stale replica %s/%s: %w", replica.Namespace, replica.Name, err))
		}
	}
	return utilerrors.NewAggregate(errs)
}

func sourceRef(src *corev1.Secret) string {
	return src.Namespace + "/" + src.Name
}

func cloneData(in map[string][]byte) map[string][]byte {
	out := make(map[string][]byte, len(in))
	for k, v := range in {
		b := make([]byte, len(v))
		copy(b, v)
		out[k] = b
	}
	return out
}

// SecretCacheByObject returns the cache scoping the cozystack-controller
// manager must apply to corev1.Secret so this controller does not cache
// every Secret in the cluster. It caches only what must be WATCHED:
// managed replicas (CopyLabel, in any namespace) and the platform values
// channel (cozy-system/cozystack-values). The operator source Secret is
// deliberately NOT cached — its name is dynamic, so it cannot be scoped by
// selector; it is read via the uncached APIReader and its rotation is
// picked up by the periodic resync. The values channel carries no tenant
// private keys, so the only key material the cache holds is the replicas',
// which already exist in tenant namespaces.
func SecretCacheByObject() cache.ByObject {
	return cache.ByObject{
		Namespaces: map[string]cache.Config{
			cache.AllNamespaces: {
				LabelSelector: labels.SelectorFromSet(labels.Set{CopyLabel: "true"}),
			},
			platformValuesNamespace: {
				FieldSelector: fields.OneTermEqualSelector("metadata.name", platformValuesName),
			},
		},
	}
}

// SetupWithManager wires the reconciler as a singleton: every watched
// event maps to the one config key. The manager's Secret cache is scoped
// (SecretCacheByObject) to managed replicas + the values channel, so this
// Secret watch only ever delivers those — replica self-heal and feature
// enable/disable are immediate. The Namespace watch reacts to a tenant
// gaining or losing a termination point. The operator source is not
// watched; its rotation is handled by the result requeue in Reconcile.
func (r *Reconciler) SetupWithManager(mgr ctrl.Manager) error {
	toSingleton := handler.EnqueueRequestsFromMapFunc(enqueueConfigKey)
	return ctrl.NewControllerManagedBy(mgr).
		Named("wildcardsecret").
		Watches(&corev1.Secret{}, toSingleton).
		Watches(&corev1.Namespace{}, toSingleton).
		Complete(r)
}

// enqueueConfigKey maps every watched event to the single config key, so
// any relevant change — a managed replica edited or deleted out of band,
// the values channel toggling the feature, or a namespace gaining/losing a
// termination point — triggers one full resync. The manager's Secret cache
// is scoped (SecretCacheByObject) to replicas + the values channel, so the
// Secret watch only ever delivers those; this mapper needs no filtering.
func enqueueConfigKey(context.Context, client.Object) []reconcile.Request {
	return []reconcile.Request{{NamespacedName: configKey}}
}
