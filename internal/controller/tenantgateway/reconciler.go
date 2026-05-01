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

// Package tenantgateway hosts the controller that reconciles
// gateway.cozystack.io/v1alpha1 TenantGateway resources into the actual
// Gateway API resources (Gateway, HTTPRoute, TLSRoute) and cert-manager
// Certificate objects required to publish a tenant's apps.
//
// The chart at packages/extra/gateway renders TenantGateway CRs; this
// controller owns everything downstream so that Helm-vs-controller
// races on Gateway.spec.listeners do not happen.
package tenantgateway

import (
	"context"
	"fmt"
	"sort"

	cmv1 "github.com/cert-manager/cert-manager/pkg/apis/certmanager/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/log"
	gatewayv1 "sigs.k8s.io/gateway-api/apis/v1"
	gatewayv1alpha2 "sigs.k8s.io/gateway-api/apis/v1alpha2"

	gatewayv1alpha1 "github.com/cozystack/cozystack/api/gateway/v1alpha1"
)

// gatewayCertificateName returns the cert-manager Certificate name for
// the tenant Gateway's wildcard cert (DNS-01 mode only). Per-listener
// certs in HTTP-01 mode are named separately (Commit 11).
func gatewayCertificateName(tgw *gatewayv1alpha1.TenantGateway) string {
	return tgw.Name + "-gateway-tls"
}

// gatewayIssuerName returns the per-tenant ACME Issuer name. The
// Issuer lives in the same namespace as the TenantGateway and is
// referenced by every Certificate this controller renders.
func gatewayIssuerName(tgw *gatewayv1alpha1.TenantGateway) string {
	return tgw.Name + "-gateway"
}

const (
	letsencryptProdServer  = "https://acme-v02.api.letsencrypt.org/directory"
	letsencryptStageServer = "https://acme-staging-v02.api.letsencrypt.org/directory"
)

// +kubebuilder:rbac:groups=gateway.cozystack.io,resources=tenantgateways,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=gateway.cozystack.io,resources=tenantgateways/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=gateway.cozystack.io,resources=tenantgateways/finalizers,verbs=update
// +kubebuilder:rbac:groups=gateway.networking.k8s.io,resources=gateways;httproutes;tlsroutes,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=gateway.networking.k8s.io,resources=gateways/status;httproutes/status;tlsroutes/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=cert-manager.io,resources=certificates;issuers,verbs=get;list;watch;create;update;patch;delete

// Reconciler reconciles TenantGateway resources, owning the downstream
// Gateway and Certificate state.
type Reconciler struct {
	client.Client
	Scheme *runtime.Scheme
}

// Reconcile renders the desired Gateway from a TenantGateway spec.
// HTTP-01 mode: static `http` listener on port 80 (for ACME), per-app
// HTTPS listeners are added by route-driven reconciliation in later
// commits. DNS-01 mode: `http` plus the wildcard `https` and apex
// `https-apex` HTTPS listeners that the chart used to render directly.
func (r *Reconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	tgw := &gatewayv1alpha1.TenantGateway{}
	if err := r.Get(ctx, req.NamespacedName, tgw); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	if err := r.runReconcileSteps(ctx, tgw); err != nil {
		// Surface the failure on the TenantGateway status so
		// operators see something in `kubectl get tgw` rather than
		// a silent stale Ready condition while the controller
		// hot-loops in logs.
		if statusErr := r.markFailed(ctx, tgw, err); statusErr != nil {
			return ctrl.Result{}, fmt.Errorf("reconcile failed: %w (status update also failed: %v)", err, statusErr)
		}
		return ctrl.Result{}, err
	}

	return ctrl.Result{}, nil
}

