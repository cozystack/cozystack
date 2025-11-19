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
	"encoding/json"
	"fmt"
	"strings"

	"gopkg.in/yaml.v3"

	helmv2 "github.com/fluxcd/helm-controller/api/v2"
	sourcev1 "github.com/fluxcd/source-controller/api/v1"
	sourcewatcherv1beta1 "github.com/fluxcd/source-watcher/api/v2/v1beta1"
	corev1 "k8s.io/api/core/v1"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	"github.com/cozystack/cozystack/pkg/config"
)

const (
	cozystackConfigMapName      = "cozystack"
	systemBundleConfigMapName   = "system-bundle"
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
	Scheme           *runtime.Scheme
	FirstReconcileDone chan struct{}
}

// +kubebuilder:rbac:groups=core,resources=configmaps,verbs=get;list;watch
// +kubebuilder:rbac:groups=core,resources=configmaps/status,verbs=get
// +kubebuilder:rbac:groups=core,resources=namespaces,verbs=get;list;watch;create;update;patch
// +kubebuilder:rbac:groups=helm.toolkit.fluxcd.io,resources=helmreleases,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=source.toolkit.fluxcd.io,resources=helmrepositories,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=source.toolkit.fluxcd.io,resources=gitrepositories,verbs=get;list;watch;create;update;patch
// +kubebuilder:rbac:groups=source.extensions.fluxcd.io,resources=artifactgenerators,verbs=get;list;watch;create;update;patch

