package controller

import (
	"context"
	"fmt"
	"strings"
	"time"

	e "errors"

	helmv2 "github.com/fluxcd/helm-controller/api/v2"
	"gopkg.in/yaml.v2"
	corev1 "k8s.io/api/core/v1"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"
)

type TenantHelmReconciler struct {
	client.Client
	Scheme *runtime.Scheme
}

func (r *TenantHelmReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx)
	time.Sleep(2 * time.Second)

	hr := &helmv2.HelmRelease{}
	if err := r.Get(ctx, req.NamespacedName, hr); err != nil {
		if errors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		logger.Error(err, "unable to fetch HelmRelease")
		return ctrl.Result{}, err
	}

	if !strings.HasPrefix(hr.Name, "tenant-") {
		return ctrl.Result{}, nil
	}

	if len(hr.Status.Conditions) == 0 || hr.Status.Conditions[0].Type != "Ready" {
		return ctrl.Result{}, nil
	}

	if len(hr.Status.History) == 0 {
		logger.Info("no history in HelmRelease status", "name", hr.Name)
		return ctrl.Result{}, nil
	}

	if hr.Status.History[0].Status != "deployed" {
		return ctrl.Result{}, nil
	}

	newDigest := hr.Status.History[0].Digest
	var hrList helmv2.HelmReleaseList
	childNamespace := getChildNamespace(hr.Namespace, hr.Name)
	if childNamespace == "tenant-root" && hr.Name == "tenant-root" {
		if hr.Spec.Values == nil {
			logger.Error(e.New("hr.Spec.Values is nil"), "cant annotate tenant-root ns")
			return ctrl.Result{}, nil
		}
		err := annotateTenantRootNs(*hr.Spec.Values, r.Client)
		if err != nil {
			logger.Error(err, "cant annotate tenant-root ns")
			return ctrl.Result{}, nil
		}
		logger.Info("namespace 'tenant-root' annotated")
	}

	if err := r.List(ctx, &hrList, client.InNamespace(childNamespace)); err != nil {
		logger.Error(err, "unable to list HelmReleases in namespace", "namespace", hr.Name)
		return ctrl.Result{}, err
	}

	for _, item := range hrList.Items {
		if item.Name == hr.Name {
			continue
		}
		oldDigest := item.GetAnnotations()["cozystack.io/tenant-config-digest"]
		if oldDigest == newDigest {
			continue
		}
		patchTarget := item.DeepCopy()

		if patchTarget.Annotations == nil {
			patchTarget.Annotations = map[string]string{}
		}
		ts := time.Now().Format(time.RFC3339Nano)

		patchTarget.Annotations["cozystack.io/tenant-config-digest"] = newDigest
		patchTarget.Annotations["reconcile.fluxcd.io/forceAt"] = ts
		patchTarget.Annotations["reconcile.fluxcd.io/requestedAt"] = ts

		patch := client.MergeFrom(item.DeepCopy())
		if err := r.Patch(ctx, patchTarget, patch); err != nil {
			logger.Error(err, "failed to patch HelmRelease", "name", patchTarget.Name)
			continue
		}

		logger.Info("patched HelmRelease with new digest", "name", patchTarget.Name, "digest", newDigest, "version", hr.Status.History[0].Version)
	}

	return ctrl.Result{}, nil
}

func (r *TenantHelmReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&helmv2.HelmRelease{}).
		Complete(r)
}

func getChildNamespace(currentNamespace, hrName string) string {
	tenantName := strings.TrimPrefix(hrName, "tenant-")

	switch {
	case currentNamespace == "tenant-root" && hrName == "tenant-root":
		// 1) root tenant inside root namespace
		return "tenant-root"

	case currentNamespace == "tenant-root":
		// 2) any other tenant in root namespace
		return fmt.Sprintf("tenant-%s", tenantName)

	default:
		// 3) tenant in a dedicated namespace
		return fmt.Sprintf("%s-%s", currentNamespace, tenantName)
	}
}

func annotateTenantRootNs(values apiextensionsv1.JSON, c client.Client) error {
	var data map[string]interface{}
	if err := yaml.Unmarshal(values.Raw, &data); err != nil {
		return fmt.Errorf("failed to parse HelmRelease values: %w", err)
	}

	host, ok := data["host"].(string)
	if !ok || host == "" {
		return fmt.Errorf("host field not found or not a string")
	}

	var ns corev1.Namespace
	if err := c.Get(context.TODO(), client.ObjectKey{Name: "tenant-root"}, &ns); err != nil {
		return fmt.Errorf("failed to get namespace tenant-root: %w", err)
	}

	if ns.Annotations == nil {
		ns.Annotations = map[string]string{}
	}
	ns.Annotations["namespace.cozystack.io/host"] = host

	if err := c.Update(context.TODO(), &ns); err != nil {
		return fmt.Errorf("failed to update namespace: %w", err)
	}

	return nil
}