// runReconcileSteps executes the desired-state work in order. Splitting
// out from Reconcile keeps the error-handling/status-update wrapper
// in one place.
func (r *Reconciler) runReconcileSteps(ctx context.Context, tgw *gatewayv1alpha1.TenantGateway) error {
	claims, err := r.collectHostnameClaims(ctx, tgw)
	if err != nil {
		return fmt.Errorf("collect attached hostnames: %w", err)
	}
	winners, losers := resolveHostnameOwners(claims)

	dynHostnames := make([]string, 0, len(winners))
	for h := range winners {
		dynHostnames = append(dynHostnames, h)
	}
	sort.Strings(dynHostnames)

	// allRefs is the full set of (route, parentRef) tuples that
	// attached to this Gateway, including duplicate parentRefs from
	// the same route. Each tuple owns its own RouteParentStatus
	// entry per Gateway API's per-(parentRef, controllerName)
	// status contract.
	allRefs := map[routeRef]struct{}{}
	for _, refs := range claims {
		for _, ref := range refs {
			allRefs[ref] = struct{}{}
		}
	}

	if err := r.reconcileGateway(ctx, tgw, dynHostnames); err != nil {
		return err
	}
	if err := r.reconcileIssuer(ctx, tgw); err != nil {
		return err
	}
	if err := r.reconcileWildcardCertificate(ctx, tgw); err != nil {
		return err
	}
	if err := r.reconcilePerListenerCertificates(ctx, tgw, dynHostnames); err != nil {
		return err
	}
	if err := r.updateRouteStatuses(ctx, tgw, allRefs, losers); err != nil {
		return err
	}
	if err := r.reconcileHTTPToHTTPSRedirect(ctx, tgw); err != nil {
		return err
	}
	return r.reconcileStatus(ctx, tgw, dynHostnames)
}

// markFailed writes a Ready=False condition with Reason=ReconcileError
// and the underlying error message. controller-runtime will requeue
// from the returned error so the next reconcile attempts to clear
// the failure.
func (r *Reconciler) markFailed(ctx context.Context, tgw *gatewayv1alpha1.TenantGateway, cause error) error {
	cond := metav1.Condition{
		Type:               "Ready",
		Status:             metav1.ConditionFalse,
		ObservedGeneration: tgw.Generation,
		Reason:             "ReconcileError",
		Message:            cause.Error(),
	}
	stale := tgw.DeepCopy()
	stale.Status.ObservedGeneration = tgw.Generation
	apimeta.SetStatusCondition(&stale.Status.Conditions, cond)
	if statusEqual(tgw.Status, stale.Status) {
		return nil
	}
	tgw.Status = stale.Status
	return r.Status().Update(ctx, tgw)
}

// reconcileHTTPToHTTPSRedirect ensures a controller-owned HTTPRoute
// named "<tgw>-http-redirect" attached to sectionName=http on the
// tenant Gateway. The route carries a single RequestRedirect filter
// (scheme=https, status=301) so plaintext requests landing on port
// 80 do not silently reach app backends. App-owned HTTPRoutes
// attaching by hostname without sectionName otherwise pick up the
// HTTP listener too — Harbor / dashboard / keycloak credentials in
// the clear. The redirect HTTPRoute matches any host on path /.
func (r *Reconciler) reconcileHTTPToHTTPSRedirect(ctx context.Context, tgw *gatewayv1alpha1.TenantGateway) error {
	logger := log.FromContext(ctx)
	desired, err := r.renderHTTPRedirect(tgw)
	if err != nil {
		return fmt.Errorf("render redirect HTTPRoute: %w", err)
	}

	existing := &gatewayv1.HTTPRoute{}
	getErr := r.Get(ctx, types.NamespacedName{Namespace: desired.Namespace, Name: desired.Name}, existing)
	switch {
	case apierrors.IsNotFound(getErr):
		if err := r.Create(ctx, desired); err != nil {
			return fmt.Errorf("create redirect HTTPRoute: %w", err)
		}
		logger.V(1).Info("created redirect HTTPRoute", "name", desired.Name, "namespace", desired.Namespace)
	case getErr != nil:
		return fmt.Errorf("get redirect HTTPRoute: %w", getErr)
	default:
		// Refuse to silently take over a pre-existing HTTPRoute that
		// shares our derived name but is not owned by this
		// TenantGateway. Without this guard, an operator who
		// hand-crafted a `<tgw>-http-redirect` route loses their
		// configuration on the first reconcile (we'd overwrite
		// `existing.Spec` and never set the OwnerReference, so
		// `kubectl delete tenantgateway` later wouldn't cascade
		// the route either — leaving it orphaned with mutated
		// content). Surface the conflict instead.
		if !ownedByTenantGateway(existing.OwnerReferences, tgw) {
			return fmt.Errorf("httproute %s/%s exists but is not owned by TenantGateway %s; refusing to take over (delete it manually if you want the controller to manage this route)", desired.Namespace, desired.Name, tgw.Name)
		}
		if equality.Semantic.DeepEqual(existing.Spec, desired.Spec) {
			return nil
		}
		existing.Spec = desired.Spec
		if err := r.Update(ctx, existing); err != nil {
			return fmt.Errorf("update redirect HTTPRoute: %w", err)
		}
	}
	return nil
}

