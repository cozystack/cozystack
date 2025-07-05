package controller

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
	"time"

	helmv2 "github.com/fluxcd/helm-controller/api/v2"
	corev1 "k8s.io/api/core/v1"
	kerrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
)

type CozystackConfigReconciler struct {
	client.Client
	Scheme *runtime.Scheme
}

var configMapNames = []string{"cozystack", "cozystack-branding", "cozystack-scheduling"}

const configMapNamespace = "cozy-system"
const digestAnnotation = "cozystack.io/cozy-config-digest"
const forceReconcileKey = "reconcile.fluxcd.io/forceAt"
const requestedAt = "reconcile.fluxcd.io/requestedAt"

func (r *CozystackConfigReconciler) Reconcile(ctx context.Context, _ ctrl.Request) (ctrl.Result, error) {
	log := log.FromContext(ctx)

	digest, err := r.computeDigest(ctx)
	if err != nil {
		log.Error(err, "failed to compute config digest")
		return ctrl.Result{}, nil
	}

	var helmList helmv2.HelmReleaseList
	if err := r.List(ctx, &helmList); err != nil {
		return ctrl.Result{}, fmt.Errorf("failed to list HelmReleases: %w", err)
	}

	now := time.Now().Format(time.RFC3339Nano)
	updated := 0

	for _, hr := range helmList.Items {
		isSystemApp := hr.Labels["cozystack.io/system-app"] == "true"
		isTenantRoot := hr.Namespace == "tenant-root" && hr.Name == "tenant-root"
		if !isSystemApp && !isTenantRoot {
			continue
		}

		if hr.Annotations == nil {
			hr.Annotations = map[string]string{}
		}

		if hr.Annotations[digestAnnotation] == digest {
			continue
		}

		patch := client.MergeFrom(hr.DeepCopy())
		hr.Annotations[digestAnnotation] = digest
		hr.Annotations[forceReconcileKey] = now
		hr.Annotations[requestedAt] = now

		if err := r.Patch(ctx, &hr, patch); err != nil {
			log.Error(err, "failed to patch HelmRelease", "name", hr.Name, "namespace", hr.Namespace)
			continue
		}
		updated++
		log.Info("patched HelmRelease with new config digest", "name", hr.Name, "namespace", hr.Namespace)
	}

	log.Info("finished reconciliation", "updatedHelmReleases", updated)

	// Check if oidc-enabled has changed from true to false
	oidcDisabled, err := r.checkOIDCDisabledTransition(ctx)
	if err != nil {
		log.Error(err, "failed to check OIDC status")
	}

	if oidcDisabled {
		type crdToDelete struct {
			kind      string
			name      string
			namespace string
			gvk       schema.GroupVersionKind
		}

		crds := []crdToDelete{
			{
				kind:      "ClusterKeycloak",
				name:      "keycloak-cozy",
				namespace: "cozy-keycloak",
				gvk:       schema.GroupVersionKind{Group: "v1.edp.epam.com", Version: "v1alpha1", Kind: "ClusterKeycloak"},
			},
			{
				kind:      "ClusterKeycloakRealm",
				name:      "keycloakrealm-cozy",
				namespace: "cozy-keycloak",
				gvk:       schema.GroupVersionKind{Group: "v1.edp.epam.com", Version: "v1alpha1", Kind: "ClusterKeycloakRealm"},
			},
			{
				kind:      "KeycloakClient",
				name:      "keycloakclient",
				namespace: "cozy-keycloak",
				gvk:       schema.GroupVersionKind{Group: "v1.edp.epam.com", Version: "v1", Kind: "KeycloakClient"},
			},
			{
				kind:      "KeycloakClient",
				name:      "kubeapps-client",
				namespace: "cozy-keycloak",
				gvk:       schema.GroupVersionKind{Group: "v1.edp.epam.com", Version: "v1", Kind: "KeycloakClient"},
			},
			{
				kind:      "KeycloakClientScope",
				name:      "keycloakclientscope-cozy",
				namespace: "cozy-keycloak",
				gvk:       schema.GroupVersionKind{Group: "v1.edp.epam.com", Version: "v1", Kind: "KeycloakClientScope"},
			},
			{
				kind:      "KeycloakClientScope",
				name:      "kubernetes-client",
				namespace: "cozy-keycloak",
				gvk:       schema.GroupVersionKind{Group: "v1.edp.epam.com", Version: "v1", Kind: "KeycloakClientScope"},
			},
			{
				kind:      "KeycloakRealmGroup",
				name:      "cozystack-cluster-admin",
				namespace: "cozy-system",
				gvk:       schema.GroupVersionKind{Group: "v1.edp.epam.com", Version: "v1", Kind: "KeycloakRealmGroup"},
			},
		}

		for _, crd := range crds {
			u := &unstructured.Unstructured{}
			u.SetGroupVersionKind(crd.gvk)
			u.SetName(crd.name)
			u.SetNamespace(crd.namespace)

			if err := r.Get(ctx, client.ObjectKeyFromObject(u), u); err != nil {
				if kerrors.IsNotFound(err) {
					continue
				}
				log.Error(err, "failed to get "+crd.kind, "name", crd.name)
				continue
			}

			if finalizers := u.GetFinalizers(); len(finalizers) > 0 {
				u.SetFinalizers(nil)
				if err := r.Update(ctx, u); err != nil {
					log.Error(err, "failed to remove finalizers from "+crd.kind, "name", crd.name)
					continue
				}
				log.Info("removed finalizers from "+crd.kind, "name", crd.name)
			}

			if err := r.Delete(ctx, u); err != nil && !kerrors.IsNotFound(err) {
				log.Error(err, "failed to delete "+crd.kind, "name", crd.name)
				continue
			}
			log.Info("deleted "+crd.kind, "name", crd.name)
		}

		for _, name := range []string{"keycloak-configure", "keycloak-operator", "keycloak"} {
			hr := &helmv2.HelmRelease{}
			key := client.ObjectKey{Name: name, Namespace: "cozy-keycloak"}

			err := r.Get(ctx, key, hr)
			if err != nil {
				if kerrors.IsNotFound(err) {
					log.Info("HelmRelease already deleted", "name", name)
					continue
				}
				log.Error(err, "failed to get HelmRelease", "name", name)
				return ctrl.Result{}, err
			}

			if err := r.Delete(ctx, hr); err != nil {
				log.Error(err, "failed to delete HelmRelease", "name", name)
				return ctrl.Result{}, err
			}
			log.Info("deletion requested for HelmRelease", "name", name)

			timeout := time.After(30 * time.Second)
			tick := time.Tick(1 * time.Second)
		WAIT_LOOP:
			for {
				select {
				case <-timeout:
					log.Error(fmt.Errorf("timeout"), "waiting for HelmRelease to be deleted", "name", name)
					return ctrl.Result{RequeueAfter: 10 * time.Second}, nil
				case <-tick:
					err := r.Get(ctx, key, &helmv2.HelmRelease{})
					if kerrors.IsNotFound(err) {
						log.Info("HelmRelease deletion confirmed", "name", name)
						break WAIT_LOOP
					} else if err != nil {
						log.Error(err, "error checking HelmRelease deletion", "name", name)
						return ctrl.Result{}, err
					}
				}
			}
		}
		ns := &corev1.Namespace{}
		nsKey := client.ObjectKey{Name: "cozy-keycloak"}

		if err := r.Get(ctx, nsKey, ns); err != nil {
			if kerrors.IsNotFound(err) {
				log.Info("Namespace cozy-keycloak already deleted")
			} else {
				log.Error(err, "failed to get namespace cozy-keycloak")
				return ctrl.Result{}, err
			}
		} else {
			if ns.DeletionTimestamp == nil {
				if err := r.Delete(ctx, ns); err != nil {
					log.Error(err, "failed to delete namespace cozy-keycloak")
					return ctrl.Result{}, err
				}
				log.Info("deletion requested for namespace cozy-keycloak")
			} else {
				log.Info("namespace cozy-keycloak is already being deleted")
			}
		}
	}
	return ctrl.Result{}, nil
}

