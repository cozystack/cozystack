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
	if err := r.updateRouteStatuses(ctx, tgw, winners, losers); err != nil {
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
		matchingRef, ok := pickAttachingParentRef(route.Spec.ParentRefs, route.Namespace, tgw)
		if !ok {
			continue
		}
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

	tlsRoutes := &gatewayv1alpha2.TLSRouteList{}
	if err := r.List(ctx, tlsRoutes); err != nil {
		return nil, fmt.Errorf("list TLSRoutes: %w", err)
	}
	for i := range tlsRoutes.Items {
		route := &tlsRoutes.Items[i]
		if _, ok := allowed[route.Namespace]; !ok {
			continue
		}
		matchingRef, ok := pickAttachingParentRef(route.Spec.ParentRefs, route.Namespace, tgw)
		if !ok {
			continue
		}
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
	return out, nil
}

// pickAttachingParentRef returns the first ParentRef in refs that
// attaches to tgw's Gateway, plus a boolean ok. Used both to decide
// whether to read hostnames from the route and to record the exact
// ref that gets stamped back into RouteParentStatus.
func pickAttachingParentRef(refs []gatewayv1.ParentReference, routeNs string, tgw *gatewayv1alpha1.TenantGateway) (gatewayv1.ParentReference, bool) {
	for _, ref := range refs {
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
		if (group == gatewayv1.GroupName || group == "") &&
			kind == "Gateway" &&
			ns == tgw.Namespace &&
			string(ref.Name) == tgw.Name {
			return ref, true
		}
	}
	return gatewayv1.ParentReference{}, false
}

// reconcilePerListenerCertificates creates a Certificate for each
// dynamic hostname (HTTP-01 mode only) and deletes Certificates owned
// by this TenantGateway that no longer correspond to a live HTTPRoute
// hostname.
func (r *Reconciler) reconcilePerListenerCertificates(ctx context.Context, tgw *gatewayv1alpha1.TenantGateway, hostnames []string) error {
	if tgw.Spec.CertMode != gatewayv1alpha1.CertModeHTTP01 && tgw.Spec.CertMode != "" {
		return nil
	}
	logger := log.FromContext(ctx)

	desiredNames := map[string]struct{}{}
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
			existing.Spec = desired.Spec
			if err := r.Update(ctx, existing); err != nil {
				return fmt.Errorf("update per-listener Certificate %s: %w", desired.Name, err)
			}
		}
	}

	// Garbage-collect Certificates this controller previously created
	// for hostnames that no longer have an attached route.
	owned := &cmv1.CertificateList{}
	if err := r.List(ctx, owned, client.InNamespace(tgw.Namespace), client.MatchingLabels{cozystackManagedByLabel: cozystackManagedByValue}); err != nil {
		return fmt.Errorf("list owned Certificates: %w", err)
	}
	for i := range owned.Items {
		c := &owned.Items[i]
		if c.Name == gatewayCertificateName(tgw) {
			// Wildcard cert is handled by reconcileWildcardCertificate.
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
		existing.Spec = desired.Spec
		// Merge labels: keep keys other actors (Cilium operator,
		// kubectl label, future controllers) wrote, only add /
		// overwrite the keys this controller owns. Wholesale
		// replacement would clobber a Gateway's accumulated label
		// set on every reconcile.
		if existing.Labels == nil {
			existing.Labels = make(map[string]string, len(desired.Labels))
		}
		for k, v := range desired.Labels {
			existing.Labels[k] = v
		}
		if err := r.Update(ctx, existing); err != nil {
			return fmt.Errorf("update Gateway: %w", err)
		}
		logger.V(1).Info("updated Gateway", "namespace", tgw.Namespace, "name", tgw.Name)
	}
	return nil
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
		existing.Spec = desired.Spec
		if err := r.Update(ctx, existing); err != nil {
			return fmt.Errorf("update Issuer: %w", err)
		}
		logger.V(1).Info("updated Issuer", "namespace", tgw.Namespace, "name", desired.Name)
	}
	return nil
}

func (r *Reconciler) reconcileWildcardCertificate(ctx context.Context, tgw *gatewayv1alpha1.TenantGateway) error {
	if tgw.Spec.CertMode != gatewayv1alpha1.CertModeDNS01 {
		// HTTP-01 mode does not use a wildcard cert; per-listener
		// certs are produced by route-driven reconciliation.
		return nil
	}
	logger := log.FromContext(ctx)
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
	listeners := []gatewayv1.Listener{
		{
			Name:          "http",
			Port:          80,
			Protocol:      gatewayv1.HTTPProtocolType,
			AllowedRoutes: allowedRoutes,
		},
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
				AllowedRoutes: allowedRoutes,
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
				AllowedRoutes: allowedRoutes,
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
				AllowedRoutes: allowedRoutes,
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
				"cozystack.io/gateway":     tgw.Namespace,
				"cozystack.io/managed-by":  "cozystack-controller",
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
		Owns(&cmv1.Certificate{}).
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

