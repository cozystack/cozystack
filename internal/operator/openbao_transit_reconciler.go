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

package operator

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/cozystack/cozystack/internal/operator/openbaotransit"
	helmv2 "github.com/fluxcd/helm-controller/api/v2"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
)

const (
	// TransitUnsealLabel marks the inner OpenBAO HelmRelease whose instance needs
	// a transit auto-unseal credential provisioned on the central OpenBAO.
	TransitUnsealLabel = "apps.cozystack.io/transit-unseal"

	// central OpenBAO coordinates (see packages/system/openbao).
	centralOpenBAONamespace  = "cozy-openbao"
	centralOpenBAOKeysSecret = "openbao-system-keys"
	centralOpenBAOCASecret   = "openbao-system-ca"
	centralOpenBAOAddr       = "https://openbao-system.cozy-openbao.svc:8200"

	transitTokenPeriod = "72h"
)

// OpenBAOTransitReconciler provisions, on the central OpenBAO, a per-tenant
// transit key + scoped policy + Kubernetes-auth role bound to the tenant's
// server ServiceAccount, and publishes the central CA as a ConfigMap in the
// tenant namespace. The tenant OpenBAO then obtains a transit-scoped token at
// startup via Kubernetes auth — no token is ever minted or stored as a Secret.
type OpenBAOTransitReconciler struct {
	client.Client
	Scheme *runtime.Scheme
}

//+kubebuilder:rbac:groups=helm.toolkit.fluxcd.io,resources=helmreleases,verbs=get;list;watch
//+kubebuilder:rbac:groups="",resources=secrets,verbs=get;list;watch
//+kubebuilder:rbac:groups="",resources=configmaps,verbs=get;list;watch;create;update;patch

// Reconcile ensures the transit credential exists for one inner OpenBAO HelmRelease.
func (r *OpenBAOTransitReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	hr := &helmv2.HelmRelease{}
	if err := r.Get(ctx, req.NamespacedName, hr); err != nil {
		// HR gone: the CA ConfigMap is owner-referenced to it and is
		// garbage-collected automatically. The orphaned central transit key /
		// policy / role are harmless and reclaimed out of band.
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	if hr.GetLabels()[TransitUnsealLabel] != "true" {
		return ctrl.Result{}, nil
	}

	// The inner HelmRelease is named "<instance>-system"; the instance (== the
	// OpenBAO CR / Helm release name) is what the chart uses for the seal key, the
	// server ServiceAccount, and the CA ConfigMap.
	instance := strings.TrimSuffix(hr.Name, "-system")
	if instance == hr.Name {
		return ctrl.Result{}, nil
	}
	ns := hr.Namespace
	keyName := fmt.Sprintf("%s-%s", ns, instance) // matches the chart's $sealKey and k8s-auth role
	policyName := keyName + "-unseal"
	saName := instance // the upstream chart's server ServiceAccount (fullnameOverride)
	caConfigMap := instance + "-transit-ca"

	// Authenticate to the central OpenBAO with its root token + CA. (The token
	// minting happens entirely on the central side via Kubernetes auth — no
	// token is ever delivered to or stored in the tenant namespace.)
	rootToken, err := r.readCentralSecretKey(ctx, centralOpenBAOKeysSecret, "root-token")
	if err != nil {
		logger.Info("central OpenBAO not ready yet (root token unavailable), requeueing", "error", err.Error())
		return ctrl.Result{RequeueAfter: 30 * time.Second}, nil
	}
	caPEM, err := r.readCentralSecretKey(ctx, centralOpenBAOCASecret, "ca.crt")
	if err != nil {
		logger.Info("central OpenBAO CA not ready yet, requeueing", "error", err.Error())
		return ctrl.Result{RequeueAfter: 30 * time.Second}, nil
	}

	bc, err := openbaotransit.New(centralOpenBAOAddr, string(rootToken), caPEM)
	if err != nil {
		return ctrl.Result{}, err
	}

	if err := bc.EnsureTransitKey(ctx, keyName); err != nil {
		logger.Info("transit key provisioning failed, requeueing", "key", keyName, "error", err.Error())
		return ctrl.Result{RequeueAfter: 30 * time.Second}, nil
	}
	if err := bc.WriteUnsealPolicy(ctx, policyName, keyName); err != nil {
		logger.Info("policy write failed, requeueing", "policy", policyName, "error", err.Error())
		return ctrl.Result{RequeueAfter: 30 * time.Second}, nil
	}
	// Bind the tenant's server ServiceAccount to the policy via Kubernetes auth.
	// The tenant pod logs in with its projected SA token at startup — nothing is
	// stored in the tenant namespace.
	if err := bc.EnsureKubernetesAuthRole(ctx, keyName, saName, ns, policyName, transitTokenPeriod); err != nil {
		logger.Info("kubernetes auth role write failed, requeueing", "role", keyName, "error", err.Error())
		return ctrl.Result{RequeueAfter: 30 * time.Second}, nil
	}

	// Publish the central CA as a ConfigMap (public cert, not a Secret) so the
	// tenant's login init container and transit seal can verify the central TLS.
	if err := r.ensureCAConfigMap(ctx, hr, caConfigMap, caPEM); err != nil {
		return ctrl.Result{}, err
	}

	logger.Info("provisioned transit auto-unseal (kubernetes auth)", "namespace", ns, "instance", instance, "key", keyName)
	return ctrl.Result{}, nil
}

func (r *OpenBAOTransitReconciler) readCentralSecretKey(ctx context.Context, name, key string) ([]byte, error) {
	s := &corev1.Secret{}
	if err := r.Get(ctx, types.NamespacedName{Namespace: centralOpenBAONamespace, Name: name}, s); err != nil {
		return nil, err
	}
	v, ok := s.Data[key]
	if !ok || len(v) == 0 {
		return nil, fmt.Errorf("secret %s/%s missing key %q", centralOpenBAONamespace, name, key)
	}
	return v, nil
}

func (r *OpenBAOTransitReconciler) ensureCAConfigMap(ctx context.Context, owner *helmv2.HelmRelease, name string, caPEM []byte) error {
	cm := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Namespace: owner.Namespace, Name: name},
	}
	_, err := controllerutil.CreateOrUpdate(ctx, r.Client, cm, func() error {
		if cm.Data == nil {
			cm.Data = map[string]string{}
		}
		cm.Data["ca.crt"] = string(caPEM)
		// GC the CA bundle when the instance's HelmRelease is removed.
		return controllerutil.SetOwnerReference(owner, cm, r.Scheme)
	})
	return err
}

// SetupWithManager wires the reconciler to watch only transit-labelled HelmReleases.
func (r *OpenBAOTransitReconciler) SetupWithManager(mgr ctrl.Manager) error {
	hasLabel := predicate.NewPredicateFuncs(func(o client.Object) bool {
		return o.GetLabels()[TransitUnsealLabel] == "true"
	})
	return ctrl.NewControllerManagedBy(mgr).
		For(&helmv2.HelmRelease{}, builder.WithPredicates(hasLabel)).
		Named("openbao-transit").
		Complete(r)
}
