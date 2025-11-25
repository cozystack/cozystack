/*
Copyright 2025.

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

package controller

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	helmv2 "github.com/fluxcd/helm-controller/api/v2"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"
)

// +kubebuilder:rbac:groups=core,resources=namespaces,verbs=get;list;watch
// +kubebuilder:rbac:groups=helm.toolkit.fluxcd.io,resources=helmreleases,verbs=get;list;watch;update;patch
type NamespaceHelmReconciler struct {
	client.Client
	Scheme *runtime.Scheme
}

// Reconcile processes namespace changes and updates HelmReleases with namespace labels
func (r *NamespaceHelmReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	// Get the namespace
	namespace := &corev1.Namespace{}
	if err := r.Get(ctx, req.NamespacedName, namespace); err != nil {
		logger.Error(err, "unable to fetch Namespace")
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	// Extract namespace.cozystack.io/* labels
	namespaceLabels := extractNamespaceLabelsFromNamespace(namespace)
	if len(namespaceLabels) == 0 {
		// No namespace labels to process, skip
		return ctrl.Result{}, nil
	}

	logger.Info("processing namespace labels", "namespace", namespace.Name, "labels", namespaceLabels)

	// List all HelmReleases in this namespace
	helmReleaseList := &helmv2.HelmReleaseList{}
	if err := r.List(ctx, helmReleaseList, client.InNamespace(namespace.Name)); err != nil {
		logger.Error(err, "unable to list HelmReleases in namespace", "namespace", namespace.Name)
		return ctrl.Result{}, err
	}

	// Update each HelmRelease with namespace labels
	updated := 0
	for i := range helmReleaseList.Items {
		hr := &helmReleaseList.Items[i]
		if err := r.updateHelmReleaseWithNamespaceLabels(ctx, hr, namespaceLabels); err != nil {
			logger.Error(err, "failed to update HelmRelease", "name", hr.Name, "namespace", hr.Namespace)
			continue
		}
		updated++
	}

	if updated > 0 {
		logger.Info("updated HelmReleases with namespace labels", "namespace", namespace.Name, "count", updated)
	}

	return ctrl.Result{}, nil
}

// extractNamespaceLabelsFromNamespace extracts namespace.cozystack.io/* labels from namespace
func extractNamespaceLabelsFromNamespace(ns *corev1.Namespace) map[string]string {
	namespaceLabels := make(map[string]string)
	prefix := "namespace.cozystack.io/"

	if ns.Labels == nil {
		return namespaceLabels
	}

	for key, value := range ns.Labels {
		if strings.HasPrefix(key, prefix) {
			// Remove prefix and add to namespace labels
			namespaceKey := strings.TrimPrefix(key, prefix)
			namespaceLabels[namespaceKey] = value
		}
	}

	return namespaceLabels
}

// updateHelmReleaseWithNamespaceLabels updates HelmRelease values with namespace labels
func (r *NamespaceHelmReconciler) updateHelmReleaseWithNamespaceLabels(ctx context.Context, hr *helmv2.HelmRelease, namespaceLabels map[string]string) error {
	logger := log.FromContext(ctx)

	// Parse current values
	var valuesMap map[string]interface{}
	if hr.Spec.Values != nil && len(hr.Spec.Values.Raw) > 0 {
		if err := json.Unmarshal(hr.Spec.Values.Raw, &valuesMap); err != nil {
			return fmt.Errorf("failed to unmarshal HelmRelease values: %w", err)
		}
	} else {
		valuesMap = make(map[string]interface{})
	}

	// Convert namespaceLabels from map[string]string to map[string]interface{}
	namespaceLabelsMap := make(map[string]interface{})
	for k, v := range namespaceLabels {
		namespaceLabelsMap[k] = v
	}

	// Check if namespace labels need to be updated (top-level _namespace field)
	needsUpdate := false
	currentNamespace, exists := valuesMap["_namespace"]
	if !exists {
		needsUpdate = true
		valuesMap["_namespace"] = namespaceLabelsMap
	} else {
		currentNamespaceMap, ok := currentNamespace.(map[string]interface{})
		if !ok {
			needsUpdate = true
			valuesMap["_namespace"] = namespaceLabelsMap
		} else {
			// Compare and update if different
			for k, v := range namespaceLabelsMap {
				if currentVal, exists := currentNamespaceMap[k]; !exists || currentVal != v {
					needsUpdate = true
					currentNamespaceMap[k] = v
				}
			}
			// Remove keys that are no longer in namespace labels
			for k := range currentNamespaceMap {
				if _, exists := namespaceLabelsMap[k]; !exists {
					needsUpdate = true
					delete(currentNamespaceMap, k)
				}
			}
			if needsUpdate {
				valuesMap["_namespace"] = currentNamespaceMap
			}
		}
	}

	if !needsUpdate {
		// No changes needed
		return nil
	}

	// Marshal back to JSON
	mergedJSON, err := json.Marshal(valuesMap)
	if err != nil {
		return fmt.Errorf("failed to marshal values with namespace labels: %w", err)
	}

	// Update HelmRelease
	patchTarget := hr.DeepCopy()
	patchTarget.Spec.Values = &apiextensionsv1.JSON{Raw: mergedJSON}

	patch := client.MergeFrom(hr)
	if err := r.Patch(ctx, patchTarget, patch); err != nil {
		return fmt.Errorf("failed to patch HelmRelease: %w", err)
	}

	logger.Info("updated HelmRelease with namespace labels", "name", hr.Name, "namespace", hr.Namespace)
	return nil
}

// SetupWithManager sets up the controller with the Manager
func (r *NamespaceHelmReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&corev1.Namespace{}).
		Complete(r)
}