func (r *CozystackConfigReconciler) computeDigest(ctx context.Context) (string, error) {
	hash := sha256.New()

	for _, name := range configMapNames {
		var cm corev1.ConfigMap
		err := r.Get(ctx, client.ObjectKey{Namespace: configMapNamespace, Name: name}, &cm)
		if err != nil {
			if kerrors.IsNotFound(err) {
				continue
			}
			return "", err
		}

		var keys []string
		for k := range cm.Data {
			keys = append(keys, k)
		}
		sort.Strings(keys)

		for _, k := range keys {
			v := cm.Data[k]
			fmt.Fprintf(hash, "%s:%s=%s\n", name, k, v)
		}
	}

	return hex.EncodeToString(hash.Sum(nil)), nil
}

func (r *CozystackConfigReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		WithEventFilter(predicate.Funcs{
			UpdateFunc: func(e event.UpdateEvent) bool {
				cm, ok := e.ObjectNew.(*corev1.ConfigMap)
				return ok && cm.Namespace == configMapNamespace && contains(configMapNames, cm.Name)
			},
			CreateFunc: func(e event.CreateEvent) bool {
				cm, ok := e.Object.(*corev1.ConfigMap)
				return ok && cm.Namespace == configMapNamespace && contains(configMapNames, cm.Name)
			},
			DeleteFunc: func(e event.DeleteEvent) bool {
				cm, ok := e.Object.(*corev1.ConfigMap)
				return ok && cm.Namespace == configMapNamespace && contains(configMapNames, cm.Name)
			},
		}).
		For(&corev1.ConfigMap{}).
		Complete(r)
}

func contains(slice []string, val string) bool {
	for _, s := range slice {
		if s == val {
			return true
		}
	}
	return false
}

func (r *CozystackConfigReconciler) checkOIDCDisabledTransition(ctx context.Context) (bool, error) {
	const configName = "cozystack"
	const fieldKey = "oidc-enabled"
	const lastOIDCStateAnnotation = "cozystack.io/last-oidc-enabled"

	var cm corev1.ConfigMap
	if err := r.Get(ctx, client.ObjectKey{Namespace: configMapNamespace, Name: configName}, &cm); err != nil {
		return false, err
	}

	current := cm.Data[fieldKey]
	if current != "false" && current != "true" {
		return false, nil
	}

	last := cm.Annotations[lastOIDCStateAnnotation]

	if cm.Annotations == nil {
		cm.Annotations = map[string]string{}
	}
	if last != current {
		patch := client.MergeFrom(cm.DeepCopy())
		cm.Annotations[lastOIDCStateAnnotation] = current
		if err := r.Patch(ctx, &cm, patch); err != nil {
			return false, fmt.Errorf("failed to update oidc-enabled annotation: %w", err)
		}
	}

	return last == "true" && current == "false", nil
}
