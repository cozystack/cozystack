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
	"k8s.io/apimachinery/pkg/runtime"
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
	time.Sleep(2 * time.Second)

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
		patchTarget := hr.DeepCopy()

		if hr.Annotations == nil {
			hr.Annotations = map[string]string{}
		}

		if hr.Annotations[digestAnnotation] == digest {
			continue
		}
		patchTarget.Annotations[digestAnnotation] = digest
		patchTarget.Annotations[forceReconcileKey] = now
		patchTarget.Annotations[requestedAt] = now

		patch := client.MergeFrom(hr.DeepCopy())
		if err := r.Patch(ctx, patchTarget, patch); err != nil {
			log.Error(err, "failed to patch HelmRelease", "name", hr.Name, "namespace", hr.Namespace)
			continue
		}
		updated++
		log.Info("patched HelmRelease with new config digest", "name", hr.Name, "namespace", hr.Namespace)
	}

	log.Info("finished reconciliation", "updatedHelmReleases", updated)
	return ctrl.Result{}, nil
}

func (r *CozystackConfigReconciler) computeDigest(ctx context.Context) (string, error) {
	hash := sha256.New()

	for _, name := range configMapNames {
		var cm corev1.ConfigMap
		err := r.Get(ctx, client.ObjectKey{Namespace: configMapNamespace, Name: name}, &cm)
		if err != nil {
			if kerrors.IsNotFound(err) {
				continue // ignore missing
			}
			return "", err
		}

		// Sort keys for consistent hashing
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