// collectHostnameClaims lists HTTPRoutes and TLSRoutes cluster-wide
// and returns a map of hostname -> []routeRef of routes claiming
// it via parentRefs targeting this TenantGateway's Gateway. Routes
// whose namespace is not the tenant namespace and not in
// Spec.AttachedNamespaces are filtered out — Gateway listener
// allowedRoutes selectors reject those routes at runtime, but the
// reconciler must not provision certs / listeners for them either
// (each unused cert eats LE rate limits and leaks the operator's
// reachable hostname set). Empty map in DNS-01 mode (wildcard
// handles everything).
func (r *Reconciler) collectHostnameClaims(ctx context.Context, tgw *gatewayv1alpha1.TenantGateway) (map[string][]routeRef, error) {
	if tgw.Spec.CertMode == gatewayv1alpha1.CertModeDNS01 {
		return nil, nil
	}

	allowed := map[string]struct{}{tgw.Namespace: {}}
	for _, ns := range tgw.Spec.AttachedNamespaces {
		if ns == "" {
			continue
		}
		allowed[ns] = struct{}{}
	}

	out := map[string][]routeRef{}

	httpRoutes := &gatewayv1.HTTPRouteList{}
	if err := r.List(ctx, httpRoutes); err != nil {
		return nil, fmt.Errorf("list HTTPRoutes: %w", err)
	}
	for i := range httpRoutes.Items {
		route := &httpRoutes.Items[i]
		if _, ok := allowed[route.Namespace]; !ok {
			continue
		}
		matchingRefs := allAttachingParentRefs(route.Spec.ParentRefs, route.Namespace, tgw)
		if len(matchingRefs) == 0 {
			continue
		}
		// One routeRef per matching parentRef so each attachment point
		// owns its own RouteParentStatus entry (Gateway API's
		// per-(parentRef, controllerName) status contract). Hostname
		// claims accumulate across refs — the same hostname declared
		// once on the route claims via every matching parent.
		for _, matchingRef := range matchingRefs {
			ref := routeRef{
				kind:      routeKindHTTP,
				namespace: route.Namespace,
				name:      route.Name,
				parentRef: matchingRef,
			}
			for _, h := range route.Spec.Hostnames {
				out[string(h)] = append(out[string(h)], ref)
			}
		}
	}

	tlsRoutes := &gatewayv1alpha2.TLSRouteList{}
	if err := r.List(ctx, tlsRoutes); err != nil {
		return nil, fmt.Errorf("list TLSRoutes: %w", err)
	}
	for i := range tlsRoutes.Items {
		route := &tlsRoutes.Items[i]
		if _, ok := allowed[route.Namespace]; !ok {
			continue
		}
		matchingRefs := allAttachingParentRefs(route.Spec.ParentRefs, route.Namespace, tgw)
		if len(matchingRefs) == 0 {
			continue
		}
		for _, matchingRef := range matchingRefs {
			ref := routeRef{
				kind:      routeKindTLS,
				namespace: route.Namespace,
				name:      route.Name,
				parentRef: matchingRef,
			}
			for _, h := range route.Spec.Hostnames {
				out[string(h)] = append(out[string(h)], ref)
			}
		}
	}
	return out, nil
}

// pickAttachingParentRef returns the first ParentRef in refs that
// attaches to tgw's Gateway, plus a boolean ok. Used by the mapper
// for cheap "does this route attach at all?" queries; for hostname
// collection and status updates, callers should use
// allAttachingParentRefs to handle multi-parentRef routes correctly.
func pickAttachingParentRef(refs []gatewayv1.ParentReference, routeNs string, tgw *gatewayv1alpha1.TenantGateway) (gatewayv1.ParentReference, bool) {
	for _, ref := range refs {
		if parentRefAttachesTo(ref, routeNs, tgw) {
			return ref, true
		}
	}
	return gatewayv1.ParentReference{}, false
}

