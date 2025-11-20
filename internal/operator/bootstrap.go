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
	"os/exec"

	cozyv1alpha1 "github.com/cozystack/cozystack/api/v1alpha1"
	helmv2 "github.com/fluxcd/helm-controller/api/v2"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"
)

// FluxIsOK checks if FluxCD is ready and operational
func FluxIsOK(ctx context.Context, c client.Client) (bool, error) {
	logger := log.FromContext(ctx)

	// Check source-controller deployment
	sourceDeploy := &appsv1.Deployment{}
	key := types.NamespacedName{Namespace: "cozy-fluxcd", Name: "source-controller"}
	if err := c.Get(ctx, key, sourceDeploy); err != nil {
		if apierrors.IsNotFound(err) {
			logger.Info("fluxcd check: source-controller deployment not found")
			return false, nil
		}
		return false, err
	}
	if !isDeploymentAvailable(sourceDeploy) {
		logger.Info("fluxcd check: source-controller deployment not available")
		return false, nil
	}

	// Check helm-controller deployment
	helmDeploy := &appsv1.Deployment{}
	key = types.NamespacedName{Namespace: "cozy-fluxcd", Name: "helm-controller"}
	if err := c.Get(ctx, key, helmDeploy); err != nil {
		if apierrors.IsNotFound(err) {
			logger.Info("fluxcd check: helm-controller deployment not found")
			return false, nil
		}
		return false, err
	}
	if !isDeploymentAvailable(helmDeploy) {
		logger.Info("fluxcd check: helm-controller deployment not available")
		return false, nil
	}

	// Check fluxcd helmrelease is ready
	hr := &helmv2.HelmRelease{}
	key = types.NamespacedName{Namespace: "cozy-fluxcd", Name: "fluxcd"}
	if err := c.Get(ctx, key, hr); err != nil {
		if apierrors.IsNotFound(err) {
			logger.Info("fluxcd check: fluxcd helmrelease not found")
			return false, nil
		}
		return false, err
	}

	// Check if ready (this implicitly checks suspend, as suspended HelmRelease cannot be Ready)
	if hr.Status.Conditions != nil {
		for _, cond := range hr.Status.Conditions {
			if cond.Type == "Ready" && cond.Status == metav1.ConditionTrue {
				logger.Info("fluxcd check: fluxcd is ready")
				return true, nil
			}
		}
	}

	logger.Info("fluxcd check: fluxcd helmrelease not ready")
	return false, nil
}

func isDeploymentAvailable(deploy *appsv1.Deployment) bool {
	for _, cond := range deploy.Status.Conditions {
		if cond.Type == appsv1.DeploymentAvailable && cond.Status == corev1.ConditionTrue {
			return true
		}
	}
	return false
}

// HasCiliumAndKubeovn checks if any CozystackBundle contains cilium and kubeovn packages
func HasCiliumAndKubeovn(ctx context.Context, c client.Client) (hasCilium, hasKubeovn bool, err error) {
	bundleList := &cozyv1alpha1.CozystackBundleList{}
	if err := c.List(ctx, bundleList); err != nil {
		return false, false, fmt.Errorf("failed to list CozystackBundles: %w", err)
	}

	for _, bundle := range bundleList.Items {
		for _, pkg := range bundle.Spec.Packages {
			if pkg.Name == "cilium" && !pkg.Disabled {
				hasCilium = true
			}
			if pkg.Name == "kubeovn" && !pkg.Disabled {
				hasKubeovn = true
			}
		}
	}

	return hasCilium, hasKubeovn, nil
}

// InstallBasicCharts installs cilium and kubeovn using make commands
func InstallBasicCharts(ctx context.Context, c client.Client) error {
	logger := log.FromContext(ctx)

	hasCilium, hasKubeovn, err := HasCiliumAndKubeovn(ctx, c)
	if err != nil {
		logger.Error(err, "Failed to check bundles for cilium/kubeovn, skipping installation")
		return nil // Don't fail, just skip
	}

	// Install cilium only if present in bundle
	if hasCilium {
		logger.Info("Installing cilium using make (found in bundle)")
		cmd := exec.CommandContext(ctx, "make", "-C", "packages/system/cilium", "apply", "resume")
		if err := cmd.Run(); err != nil {
			return fmt.Errorf("failed to install cilium: %w", err)
		}
	} else {
		logger.Info("Skipping cilium installation (not found in bundle)")
	}

	// Install kubeovn only if present in bundle
	if hasKubeovn {
		logger.Info("Installing kubeovn using make (found in bundle)")
		cmd := exec.CommandContext(ctx, "make", "-C", "packages/system/kubeovn", "apply", "resume")
		if err := cmd.Run(); err != nil {
			return fmt.Errorf("failed to install kubeovn: %w", err)
		}
	} else {
		logger.Info("Skipping kubeovn installation (not found in bundle)")
	}

	return nil
}
