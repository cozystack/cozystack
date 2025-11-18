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

	"gopkg.in/yaml.v3"

	helmv2 "github.com/fluxcd/helm-controller/api/v2"
	sourcev1 "github.com/fluxcd/source-controller/api/v1"
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

// PlatformReconciler reconciles the platform configuration.
type PlatformReconciler struct {
	client.Client
	Scheme *runtime.Scheme
}

// +kubebuilder:rbac:groups=core,resources=configmaps,verbs=get;list;watch
// +kubebuilder:rbac:groups=core,resources=configmaps/status,verbs=get
// +kubebuilder:rbac:groups=core,resources=namespaces,verbs=get;list;watch;create;update;patch
// +kubebuilder:rbac:groups=helm.toolkit.fluxcd.io,resources=helmreleases,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=source.toolkit.fluxcd.io,resources=helmrepositories,verbs=get;list;watch;create;update;patch
// +kubebuilder:rbac:groups=source.toolkit.fluxcd.io,resources=gitrepositories,verbs=get;list;watch;create;update;patch

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

	// Reconcile HelmRepository resources
	if err := r.reconcileHelmRepositories(ctx, cfg); err != nil {
		return ctrl.Result{}, fmt.Errorf("failed to reconcile HelmRepositories: %w", err)
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

	return ctrl.Result{}, nil
}

// SetupWithManager sets up the controller with the Manager.
func (r *PlatformReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		Named("platform-operator").
		For(&corev1.ConfigMap{}).
		Complete(r)
}

func (r *PlatformReconciler) reconcileHelmRepositories(ctx context.Context, cfg *config.CozystackConfig) error {
	logger := log.FromContext(ctx)

	repos := []struct {
		name      string
		namespace string
		url       string
		labels    map[string]string
	}{
		{
			name:      "cozystack-system",
			namespace: "cozy-system",
			url:       "http://cozystack.cozy-system.svc/repos/system",
			labels: map[string]string{
				"cozystack.io/repository": "system",
			},
		},
		{
			name:      "cozystack-apps",
			namespace: "cozy-public",
			url:       "http://cozystack.cozy-system.svc/repos/apps",
			labels: map[string]string{
				"cozystack.io/ui":         "true",
				"cozystack.io/repository": "apps",
			},
		},
		{
			name:      "cozystack-extra",
			namespace: "cozy-public",
			url:       "http://cozystack.cozy-system.svc/repos/extra",
			labels: map[string]string{
				"cozystack.io/repository": "extra",
			},
		},
	}

	for _, repo := range repos {
		hr := &sourcev1.HelmRepository{
			ObjectMeta: metav1.ObjectMeta{
				Name:      repo.name,
				Namespace: repo.namespace,
				Labels:    repo.labels,
			},
			Spec: sourcev1.HelmRepositorySpec{
				URL:      repo.url,
				Interval: metav1.Duration{Duration: 5 * 60 * 1000000000}, // 5m
			},
		}

		if err := r.CreateOrUpdate(ctx, hr); err != nil {
			logger.Error(err, "failed to reconcile HelmRepository", "name", repo.name, "namespace", repo.namespace)
			return err
		}
		logger.Info("reconciled HelmRepository", "name", repo.name, "namespace", repo.namespace)
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

	// Create/update all namespaces
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

	return nil
}

func (r *PlatformReconciler) reconcileTenantRoot(ctx context.Context, cfg *config.CozystackConfig) error {
	logger := log.FromContext(ctx)

	host := cfg.RootHost
	if host == "" {
		host = "example.org"
	}

	// Reconcile tenant-root namespace
	namespace := &corev1.Namespace{
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

	if err := r.CreateOrUpdate(ctx, namespace); err != nil {
		logger.Error(err, "failed to reconcile tenant-root Namespace")
		return err
	}

	// Reconcile tenant-root HelmRelease
	hr := &helmv2.HelmRelease{
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

	if err := r.CreateOrUpdate(ctx, hr); err != nil {
		logger.Error(err, "failed to reconcile tenant-root HelmRelease")
		return err
	}

	logger.Info("reconciled tenant-root")
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
				Chart: &helmv2.HelmChartTemplate{
					Spec: helmv2.HelmChartTemplateSpec{
						Chart:   release.Chart,
						Version: ">= 0.0.0-0",
						SourceRef: helmv2.CrossNamespaceObjectReference{
							Kind:      "HelmRepository",
							Name:      "cozystack-system",
							Namespace: "cozy-system",
						},
					},
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