// allAttachingParentRefs returns every ParentRef in refs that attaches
// to tgw's Gateway. Per Gateway API, a route may carry multiple
// parentRefs to the same Gateway (e.g. one per sectionName) and each
// (parentRef, controllerName) pair owns its own RouteParentStatus
// entry. Returning the full set lets the reconciler write per-ref
// status and aggregate hostname claims correctly across all
// attachment points instead of arbitrarily picking the first.
func allAttachingParentRefs(refs []gatewayv1.ParentReference, routeNs string, tgw *gatewayv1alpha1.TenantGateway) []gatewayv1.ParentReference {
	var out []gatewayv1.ParentReference
	for _, ref := range refs {
		if parentRefAttachesTo(ref, routeNs, tgw) {
			out = append(out, ref)
		}
	}
	return out
}

func parentRefAttachesTo(ref gatewayv1.ParentReference, routeNs string, tgw *gatewayv1alpha1.TenantGateway) bool {
	group := ""
	if ref.Group != nil {
		group = string(*ref.Group)
	}
	kind := "Gateway"
	if ref.Kind != nil {
		kind = string(*ref.Kind)
	}
	ns := routeNs
	if ref.Namespace != nil {
		ns = string(*ref.Namespace)
	}
	return (group == gatewayv1.GroupName || group == "") &&
		kind == "Gateway" &&
		ns == tgw.Namespace &&
		string(ref.Name) == tgw.Name
}

// reconcilePerListenerCertificates creates a Certificate for each
// dynamic hostname (HTTP-01 mode only) and deletes Certificates owned
// by this TenantGateway that no longer correspond to a live HTTPRoute
// hostname OR were left behind by a switch from HTTP-01 to DNS-01
// mode. The garbage-collect loop runs unconditionally so per-listener
// certs do not leak across mode transitions.
func (r *Reconciler) reconcilePerListenerCertificates(ctx context.Context, tgw *gatewayv1alpha1.TenantGateway, hostnames []string) error {
	logger := log.FromContext(ctx)

	desiredNames := map[string]struct{}{}
	// Provision per-listener certs only in HTTP-01 mode (wildcard
	// cert in DNS-01 mode covers everything). DNS-01 mode falls
	// through to the GC loop below with an empty desired set, which
	// then deletes any stale per-listener certs from a previous
	// HTTP-01 reconcile.
	if tgw.Spec.CertMode == gatewayv1alpha1.CertModeHTTP01 || tgw.Spec.CertMode == "" {
		for _, h := range hostnames {
			desired, err := r.renderPerListenerCertificate(tgw, h)
			if err != nil {
				return fmt.Errorf("render per-listener Certificate for %s: %w", h, err)
			}
			desiredNames[desired.Name] = struct{}{}

			existing := &cmv1.Certificate{}
			getErr := r.Get(ctx, types.NamespacedName{Namespace: tgw.Namespace, Name: desired.Name}, existing)
			switch {
			case apierrors.IsNotFound(getErr):
				if err := r.Create(ctx, desired); err != nil {
					return fmt.Errorf("create per-listener Certificate %s: %w", desired.Name, err)
				}
				logger.V(1).Info("created per-listener Certificate", "namespace", tgw.Namespace, "name", desired.Name)
			case getErr != nil:
				return fmt.Errorf("get per-listener Certificate %s: %w", desired.Name, getErr)
			default:
				// Same takeover-guard contract as elsewhere in
				// this file: an operator-pinned Certificate whose
				// name happens to collide with our derived
				// per-listener cert name must not be silently
				// rewritten and re-issued. The garbage-collect
				// loop below already gates Delete on ownership;
				// the create-or-update path here would otherwise
				// be the only asymmetric case.
				if !ownedByTenantGateway(existing.OwnerReferences, tgw) {
					return fmt.Errorf("certificate %s/%s exists but is not owned by TenantGateway %s; refusing to take over (delete it manually if you want the controller to manage this per-listener Certificate)", tgw.Namespace, desired.Name, tgw.Name)
				}
				if equality.Semantic.DeepEqual(existing.Spec, desired.Spec) {
					continue
				}
				existing.Spec = desired.Spec
				if err := r.Update(ctx, existing); err != nil {
					return fmt.Errorf("update per-listener Certificate %s: %w", desired.Name, err)
				}
			}
		}
	}

	// Garbage-collect: delete owned Certificates whose name no longer
	// matches a desired per-listener cert. Runs in both HTTP-01 and
	// DNS-01 modes — the empty desiredNames set in DNS-01 mode means
	// every per-listener cert from a previous HTTP-01 phase is
	// reclaimed.
	owned := &cmv1.CertificateList{}
	if err := r.List(ctx, owned, client.InNamespace(tgw.Namespace), client.MatchingLabels{cozystackManagedByLabel: cozystackManagedByValue}); err != nil {
		return fmt.Errorf("list owned Certificates: %w", err)
	}
	for i := range owned.Items {
		c := &owned.Items[i]
		if c.Name == gatewayCertificateName(tgw) {
			// Wildcard cert lifecycle is owned by
			// reconcileWildcardCertificate (which now also handles
			// the DNS-01→HTTP-01 transition cleanup).
			continue
		}
		if _, keep := desiredNames[c.Name]; keep {
			continue
		}
		if !ownedByTenantGateway(c.OwnerReferences, tgw) {
			continue
		}
		if err := r.Delete(ctx, c); err != nil && !apierrors.IsNotFound(err) {
			return fmt.Errorf("delete orphan Certificate %s: %w", c.Name, err)
		}
		logger.V(1).Info("deleted orphan Certificate", "namespace", tgw.Namespace, "name", c.Name)
	}
	return nil
}

