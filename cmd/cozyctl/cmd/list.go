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

package cmd

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"
	cozyv1alpha1 "github.com/cozystack/cozystack/api/v1alpha1"
	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	_ "k8s.io/client-go/plugin/pkg/client/auth"
)

var listCmdFlags struct {
	installed   bool
	kubeconfig  string
}

var listCmd = &cobra.Command{
	Use:   "list",
	Short: "List PackageSource or Package resources",
	Long: `List PackageSource or Package resources in table format.

By default, lists PackageSource resources. Use -i flag to list installed Package resources.`,
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		ctx := context.Background()

		// Create Kubernetes client config
		var config *rest.Config
		var err error

		if listCmdFlags.kubeconfig != "" {
			config, err = clientcmd.BuildConfigFromFlags("", listCmdFlags.kubeconfig)
			if err != nil {
				return fmt.Errorf("failed to load kubeconfig from %s: %w", listCmdFlags.kubeconfig, err)
			}
		} else {
			config, err = ctrl.GetConfig()
			if err != nil {
				return fmt.Errorf("failed to get kubeconfig: %w", err)
			}
		}

		scheme := runtime.NewScheme()
		utilruntime.Must(clientgoscheme.AddToScheme(scheme))
		utilruntime.Must(cozyv1alpha1.AddToScheme(scheme))

		k8sClient, err := client.New(config, client.Options{Scheme: scheme})
		if err != nil {
			return fmt.Errorf("failed to create k8s client: %w", err)
		}

		if listCmdFlags.installed {
			return listPackages(ctx, k8sClient)
		}
		return listPackageSources(ctx, k8sClient)
	},
}

func listPackageSources(ctx context.Context, k8sClient client.Client) error {
	var psList cozyv1alpha1.PackageSourceList
	if err := k8sClient.List(ctx, &psList); err != nil {
		return fmt.Errorf("failed to list PackageSources: %w", err)
	}

	// Print header
	fmt.Fprintf(os.Stdout, "%-50s %-30s %-10s %s\n", "NAME", "VARIANTS", "READY", "STATUS")
	fmt.Fprintf(os.Stdout, "%-50s %-30s %-10s %s\n", strings.Repeat("-", 50), strings.Repeat("-", 30), strings.Repeat("-", 10), strings.Repeat("-", 50))

	// Print rows
	for _, ps := range psList.Items {
		// Get variants
		var variants []string
		for _, variant := range ps.Spec.Variants {
			variants = append(variants, variant.Name)
		}
		variantsStr := strings.Join(variants, ",")
		if len(variantsStr) > 28 {
			variantsStr = variantsStr[:25] + "..."
		}

		// Get Ready condition
		ready := "Unknown"
		status := ""
		for _, condition := range ps.Status.Conditions {
			if condition.Type == "Ready" {
				ready = string(condition.Status)
				status = condition.Message
				if len(status) > 48 {
					status = status[:45] + "..."
				}
				break
			}
		}

		fmt.Fprintf(os.Stdout, "%-50s %-30s %-10s %s\n", ps.Name, variantsStr, ready, status)
	}

	return nil
}

func listPackages(ctx context.Context, k8sClient client.Client) error {
	var pkgList cozyv1alpha1.PackageList
	if err := k8sClient.List(ctx, &pkgList); err != nil {
		return fmt.Errorf("failed to list Packages: %w", err)
	}

	// Print header
	fmt.Fprintf(os.Stdout, "%-50s %-20s %-10s %s\n", "NAME", "VARIANT", "READY", "STATUS")
	fmt.Fprintf(os.Stdout, "%-50s %-20s %-10s %s\n", strings.Repeat("-", 50), strings.Repeat("-", 20), strings.Repeat("-", 10), strings.Repeat("-", 50))

	// Print rows
	for _, pkg := range pkgList.Items {
		variant := pkg.Spec.Variant
		if variant == "" {
			variant = "default"
		}

		// Get Ready condition
		ready := "Unknown"
		status := ""
		for _, condition := range pkg.Status.Conditions {
			if condition.Type == "Ready" {
				ready = string(condition.Status)
				status = condition.Message
				if len(status) > 48 {
					status = status[:45] + "..."
				}
				break
			}
		}

		fmt.Fprintf(os.Stdout, "%-50s %-20s %-10s %s\n", pkg.Name, variant, ready, status)
	}

	return nil
}

func init() {
	listCmd.Flags().BoolVarP(&listCmdFlags.installed, "installed", "i", false, "list installed Package resources instead of PackageSource resources")
	listCmd.Flags().StringVar(&listCmdFlags.kubeconfig, "kubeconfig", "", "Path to kubeconfig file (defaults to ~/.kube/config or KUBECONFIG env var)")
}