// Reconcile is part of the main kubernetes reconciliation loop which aims to
// move the current state of the cluster closer to the desired state.
func (r *PlatformReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	// Reconcile on changes to cozystack ConfigMap or system-bundle ConfigMap
	if req.Namespace != cozystackConfigMapNamespace {
		return ctrl.Result{}, nil
	}

	// Reconcile on changes to cozystack ConfigMap
	// Also reconcile when system-bundle changes (it will read bundle-name from cozystack ConfigMap)
	if req.Name != cozystackConfigMapName && req.Name != systemBundleConfigMapName {
		return ctrl.Result{}, nil
	}

	// If system-bundle changed, we still need to get the cozystack ConfigMap to read bundle-name
	if req.Name == systemBundleConfigMapName {
		logger.Info("system-bundle ConfigMap changed, reconciling platform")
		// Continue to get cozystack ConfigMap to determine which bundle to use
		req.Name = cozystackConfigMapName
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

	// Reconcile HelmRepositories (delete old ones, we no longer create them)
	if err := r.reconcileHelmRepositories(ctx); err != nil {
		return ctrl.Result{}, fmt.Errorf("failed to reconcile HelmRepositories: %w", err)
	}

	// Reconcile GitRepository
	if err := r.reconcileGitRepository(ctx); err != nil {
		return ctrl.Result{}, fmt.Errorf("failed to reconcile GitRepository: %w", err)
	}

	// Reconcile ArtifactGenerators
	if err := r.reconcileArtifactGenerators(ctx); err != nil {
		return ctrl.Result{}, fmt.Errorf("failed to reconcile ArtifactGenerators: %w", err)
	}

	// Reconcile namespaces
	if err := r.reconcileNamespaces(ctx, cfg); err != nil {
		return ctrl.Result{}, fmt.Errorf("failed to reconcile namespaces: %w", err)
	}

	// Reconcile HelmReleases (from bundle)
	if err := r.reconcileHelmReleases(ctx, cfg); err != nil {
		return ctrl.Result{}, fmt.Errorf("failed to reconcile HelmReleases: %w", err)
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

func (r *PlatformReconciler) reconcileHelmRepositories(ctx context.Context) error {
	logger := log.FromContext(ctx)

	// We no longer create HelmRepositories, we use ArtifactGenerators instead
	// Delete all old HelmRepositories that were managed by this operator

	desiredRepos := map[string]string{} // empty - we don't want any HelmRepositories

	// List all HelmRepositories that were managed by this operator
	hrList := &sourcev1.HelmRepositoryList{}
	if err := r.List(ctx, hrList, client.MatchingLabels{
		platformOperatorLabel: "true",
	}); err != nil {
		return fmt.Errorf("failed to list HelmRepositories: %w", err)
	}

	// Also list by old labels for backward compatibility
	oldLabels := []map[string]string{
		{"cozystack.io/repository": "system"},
		{"cozystack.io/repository": "apps"},
		{"cozystack.io/repository": "extra"},
	}

	reposToCheck := make(map[types.NamespacedName]bool)
	for _, hr := range hrList.Items {
		key := types.NamespacedName{Name: hr.Name, Namespace: hr.Namespace}
		reposToCheck[key] = true
	}

	for _, labels := range oldLabels {
		oldList := &sourcev1.HelmRepositoryList{}
		if err := r.List(ctx, oldList, client.MatchingLabels(labels)); err != nil {
			logger.Error(err, "failed to list HelmRepositories by old labels")
			continue
		}
		for _, hr := range oldList.Items {
			key := types.NamespacedName{Name: hr.Name, Namespace: hr.Namespace}
			reposToCheck[key] = true
		}
	}

	// Delete all HelmRepositories that are no longer desired
	for key := range reposToCheck {
		hr := &sourcev1.HelmRepository{}
		if err := r.Get(ctx, key, hr); err != nil {
			if apierrors.IsNotFound(err) {
				continue
			}
			return fmt.Errorf("failed to get HelmRepository %s/%s: %w", key.Namespace, key.Name, err)
		}

		// Check if this HelmRepository is still desired (none are desired now)
		desiredKey := fmt.Sprintf("%s/%s", hr.Namespace, hr.Name)
		if _, ok := desiredRepos[desiredKey]; !ok {
			// Not desired anymore, delete it
			if err := r.Delete(ctx, hr); err != nil {
				if apierrors.IsNotFound(err) {
					continue
				}
				logger.Error(err, "failed to delete HelmRepository", "name", hr.Name, "namespace", hr.Namespace)
				// Continue with other deletions
			} else {
				logger.Info("deleted HelmRepository (no longer needed)", "name", hr.Name, "namespace", hr.Namespace)
			}
		}
	}

	return nil
}

func (r *PlatformReconciler) reconcileGitRepository(ctx context.Context) error {
	logger := log.FromContext(ctx)

	// Define desired GitRepository
	desiredGR := &sourcev1.GitRepository{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "cozystack",
			Namespace: "cozy-system",
			Labels: map[string]string{
				platformOperatorLabel: "true",
			},
		},
		Spec: sourcev1.GitRepositorySpec{
			URL:      "https://github.com/cozystack/cozystack.git",
			Interval: metav1.Duration{Duration: 1 * 60 * 1000000000}, // 1m
			Timeout:  &metav1.Duration{Duration: 60 * 1000000000},    // 60s
			Reference: &sourcev1.GitRepositoryRef{
				Tag: "v0.38.0-alpha.2",
			},
			Ignore: func() *string {
				ignore := `# exclude all
/*
# include packages dir
!/packages`
				return &ignore
			}(),
		},
	}

	// Create or update desired GitRepository
	if err := r.CreateOrUpdate(ctx, desiredGR); err != nil {
		logger.Error(err, "failed to reconcile GitRepository")
		return err
	}
	logger.Info("reconciled GitRepository", "name", "cozystack")

	// List all GitRepositories managed by this operator and delete unwanted ones
	grList := &sourcev1.GitRepositoryList{}
	if err := r.List(ctx, grList, client.MatchingLabels{
		platformOperatorLabel: "true",
	}); err != nil {
		return fmt.Errorf("failed to list GitRepositories: %w", err)
	}

	for _, gr := range grList.Items {
		key := types.NamespacedName{Name: gr.Name, Namespace: gr.Namespace}
		desiredKey := types.NamespacedName{Name: desiredGR.Name, Namespace: desiredGR.Namespace}
		if key != desiredKey {
			// Not desired, delete it
			if err := r.Delete(ctx, &gr); err != nil {
				if apierrors.IsNotFound(err) {
					continue
				}
				logger.Error(err, "failed to delete GitRepository", "name", gr.Name, "namespace", gr.Namespace)
				// Continue with other deletions
			} else {
				logger.Info("deleted GitRepository (not in desired state)", "name", gr.Name, "namespace", gr.Namespace)
			}
		}
	}

	return nil
}

func (r *PlatformReconciler) reconcileArtifactGenerators(ctx context.Context) error {
	logger := log.FromContext(ctx)

	// Packages that use cozy-lib
	packagesWithCozyLib := map[string]bool{
		"bootbox": true, "bucket": true, "clickhouse": true, "etcd": true,
		"ferretdb": true, "foundationdb": true, "http-cache": true, "info": true,
		"ingress": true, "kafka": true, "keycloak": true, "kubernetes": true, "monitoring": true,
		"mysql": true, "nats": true, "postgres": true, "rabbitmq": true,
		"redis": true, "seaweedfs": true, "tcp-balancer": true, "tenant": true,
		"virtual-machine": true, "vm-disk": true, "vm-instance": true,
		"vpc": true, "vpn": true,
	}

	// Convert maps to slices for ArtifactGenerator
	systemPackages := make([]string, 0, len(systemPackagesList))
	for pkg := range systemPackagesList {
		systemPackages = append(systemPackages, pkg)
	}

	appsPackages := make([]string, 0, len(appsPackagesList))
	for pkg := range appsPackagesList {
		appsPackages = append(appsPackages, pkg)
	}

	extraPackages := make([]string, 0, len(extraPackagesList))
	for pkg := range extraPackagesList {
		extraPackages = append(extraPackages, pkg)
	}

	// Define desired ArtifactGenerators
	desiredAGs := []struct {
		name      string
		namespace string
		packages  []string
	}{
		{"system", "cozy-system", systemPackages},
		{"apps", "cozy-public", appsPackages},
		{"extra", "cozy-public", extraPackages},
	}

	// Create or update desired ArtifactGenerators
	for _, ag := range desiredAGs {
		if err := r.reconcileArtifactGenerator(ctx, ag.name, ag.namespace, ag.packages, packagesWithCozyLib); err != nil {
			logger.Error(err, "failed to reconcile ArtifactGenerator", "name", ag.name)
			return err
		}
	}

	// List all ArtifactGenerators managed by this operator and delete unwanted ones
	agList := &sourcewatcherv1beta1.ArtifactGeneratorList{}
	if err := r.List(ctx, agList, client.MatchingLabels{
		platformOperatorLabel: "true",
	}); err != nil {
		// If CRD doesn't exist, just log and continue
		logger.Info("ArtifactGenerator CRD may not exist, skipping cleanup", "error", err)
	} else {
		// Build desired map
		desiredMap := make(map[types.NamespacedName]bool)
		for _, ag := range desiredAGs {
			key := types.NamespacedName{Name: ag.name, Namespace: ag.namespace}
			desiredMap[key] = true
		}

		// Delete unwanted ArtifactGenerators
		for _, item := range agList.Items {
			key := types.NamespacedName{Name: item.Name, Namespace: item.Namespace}
			if !desiredMap[key] {
				// Not desired, delete it
				if err := r.Delete(ctx, &item); err != nil {
					if apierrors.IsNotFound(err) {
						continue
					}
					logger.Error(err, "failed to delete ArtifactGenerator", "name", item.Name, "namespace", item.Namespace)
					// Continue with other deletions
				} else {
					logger.Info("deleted ArtifactGenerator (not in desired state)", "name", item.Name, "namespace", item.Namespace)
				}
			}
		}
	}

	return nil
}

func (r *PlatformReconciler) reconcileArtifactGenerator(ctx context.Context, name, namespace string, packages []string, packagesWithCozyLib map[string]bool) error {
	logger := log.FromContext(ctx)

	// Load bundle to get valuesFiles for packages
	cfg := &config.CozystackConfig{}
	configMap := &corev1.ConfigMap{}
	configMapKey := types.NamespacedName{Namespace: "cozy-system", Name: "cozystack"}
	if err := r.Get(ctx, configMapKey, configMap); err == nil {
		cfg = config.ParseConfigMapData(configMap.Data)
	}

	var bundle *Bundle
	var err error
	if cfg.BundleName != "" {
		bundle, err = loadBundle(ctx, r.Client, cfg.BundleName)
		if err != nil {
			logger.Info("failed to load bundle for valuesFiles, continuing without them", "error", err)
			bundle = nil
		}
	}

	// Build map of chart -> valuesFiles from bundle
	chartValuesFiles := make(map[string][]string)
	if bundle != nil {
		for _, release := range bundle.Releases {
			// Check if release is disabled or optional
			if cfg.IsComponentDisabled(release.Name) {
				continue
			}
			if release.Optional && !cfg.IsComponentEnabled(release.Name) {
				continue
			}
			// Store valuesFiles for this chart
			if len(release.ValuesFiles) > 0 {
				chartValuesFiles[release.Chart] = release.ValuesFiles
			}
		}
	}

	// Build output artifacts
	outputArtifacts := []sourcewatcherv1beta1.OutputArtifact{}
	for _, pkg := range packages {
		copyOps := []sourcewatcherv1beta1.CopyOperation{
			{
				From: fmt.Sprintf("@cozystack/packages/%s/%s/**", name, pkg),
				To:   fmt.Sprintf("@artifact/%s/", pkg),
			},
		}

		// Add cozy-lib if package uses it
		if packagesWithCozyLib[pkg] {
			copyOps = append(copyOps, sourcewatcherv1beta1.CopyOperation{
				From: "@cozystack/packages/library/cozy-lib/**",
				To:   fmt.Sprintf("@artifact/%s/charts/cozy-lib/", pkg),
			})
		}

		// Add valuesFiles if specified in bundle
		if valuesFiles, ok := chartValuesFiles[pkg]; ok {
			for i, valuesFile := range valuesFiles {
				// Copy values file from GitRepository to artifact
				// Path in GitRepository: packages/{name}/{pkg}/{valuesFile}
				// Path in artifact: {pkg}/{valuesFile}
				// First file should use Overwrite (to replace original values.yaml), others use Merge
				strategy := "Merge"
				if i == 0 {
					strategy = "Overwrite"
				}
				copyOps = append(copyOps, sourcewatcherv1beta1.CopyOperation{
					From:    fmt.Sprintf("@cozystack/packages/%s/%s/%s", name, pkg, valuesFile),
					To:      fmt.Sprintf("@artifact/%s/%s", pkg, valuesFile),
					Strategy: strategy,
				})
				logger.Info("Adding valuesFile copy operation", "package", pkg, "valuesFile", valuesFile, "strategy", strategy)
			}
		}

		artifactName := fmt.Sprintf("%s-%s", name, pkg)
		logger.Info("Adding output artifact to ArtifactGenerator", "artifactName", artifactName, "generator", name, "package", pkg)
		outputArtifacts = append(outputArtifacts, sourcewatcherv1beta1.OutputArtifact{
			Name: artifactName,
			Copy: copyOps,
		})
	}

	// Define desired ArtifactGenerator
	ag := &sourcewatcherv1beta1.ArtifactGenerator{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
			Labels: map[string]string{
				platformOperatorLabel: "true",
			},
		},
		Spec: sourcewatcherv1beta1.ArtifactGeneratorSpec{
			Sources: []sourcewatcherv1beta1.SourceReference{
				{
					Alias:     "cozystack",
					Kind:      "GitRepository",
					Name:      "cozystack",
					Namespace: "cozy-system",
				},
			},
			OutputArtifacts: outputArtifacts,
		},
	}

	// Get existing resource to preserve resourceVersion
	existing := &sourcewatcherv1beta1.ArtifactGenerator{}
	key := client.ObjectKey{Name: name, Namespace: namespace}
	err = r.Get(ctx, key, existing)
	if err == nil {
		ag.SetResourceVersion(existing.GetResourceVersion())
		// Merge labels and annotations
		labels := ag.GetLabels()
		if labels == nil {
			labels = make(map[string]string)
		}
		for k, v := range existing.GetLabels() {
			if _, ok := labels[k]; !ok {
				labels[k] = v
			}
		}
		ag.SetLabels(labels)

		annotations := ag.GetAnnotations()
		if annotations == nil {
			annotations = make(map[string]string)
		}
		for k, v := range existing.GetAnnotations() {
			if _, ok := annotations[k]; !ok {
				annotations[k] = v
			}
		}
		ag.SetAnnotations(annotations)

		if err := r.Update(ctx, ag); err != nil {
			return fmt.Errorf("failed to update ArtifactGenerator: %w", err)
		}
		logger.Info("updated ArtifactGenerator", "name", name, "namespace", namespace)
	} else if apierrors.IsNotFound(err) {
		if err := r.Create(ctx, ag); err != nil {
			return fmt.Errorf("failed to create ArtifactGenerator: %w", err)
		}
		logger.Info("created ArtifactGenerator", "name", name, "namespace", namespace)
	} else {
		return fmt.Errorf("failed to get ArtifactGenerator: %w", err)
	}

	return nil
}

func (r *PlatformReconciler) reconcileNamespaces(ctx context.Context, cfg *config.CozystackConfig) error {
	logger := log.FromContext(ctx)

	// Load bundle to determine namespaces
	bundle, err := loadBundle(ctx, r.Client, cfg.BundleName)
	if err != nil {
		logger.Error(err, "failed to load bundle, using default namespaces", "bundle", cfg.BundleName)
		// Fallback to default namespaces if bundle loading fails
		bundle = nil
	}

	// Collect namespaces from bundle releases
	namespacesMap := make(map[string]bool)

	if bundle != nil {
		for _, release := range bundle.Releases {
			// Check if release is disabled
			if cfg.IsComponentDisabled(release.Name) {
				continue
			}

			// Check if optional release is enabled
			if release.Optional && !cfg.IsComponentEnabled(release.Name) {
				continue
			}

			// If at least one release requires a privileged namespace, then it should be privileged
			if release.Namespace != "" {
				if release.Privileged {
					namespacesMap[release.Namespace] = true
				} else if _, exists := namespacesMap[release.Namespace]; !exists {
					namespacesMap[release.Namespace] = false
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

	// List all namespaces managed by this operator and delete unwanted ones
	nsList := &corev1.NamespaceList{}
	if err := r.List(ctx, nsList, client.MatchingLabels{
		"cozystack.io/system": "true",
	}); err != nil {
		return fmt.Errorf("failed to list namespaces: %w", err)
	}

	for _, ns := range nsList.Items {
		// Skip tenant-root as it's managed separately
		if ns.Name == "tenant-root" {
			continue
		}

		if _, ok := namespacesMap[ns.Name]; !ok {
			// Not desired, delete it
			if err := r.Delete(ctx, &ns); err != nil {
				if apierrors.IsNotFound(err) {
					continue
				}
				logger.Error(err, "failed to delete Namespace", "name", ns.Name)
				// Continue with other deletions
			} else {
				logger.Info("deleted Namespace (not in desired state)", "name", ns.Name)
			}
		}
	}

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
			Chart: &helmv2.HelmChartTemplate{
				Spec: helmv2.HelmChartTemplateSpec{
					Chart:   "tenant",
					Version: ">= 0.0.0-0",
					SourceRef: helmv2.CrossNamespaceObjectReference{
						Kind:      "HelmRepository",
						Name:      "cozystack-apps",
						Namespace: "cozy-public",
					},
				},
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

func (r *PlatformReconciler) reconcileHelmReleases(ctx context.Context, cfg *config.CozystackConfig) error {
	logger := log.FromContext(ctx)

	// Load bundle to determine desired HelmReleases
	bundle, err := loadBundle(ctx, r.Client, cfg.BundleName)
	if err != nil {
		logger.Error(err, "failed to load bundle, skipping HelmReleases reconciliation", "bundle", cfg.BundleName)
		return nil // Don't fail if bundle loading fails
	}

	// Determine desired HelmReleases from bundle
	desiredReleases := make(map[string]map[string]bool) // namespace -> name -> true
	for _, release := range bundle.Releases {
		// Check if release is disabled
		if cfg.IsComponentDisabled(release.Name) {
			continue
		}

		// Check if optional release is enabled
		if release.Optional && !cfg.IsComponentEnabled(release.Name) {
			continue
		}

		if release.Namespace == "" {
			logger.Info("skipping release without namespace", "name", release.Name)
			continue
		}

		if desiredReleases[release.Namespace] == nil {
			desiredReleases[release.Namespace] = make(map[string]bool)
		}
		desiredReleases[release.Namespace][release.Name] = true

		// Create or update HelmRelease
		hr := &helmv2.HelmRelease{
			ObjectMeta: metav1.ObjectMeta{
				Name:      release.Name,
				Namespace: release.Namespace,
				Labels: map[string]string{
					platformOperatorLabel: "true",
				},
			},
			Spec: helmv2.HelmReleaseSpec{
				Interval:    metav1.Duration{Duration: 5 * 60 * 1000000000}, // 5m
				ReleaseName: release.ReleaseName,
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

		// All packages now use chartRef (ExternalArtifact)
		// Determine artifact prefix and namespace based on package category
		artifactPrefix, artifactNamespace, found := getArtifactPrefixAndNamespace(release.Chart)
		if !found {
			logger.Error(fmt.Errorf("unknown package category"), "skipping HelmRelease with unknown package", "name", release.Name, "namespace", release.Namespace, "chart", release.Chart)
			continue
		}

		artifactName := fmt.Sprintf("%s-%s", artifactPrefix, release.Chart)
		logger.Info("Creating HelmRelease with chartRef", "name", release.Name, "namespace", release.Namespace, "chart", release.Chart, "artifactName", artifactName, "artifactNamespace", artifactNamespace)
		hr.Spec.ChartRef = &helmv2.CrossNamespaceSourceReference{
			Kind:      "ExternalArtifact",
			Name:      artifactName,
			Namespace: artifactNamespace,
		}

		// Set values if provided
		if len(release.Values) > 0 {
			valuesJSON, err := json.Marshal(release.Values)
			if err != nil {
				logger.Error(err, "failed to marshal values for release", "name", release.Name)
				return fmt.Errorf("failed to marshal values for release %s: %w", release.Name, err)
			}
			hr.Spec.Values = &apiextensionsv1.JSON{
				Raw: valuesJSON,
			}
		}

		// Get component-specific values from config if available
		if componentValues, ok := cfg.GetComponentValues(release.Name); ok {
			// Merge with existing values if any
			mergedValues := make(map[string]interface{})
			if len(release.Values) > 0 {
				mergedValues = release.Values
			}
			// Parse component values (they might be YAML or JSON string)
			var componentValuesMap map[string]interface{}
			if err := yaml.Unmarshal([]byte(componentValues), &componentValuesMap); err != nil {
				logger.Error(err, "failed to parse component values for release", "name", release.Name)
				return fmt.Errorf("failed to parse component values for release %s: %w", release.Name, err)
			}
			// Merge maps (component values override bundle values)
			for k, v := range componentValuesMap {
				mergedValues[k] = v
			}
			// Marshal merged values
			valuesJSON, err := json.Marshal(mergedValues)
			if err != nil {
				logger.Error(err, "failed to marshal merged values for release", "name", release.Name)
				return fmt.Errorf("failed to marshal merged values for release %s: %w", release.Name, err)
			}
			hr.Spec.Values = &apiextensionsv1.JSON{
				Raw: valuesJSON,
			}
		}

		// Set DependsOn if provided
		// DependsOn is a list of release names, we need to convert them to HelmRelease references
		if len(release.DependsOn) > 0 {
			dependsOn := make([]helmv2.DependencyReference, 0, len(release.DependsOn))
			for _, depName := range release.DependsOn {
				// Find the dependent release in the bundle to get its namespace
				var depNamespace string
				for _, depRelease := range bundle.Releases {
					if depRelease.Name == depName {
						depNamespace = depRelease.Namespace
						break
					}
				}
				// If namespace not found, use the same namespace as current release
				if depNamespace == "" {
					depNamespace = release.Namespace
					logger.Info("dependent release not found in bundle, using same namespace", "name", release.Name, "dependsOn", depName)
				}
				dependsOn = append(dependsOn, helmv2.DependencyReference{
					Name:      depName,
					Namespace: depNamespace,
				})
			}
			hr.Spec.DependsOn = dependsOn
			logger.Info("set DependsOn for HelmRelease", "name", release.Name, "dependsOn", release.DependsOn)
		}

		// Set valuesFiles annotation for cozypkg
		// Initialize annotations if needed
		if hr.Annotations == nil {
			hr.Annotations = make(map[string]string)
		}
		if len(release.ValuesFiles) > 0 {
			// Format: comma-separated string, e.g., "values.yaml,values-cilium.yaml"
			hr.Annotations["cozypkg.cozystack.io/values-files"] = strings.Join(release.ValuesFiles, ",")
			logger.Info("set valuesFiles annotation for HelmRelease", "name", release.Name, "valuesFiles", release.ValuesFiles)
		} else {
			// Remove annotation if valuesFiles is empty
			delete(hr.Annotations, "cozypkg.cozystack.io/values-files")
		}

		if err := r.CreateOrUpdate(ctx, hr); err != nil {
			logger.Error(err, "failed to reconcile HelmRelease", "name", release.Name, "namespace", release.Namespace)
			return err
		}
		logger.Info("reconciled HelmRelease", "name", release.Name, "namespace", release.Namespace)
	}

	// Find all HelmReleases managed by this operator
	hrList := &helmv2.HelmReleaseList{}
	if err := r.List(ctx, hrList, client.MatchingLabels{
		platformOperatorLabel: "true",
	}); err != nil {
		return fmt.Errorf("failed to list HelmReleases: %w", err)
	}

	// Delete HelmReleases that are no longer desired
	for _, hr := range hrList.Items {
		// Skip tenant-root as it's managed separately
		if hr.Name == "tenant-root" && hr.Namespace == "tenant-root" {
			continue
		}

		// Check if this HelmRelease is still desired
		if desiredNamespaces, ok := desiredReleases[hr.Namespace]; ok {
			if desiredNamespaces[hr.Name] {
				// Still desired, keep it
				continue
			}
		}

		// Not desired anymore, delete it
		if err := r.Delete(ctx, &hr); err != nil {
			if apierrors.IsNotFound(err) {
				// Already deleted, ignore
				continue
			}
			logger.Error(err, "failed to delete HelmRelease", "name", hr.Name, "namespace", hr.Namespace)
			// Continue with other deletions
		} else {
			logger.Info("deleted HelmRelease (no longer in bundle)", "name", hr.Name, "namespace", hr.Namespace)
		}
	}

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

// Bundle represents the structure of a bundle YAML file.
type Bundle struct {
	Releases []BundleRelease `yaml:"releases"`
}

// BundleRelease represents a single release in a bundle.
type BundleRelease struct {
	Name        string                 `yaml:"name"`
	ReleaseName string                 `yaml:"releaseName"`
	Chart       string                 `yaml:"chart"`
	Namespace   string                 `yaml:"namespace"`
	Privileged  bool                   `yaml:"privileged,omitempty"`
	Optional    bool                   `yaml:"optional,omitempty"`
	DependsOn   []string               `yaml:"dependsOn,omitempty"`
	Values      map[string]interface{} `yaml:"values,omitempty"`
	ValuesFiles []string               `yaml:"valuesFiles,omitempty"`
}

// loadBundle loads a bundle from the system-bundle ConfigMap in cozy-system namespace.
// The ConfigMap contains all bundles as data keys: {bundleName}.yaml
func loadBundle(ctx context.Context, c client.Client, bundleName string) (*Bundle, error) {
	if bundleName == "" {
		return nil, fmt.Errorf("bundle name is empty")
	}

	// Get the system-bundle ConfigMap
	configMap := &corev1.ConfigMap{}
	key := types.NamespacedName{Namespace: "cozy-system", Name: "system-bundle"}
	if err := c.Get(ctx, key, configMap); err != nil {
		if apierrors.IsNotFound(err) {
			return nil, fmt.Errorf("system-bundle ConfigMap not found in cozy-system namespace")
		}
		return nil, fmt.Errorf("failed to get system-bundle ConfigMap: %w", err)
	}

	// Get the bundle data from ConfigMap
	bundleKey := bundleName + ".yaml"
	bundleData, ok := configMap.Data[bundleKey]
	if !ok {
		return nil, fmt.Errorf("bundle %s not found in system-bundle ConfigMap (available keys: %v)", bundleName, getMapKeys(configMap.Data))
	}

	var bundle Bundle
	// Note: bundle files contain Helm template syntax ({{ }}), but after Helm rendering
	// they should be valid YAML. If template syntax remains, it will be treated as strings
	// which is fine for extracting namespace and privileged fields
	if err := yaml.Unmarshal([]byte(bundleData), &bundle); err != nil {
		return nil, fmt.Errorf("failed to parse bundle %s from ConfigMap: %w", bundleName, err)
	}

	return &bundle, nil
}

// getMapKeys returns a slice of keys from a map for error messages
func getMapKeys(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	return keys
}
