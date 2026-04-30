/*
Copyright 2025 The Cozystack Authors.

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

	cmv1 "github.com/cert-manager/cert-manager/pkg/apis/certmanager/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
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
	logger := log.FromContext(ctx)

	tgw := &gatewayv1alpha1.TenantGateway{}
	if err := r.Get(ctx, req.NamespacedName, tgw); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	if err := r.reconcileGateway(ctx, tgw); err != nil {
		return ctrl.Result{}, err
	}
	if err := r.reconcileIssuer(ctx, tgw); err != nil {
		return ctrl.Result{}, err
	}
	if err := r.reconcileWildcardCertificate(ctx, tgw); err != nil {
		return ctrl.Result{}, err
	}

	_ = logger
	return ctrl.Result{}, nil
}

func (r *Reconciler) reconcileGateway(ctx context.Context, tgw *gatewayv1alpha1.TenantGateway) error {
	logger := log.FromContext(ctx)
	desired, err := r.renderGateway(tgw)
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
		existing.Labels = desired.Labels
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
func (r *Reconciler) renderGateway(tgw *gatewayv1alpha1.TenantGateway) (*gatewayv1.Gateway, error) {
	listeners := []gatewayv1.Listener{
		{
			Name:     "http",
			Port:     80,
			Protocol: gatewayv1.HTTPProtocolType,
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
			},
		)
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
			routeToTenantGateway(),
		).
		Watches(
			&gatewayv1alpha2.TLSRoute{},
			routeToTenantGateway(),
		).
		Complete(r)
}