func ownedByTenantGateway(refs []metav1.OwnerReference, tgw *gatewayv1alpha1.TenantGateway) bool {
	for _, ref := range refs {
		if ref.UID == tgw.UID && ref.Controller != nil && *ref.Controller {
			return true
		}
	}
	return false
}

func (r *Reconciler) reconcileGateway(ctx context.Context, tgw *gatewayv1alpha1.TenantGateway, dynHostnames []string) error {
	logger := log.FromContext(ctx)
	desired, err := r.renderGateway(tgw, dynHostnames)
	if err != nil {
		return fmt.Errorf("render Gateway: %w", err)
	}

	existing := &gatewayv1.Gateway{}
	getErr := r.Get(ctx, types.NamespacedName{Namespace: tgw.Namespace, Name: tgw.Name}, existing)
	switch {
	case apierrors.IsNotFound(getErr):
		if err := r.Create(ctx, desired); err != nil {
			return fmt.Errorf("create Gateway: %w", err)
		}
		logger.V(1).Info("created Gateway", "namespace", tgw.Namespace, "name", tgw.Name)
	case getErr != nil:
		return fmt.Errorf("get Gateway: %w", getErr)
	default:
		// Refuse to silently take over a Gateway that shares our
		// derived name but is not owned by this TenantGateway. An
		// operator (or another controller) may have created a
		// Gateway in this namespace with the same name; absent
		// this guard we'd overwrite its spec on first reconcile
		// and never establish the OwnerReference, leaving an
		// orphan that doesn't cascade-delete with the
		// TenantGateway.
		if !ownedByTenantGateway(existing.OwnerReferences, tgw) {
			return fmt.Errorf("gateway %s/%s exists but is not owned by TenantGateway %s; refusing to take over (delete it manually if you want the controller to manage this Gateway)", tgw.Namespace, tgw.Name, tgw.Name)
		}
		// Merge labels: keep keys other actors (Cilium operator,
		// kubectl label, future controllers) wrote, only add /
		// overwrite the keys this controller owns. Wholesale
		// replacement would clobber a Gateway's accumulated label
		// set on every reconcile.
		mergedLabels := mergeLabels(existing.Labels, desired.Labels)
		// Idempotency guard: skip Update when nothing changed.
		// Without this, every reconcile bumps ResourceVersion,
		// the Owns(Gateway) watch fires, the parent re-enqueues,
		// and the controller hot-loops indefinitely.
		if equality.Semantic.DeepEqual(existing.Spec, desired.Spec) &&
			labelsEqual(existing.Labels, mergedLabels) {
			return nil
		}
		existing.Spec = desired.Spec
		existing.Labels = mergedLabels
		if err := r.Update(ctx, existing); err != nil {
			return fmt.Errorf("update Gateway: %w", err)
		}
		logger.V(1).Info("updated Gateway", "namespace", tgw.Namespace, "name", tgw.Name)
	}
	return nil
}

