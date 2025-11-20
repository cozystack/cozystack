/*
Copyright 2024 The Cozystack Authors.

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

	cozyv1alpha1 "github.com/cozystack/cozystack/api/v1alpha1"
	helmv2 "github.com/fluxcd/helm-controller/api/v2"
	corev1 "k8s.io/api/core/v1"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	"github.com/cozystack/cozystack/pkg/config"
)

const (
	cozystackConfigMapName      = "cozystack"
	cozystackConfigMapNamespace = "cozy-system"
	platformOperatorLabel       = "cozystack.io/platform-operator"
)

var (
	// System packages list - packages that are in packages/system/ directory
	systemPackagesList = map[string]bool{
		"bootbox": true, "bucket": true, "capi-operator": true, "capi-providers-bootstrap": true,
		"capi-providers-core": true, "capi-providers-cpprovider": true, "capi-providers-infraprovider": true,
		"cert-manager": true, "cert-manager-crds": true, "cert-manager-issuers": true, "cilium": true,
		"cilium-networkpolicy": true, "clickhouse-operator": true, "coredns": true, "cozy-proxy": true,
		"cozystack-api": true, "cozystack-controller": true, "cozystack-resource-definition-crd": true,
		"cozystack-resource-definitions": true, "dashboard": true, "etcd-operator": true, "external-dns": true,
		"external-secrets-operator": true, "fluxcd": true, "fluxcd-operator": true, "foundationdb-operator": true,
		"gateway-api-crds": true, "goldpinger": true, "gpu-operator": true, "grafana-operator": true,
		"hetzner-robotlb": true, "ingress-nginx": true, "kafka-operator": true, "kamaji": true,
		"keycloak": true, "keycloak-configure": true, "keycloak-operator": true, "kubeovn": true,
		"kubeovn-plunger": true, "kubeovn-webhook": true, "kubevirt": true, "kubevirt-cdi": true,
		"kubevirt-cdi-operator": true, "kubevirt-csi-node": true, "kubevirt-instancetypes": true,
		"kubevirt-operator": true, "lineage-controller-webhook": true, "linstor": true, "mariadb-operator": true,
		"metallb": true, "monitoring-agents": true, "multus": true, "nats": true, "nfs-driver": true,
		"objectstorage-controller": true, "opencost": true, "piraeus-operator": true, "postgres-operator": true,
		"rabbitmq-operator": true, "redis-operator": true, "reloader": true, "seaweedfs": true,
		"snapshot-controller": true, "telepresence": true, "velero": true, "vertical-pod-autoscaler": true,
		"vertical-pod-autoscaler-crds": true, "victoria-metrics-operator": true, "vsnap-crd": true,
	}

	// Apps packages list - packages that are in packages/apps/ directory
	appsPackagesList = map[string]bool{
		"bucket": true, "clickhouse": true, "ferretdb": true, "foundationdb": true, "http-cache": true,
		"kafka": true, "kubernetes": true, "mysql": true, "nats": true, "postgres": true, "rabbitmq": true,
		"redis": true, "tcp-balancer": true, "tenant": true, "virtual-machine": true, "vm-disk": true,
		"vm-instance": true, "vpc": true, "vpn": true,
	}

	// Extra packages list - packages that are in packages/extra/ directory
	extraPackagesList = map[string]bool{
		"bootbox": true, "etcd": true, "info": true, "ingress": true, "monitoring": true, "seaweedfs": true,
	}
)

// getArtifactPrefixAndNamespace determines the artifact prefix and namespace based on package category
// Returns: prefix, namespace, found
func getArtifactPrefixAndNamespace(chartName string) (string, string, bool) {
	if systemPackagesList[chartName] {
		return "system", "cozy-system", true
	}
	if appsPackagesList[chartName] {
		return "apps", "cozy-public", true
	}
	if extraPackagesList[chartName] {
		return "extra", "cozy-public", true
	}
	return "", "", false
}

// PlatformReconciler reconciles the platform configuration.
type PlatformReconciler struct {
	client.Client
	Scheme             *runtime.Scheme
	FirstReconcileDone chan struct{}
}

// +kubebuilder:rbac:groups=core,resources=configmaps,verbs=get;list;watch
// +kubebuilder:rbac:groups=core,resources=configmaps/status,verbs=get
// +kubebuilder:rbac:groups=core,resources=namespaces,verbs=get;list;watch;create;update;patch
// +kubebuilder:rbac:groups=helm.toolkit.fluxcd.io,resources=helmreleases,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=cozystack.io,resources=cozystackbundles,verbs=get;list;watch

// Reconcile is part of the main kubernetes reconciliation loop which aims to
// move the current state of the cluster closer to the desired state.
func (r *PlatformReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	// Reconcile on changes to cozystack ConfigMap
	if req.Namespace != cozystackConfigMapNamespace {
		return ctrl.Result{}, nil
	}

	// Reconcile on changes to cozystack ConfigMap
	if req.Name != cozystackConfigMapName {
		return ctrl.Result{}, nil
	}

	// Get the cozystack ConfigMap
	configMap := &corev1.ConfigMap{}
	if err := r.Get(ctx, req.NamespacedName, configMap); err != nil {
		if apierrors.IsNotFound(err) {
			logger.Info("cozystack ConfigMap not found")
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}

	// Parse ConfigMap data
	cfg := config.ParseConfigMapData(configMap.Data)
	if cfg.BundleName == "" {
		logger.Info("bundle-name not set in cozystack ConfigMap")
		return ctrl.Result{}, nil
	}

	logger.Info("Reconciling platform", "bundle", cfg.BundleName)

	// Reconcile namespaces
	if err := r.reconcileNamespaces(ctx, cfg); err != nil {
		return ctrl.Result{}, fmt.Errorf("failed to reconcile namespaces: %w", err)
	}

	// Reconcile tenant-root
	if err := r.reconcileTenantRoot(ctx, cfg); err != nil {
		return ctrl.Result{}, fmt.Errorf("failed to reconcile tenant-root: %w", err)
	}

	// Signal that first reconcile is done (non-blocking)
	if r.FirstReconcileDone != nil {
		select {
		case <-r.FirstReconcileDone:
			// Already closed, do nothing
		default:
			// Close channel to signal first reconcile is done
			close(r.FirstReconcileDone)
		}
	}

	return ctrl.Result{}, nil
}

// SetupWithManager sets up the controller with the Manager.
func (r *PlatformReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		Named("platform-operator").
		For(&corev1.ConfigMap{}).
		Complete(r)
}

// Removed reconcileHelmRepositories, reconcileGitRepository, reconcileArtifactGenerators, and reconcileHelmReleases
// These are now handled by CozystackBundleReconciler and CozystackResourceDefinitionReconciler

func (r *PlatformReconciler) reconcileNamespaces(ctx context.Context, cfg *config.CozystackConfig) error {
	logger := log.FromContext(ctx)

	// Collect namespaces from CozystackBundle resources
	namespacesMap := make(map[string]bool)

	// List all CozystackBundle resources
	bundleList := &cozyv1alpha1.CozystackBundleList{}
	if err := r.Client.List(ctx, bundleList); err != nil {
		logger.Error(err, "failed to list CozystackBundles, using default namespaces")
	} else {
		for _, bundle := range bundleList.Items {
			for _, pkg := range bundle.Spec.Packages {
				// Check if package is disabled
				if pkg.Disabled {
					continue
				}

				// Check if component is disabled via config
				if cfg.IsComponentDisabled(pkg.Name) {
					continue
				}

				// If at least one package requires a privileged namespace, then it should be privileged
				if pkg.Namespace != "" {
					if pkg.Privileged {
						namespacesMap[pkg.Namespace] = true
					} else if _, exists := namespacesMap[pkg.Namespace]; !exists {
						namespacesMap[pkg.Namespace] = false
					}
				}
			}
		}
	}

	// Always add cozy-system (privileged) and cozy-public (non-privileged)
	namespacesMap["cozy-system"] = true
	namespacesMap["cozy-public"] = false

	// Create/update all desired namespaces
	for nsName, privileged := range namespacesMap {
		namespace := &corev1.Namespace{
			ObjectMeta: metav1.ObjectMeta{
				Name: nsName,
				Annotations: map[string]string{
					"helm.sh/resource-policy": "keep",
				},
				Labels: map[string]string{
					"cozystack.io/system": "true",
				},
			},
		}

		if privileged {
			namespace.Labels["pod-security.kubernetes.io/enforce"] = "privileged"
		}

		if err := r.CreateOrUpdate(ctx, namespace); err != nil {
			logger.Error(err, "failed to reconcile Namespace", "name", nsName)
			return err
		}
		logger.Info("reconciled Namespace", "name", nsName, "privileged", privileged)
	}

	// Note: We no longer delete namespaces that are not in the desired state.
	// Namespaces are managed by their respective HelmReleases and should not be
	// deleted by the platform operator.

	return nil
}

func (r *PlatformReconciler) reconcileTenantRoot(ctx context.Context, cfg *config.CozystackConfig) error {
	logger := log.FromContext(ctx)

	host := cfg.RootHost
	if host == "" {
		host = "example.org"
	}

	// Define desired tenant-root namespace
	desiredNamespace := &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{
			Name: "tenant-root",
			Annotations: map[string]string{
				"helm.sh/resource-policy":           "keep",
				"namespace.cozystack.io/etcd":       "tenant-root",
				"namespace.cozystack.io/monitoring": "tenant-root",
				"namespace.cozystack.io/ingress":    "tenant-root",
				"namespace.cozystack.io/seaweedfs":  "tenant-root",
				"namespace.cozystack.io/host":       host,
			},
		},
	}

	if err := r.CreateOrUpdate(ctx, desiredNamespace); err != nil {
		logger.Error(err, "failed to reconcile tenant-root Namespace")
		return err
	}
	logger.Info("reconciled tenant-root Namespace")

	// Define desired tenant-root HelmRelease
	desiredHR := &helmv2.HelmRelease{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "tenant-root",
			Namespace: "tenant-root",
			Labels: map[string]string{
				"cozystack.io/ui": "true",
			},
		},
		Spec: helmv2.HelmReleaseSpec{
			Interval:    metav1.Duration{Duration: 0}, // 0s
			ReleaseName: "tenant-root",
			ChartRef: &helmv2.CrossNamespaceSourceReference{
				Kind:      "ExternalArtifact",
				Name:      "cozystack-system-tenant",
				Namespace: "cozy-public",
			},
			Values: &apiextensionsv1.JSON{
				Raw: []byte(fmt.Sprintf(`{"host":%q}`, host)),
			},
			Install: &helmv2.Install{
				Remediation: &helmv2.InstallRemediation{
					Retries: -1,
				},
			},
			Upgrade: &helmv2.Upgrade{
				Remediation: &helmv2.UpgradeRemediation{
					Retries: -1,
				},
			},
		},
	}

	if err := r.CreateOrUpdate(ctx, desiredHR); err != nil {
		logger.Error(err, "failed to reconcile tenant-root HelmRelease")
		return err
	}
	logger.Info("reconciled tenant-root HelmRelease")

	return nil
}

// CreateOrUpdate creates or updates a resource.
func (r *PlatformReconciler) CreateOrUpdate(ctx context.Context, obj client.Object) error {
	existing := obj.DeepCopyObject().(client.Object)
	key := client.ObjectKeyFromObject(obj)

	err := r.Get(ctx, key, existing)
	if apierrors.IsNotFound(err) {
		return r.Create(ctx, obj)
	} else if err != nil {
		return err
	}

	// Preserve resource version
	obj.SetResourceVersion(existing.GetResourceVersion())
	// Merge labels and annotations
	labels := obj.GetLabels()
	if labels == nil {
		labels = make(map[string]string)
	}
	for k, v := range existing.GetLabels() {
		if _, ok := labels[k]; !ok {
			labels[k] = v
		}
	}
	obj.SetLabels(labels)

	annotations := obj.GetAnnotations()
	if annotations == nil {
		annotations = make(map[string]string)
	}
	for k, v := range existing.GetAnnotations() {
		if _, ok := annotations[k]; !ok {
			annotations[k] = v
		}
	}
	obj.SetAnnotations(annotations)

	return r.Update(ctx, obj)
}