// mergeLabels overlays controller-owned labels onto the existing set,
// preserving foreign keys.
func mergeLabels(existing, desired map[string]string) map[string]string {
	out := make(map[string]string, len(existing)+len(desired))
	for k, v := range existing {
		out[k] = v
	}
	for k, v := range desired {
		out[k] = v
	}
	return out
}

func labelsEqual(a, b map[string]string) bool {
	if len(a) != len(b) {
		return false
	}
	for k, va := range a {
		if vb, ok := b[k]; !ok || va != vb {
			return false
		}
	}
	return true
}

func (r *Reconciler) reconcileIssuer(ctx context.Context, tgw *gatewayv1alpha1.TenantGateway) error {
	logger := log.FromContext(ctx)
	desired, err := r.renderIssuer(tgw)
	if err != nil {
		return fmt.Errorf("render Issuer: %w", err)
	}

	existing := &cmv1.Issuer{}
	getErr := r.Get(ctx, types.NamespacedName{Namespace: tgw.Namespace, Name: gatewayIssuerName(tgw)}, existing)
	switch {
	case apierrors.IsNotFound(getErr):
		if err := r.Create(ctx, desired); err != nil {
			return fmt.Errorf("create Issuer: %w", err)
		}
		logger.V(1).Info("created Issuer", "namespace", tgw.Namespace, "name", desired.Name)
	case getErr != nil:
		return fmt.Errorf("get Issuer: %w", getErr)
	default:
		// Same takeover-guard contract as reconcileGateway /
		// reconcileHTTPToHTTPSRedirect: refuse to mutate a
		// pre-existing Issuer that shares our derived name but
		// carries no OwnerReference back to this TenantGateway.
		// Without this, an operator-pinned Issuer (e.g. for a
		// private CA) gets silently re-issued from our ACME
		// account on the next reconcile.
		if !ownedByTenantGateway(existing.OwnerReferences, tgw) {
			return fmt.Errorf("issuer %s/%s exists but is not owned by TenantGateway %s; refusing to take over (delete it manually if you want the controller to manage this Issuer)", tgw.Namespace, desired.Name, tgw.Name)
		}
		if equality.Semantic.DeepEqual(existing.Spec, desired.Spec) {
			return nil
		}
		existing.Spec = desired.Spec
		if err := r.Update(ctx, existing); err != nil {
			return fmt.Errorf("update Issuer: %w", err)
		}
		logger.V(1).Info("updated Issuer", "namespace", tgw.Namespace, "name", desired.Name)
	}
	return nil
}

func (r *Reconciler) reconcileWildcardCertificate(ctx context.Context, tgw *gatewayv1alpha1.TenantGateway) error {
	logger := log.FromContext(ctx)

	if tgw.Spec.CertMode != gatewayv1alpha1.CertModeDNS01 {
		// HTTP-01 mode: wildcard cert must not exist. Delete any
		// stale wildcard cert left over from a previous DNS-01
		// reconcile so a mode toggle doesn't leak Certificates.
		stale := &cmv1.Certificate{}
		err := r.Get(ctx, types.NamespacedName{Namespace: tgw.Namespace, Name: gatewayCertificateName(tgw)}, stale)
		if apierrors.IsNotFound(err) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("get wildcard Certificate for cleanup: %w", err)
		}
		if !ownedByTenantGateway(stale.OwnerReferences, tgw) {
			return nil
		}
		if err := r.Delete(ctx, stale); err != nil && !apierrors.IsNotFound(err) {
			return fmt.Errorf("delete stale wildcard Certificate %s: %w", stale.Name, err)
		}
		logger.V(1).Info("deleted stale wildcard Certificate after switch to HTTP-01", "name", stale.Name)
		return nil
	}
	desired, err := r.renderWildcardCertificate(tgw)
	if err != nil {
		return fmt.Errorf("render Certificate: %w", err)
	}

	existing := &cmv1.Certificate{}
	getErr := r.Get(ctx, types.NamespacedName{Namespace: tgw.Namespace, Name: desired.Name}, existing)
	switch {
	case apierrors.IsNotFound(getErr):
		if err := r.Create(ctx, desired); err != nil {
			return fmt.Errorf("create Certificate: %w", err)
		}
		logger.V(1).Info("created Certificate", "namespace", tgw.Namespace, "name", desired.Name)
	case getErr != nil:
		return fmt.Errorf("get Certificate: %w", getErr)
	default:
		// Same takeover-guard as reconcileIssuer: refuse to mutate
		// a pre-existing wildcard Certificate that shares our
		// derived name but is not owned. Operator-pinned certs
		// (e.g. wildcards from an internal CA) would otherwise get
		// silently re-issued from our Issuer on the next reconcile.
		if !ownedByTenantGateway(existing.OwnerReferences, tgw) {
			return fmt.Errorf("certificate %s/%s exists but is not owned by TenantGateway %s; refusing to take over (delete it manually if you want the controller to manage this Certificate)", tgw.Namespace, desired.Name, tgw.Name)
		}
		if equality.Semantic.DeepEqual(existing.Spec, desired.Spec) {
			return nil
		}
		existing.Spec = desired.Spec
		if err := r.Update(ctx, existing); err != nil {
			return fmt.Errorf("update Certificate: %w", err)
		}
		logger.V(1).Info("updated Certificate", "namespace", tgw.Namespace, "name", desired.Name)
	}
	return nil
}

// renderGateway builds the Gateway resource that should exist for the
// given TenantGateway. The result is owned by the TenantGateway via
// controllerutil.SetControllerReference so cascade delete works.
//
// dynHostnames is the deduplicated list of hostnames pulled from
// HTTPRoutes / TLSRoutes attached to this Gateway. In HTTP-01 mode
// each becomes an HTTPS listener with its own per-listener cert. In
// DNS-01 mode dynHostnames is expected to be empty (collector returns
// nothing) — the wildcard listener handles all subdomains.
//
// Every listener is gated by an unspoofable namespace selector
// (kubernetes.io/metadata.name In [...]) so only the publishing
// tenant namespace plus the TenantGateway.Spec.AttachedNamespaces
// list (cozy-* platform namespaces) can attach routes. This is
// Layer 1 of the security model documented in
// packages/extra/gateway/README.md.
func (r *Reconciler) renderGateway(tgw *gatewayv1alpha1.TenantGateway, dynHostnames []string) (*gatewayv1.Gateway, error) {
	allowedRoutes := buildAllowedRoutes(tgw)
	httpAllowedRoutes := buildHTTPListenerAllowedRoutes(tgw)
	listeners := []gatewayv1.Listener{
		{
			Name:          "http",
			Port:          80,
			Protocol:      gatewayv1.HTTPProtocolType,
			AllowedRoutes: httpAllowedRoutes,
		},
	}

	// HTTPS listeners restrict route attachment to HTTPRoute only —
	// Layer 7 (cozystack-route-hostname-policy VAP) currently gates
	// HTTPRoute and TLSRoute. With Gateway API v1.5.1 experimental
	// CRDs in scope, GRPCRoute / TCPRoute / UDPRoute could otherwise
	// attach by hostname and bypass the apex hostname check.
	httpsAllowedRoutes := allowedRoutes.DeepCopy()
	httpsAllowedRoutes.Kinds = []gatewayv1.RouteGroupKind{
		{Group: ptrGroup(gatewayv1.GroupName), Kind: "HTTPRoute"},
	}

	if tgw.Spec.CertMode == gatewayv1alpha1.CertModeDNS01 {
		certName := gatewayCertificateName(tgw)
		wildcardHost := gatewayv1.Hostname("*." + tgw.Spec.Apex)
		apexHost := gatewayv1.Hostname(tgw.Spec.Apex)
		listeners = append(listeners,
			gatewayv1.Listener{
				Name:     "https",
				Port:     443,
				Protocol: gatewayv1.HTTPSProtocolType,
				Hostname: &wildcardHost,
				TLS: &gatewayv1.ListenerTLSConfig{
					Mode: ptrTLSMode(gatewayv1.TLSModeTerminate),
					CertificateRefs: []gatewayv1.SecretObjectReference{
						{Name: gatewayv1.ObjectName(certName)},
					},
				},
				AllowedRoutes: httpsAllowedRoutes,
			},
			gatewayv1.Listener{
				Name:     "https-apex",
				Port:     443,
				Protocol: gatewayv1.HTTPSProtocolType,
				Hostname: &apexHost,
				TLS: &gatewayv1.ListenerTLSConfig{
					Mode: ptrTLSMode(gatewayv1.TLSModeTerminate),
					CertificateRefs: []gatewayv1.SecretObjectReference{
						{Name: gatewayv1.ObjectName(certName)},
					},
				},
				AllowedRoutes: httpsAllowedRoutes,
			},
		)
	} else {
		// HTTP-01 (default): per-app HTTPS listener per attached
		// HTTPRoute / TLSRoute hostname. Names + cert refs are
		// derived from the hostname's first label.
		for _, h := range dynHostnames {
			hostnameVal := gatewayv1.Hostname(h)
			listenerName := perListenerName(h)
			certName := perListenerCertName(tgw, h)
			listeners = append(listeners, gatewayv1.Listener{
				Name:     gatewayv1.SectionName(listenerName),
				Port:     443,
				Protocol: gatewayv1.HTTPSProtocolType,
				Hostname: &hostnameVal,
				TLS: &gatewayv1.ListenerTLSConfig{
					Mode: ptrTLSMode(gatewayv1.TLSModeTerminate),
					CertificateRefs: []gatewayv1.SecretObjectReference{
						{Name: gatewayv1.ObjectName(certName)},
					},
				},
				AllowedRoutes: httpsAllowedRoutes.DeepCopy(),
			})
		}
	}

	// TLS-passthrough listeners. One per service in
	// Spec.TLSPassthroughServices, named "tls-<service>", hostname
	// "<service>.<apex>", port 443, mode Passthrough. AllowedRoutes
	// restrict the kinds to TLSRoute (HTTPRoute makes no sense on a
	// Passthrough listener). The corresponding TLSRoute templates
	// (cozystack-api, vm-exportproxy, cdi-uploadproxy) attach to
	// these listeners by sectionName.
	for _, svc := range tgw.Spec.TLSPassthroughServices {
		host := gatewayv1.Hostname(svc + "." + tgw.Spec.Apex)
		passthroughAllowed := *allowedRoutes
		passthroughAllowed.Kinds = []gatewayv1.RouteGroupKind{
			{
				Group: ptrGroup(gatewayv1.GroupName),
				Kind:  "TLSRoute",
			},
		}
		listeners = append(listeners, gatewayv1.Listener{
			Name:     gatewayv1.SectionName("tls-" + svc),
			Port:     443,
			Protocol: gatewayv1.TLSProtocolType,
			Hostname: &host,
			TLS: &gatewayv1.ListenerTLSConfig{
				Mode: ptrTLSMode(gatewayv1.TLSModePassthrough),
			},
			AllowedRoutes: &passthroughAllowed,
		})
	}

	className := tgw.Spec.GatewayClassName
	if className == "" {
		className = "cilium"
	}

	gw := &gatewayv1.Gateway{
		ObjectMeta: metav1.ObjectMeta{
			Name:      tgw.Name,
			Namespace: tgw.Namespace,
			Labels: map[string]string{
				"cozystack.io/gateway":  tgw.Namespace,
				cozystackManagedByLabel: cozystackManagedByValue,
			},
		},
		Spec: gatewayv1.GatewaySpec{
			GatewayClassName: gatewayv1.ObjectName(className),
			Listeners:        listeners,
		},
	}
	if err := controllerutil.SetControllerReference(tgw, gw, r.Scheme); err != nil {
		return nil, err
	}
	return gw, nil
}

func ptrTLSMode(m gatewayv1.TLSModeType) *gatewayv1.TLSModeType {
	return &m
}

// SetupWithManager wires the Reconciler into the controller manager
// with For (TenantGateway as primary), Owns (Gateway and Certificate
// as owned children), and Watches against HTTPRoute and TLSRoute so
// route additions in attached namespaces re-trigger reconciliation
// of the parent TenantGateway.
func (r *Reconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		Named("tenantgateway-controller").
		For(&gatewayv1alpha1.TenantGateway{}).
		Owns(&gatewayv1.Gateway{}).
		Owns(&gatewayv1.HTTPRoute{}).
		Owns(&cmv1.Certificate{}).
		Owns(&cmv1.Issuer{}).
		Watches(
			&gatewayv1.HTTPRoute{},
			r.routeToTenantGateway(),
		).
		Watches(
			&gatewayv1alpha2.TLSRoute{},
			r.routeToTenantGateway(),
		).
		Complete(r)
}
